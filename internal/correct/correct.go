// Package correct implements the Polish autocorrection state machine.
//
// The Corrector consumes raw key events (keycode + press/release/repeat)
// and maintains a small in-memory word buffer plus modifier state. That is
// all the ordinary-typing path does: no dictionary access, no allocation,
// no I/O. Only when a separator key commits a word does it consult the
// dictionary index, and then it may return a Plan — "send N backspaces,
// type S" — which the caller executes through an output backend.
//
// The package is deliberately free of evdev device access, uinput, and
// file I/O so the full pipeline is unit-testable with synthetic events.
package correct

import (
	"strings"
	"sync/atomic"
	"unicode"
	"unicode/utf8"

	evdev "github.com/holoplot/go-evdev"

	"github.com/andresousadotpt/texpand/internal/fold"
	"github.com/andresousadotpt/texpand/internal/keymap"
)

// KeyEvent is a single key event. Value: 0=release, 1=press, 2=repeat.
type KeyEvent struct {
	Code  evdev.EvCode
	Value int32
}

// Plan is a text edit for the output backend: delete Backspaces characters
// before the cursor, then type Type.
type Plan struct {
	Backspaces int
	Type       string
}

// Result is what handling one event produced.
type Result struct {
	// Plan, if non-nil, is a correction (or undo) to execute.
	Plan *Plan
	// Toggled is true when the toggle shortcut was pressed; the caller
	// flips the enabled state.
	Toggled bool
	// Undo is true when Plan reverts the previous correction.
	Undo bool
}

// Lookup is the dictionary interface the corrector queries at word
// boundaries. Implementations must be safe for use from the event loop
// goroutine and must not block.
type Lookup interface {
	// IsWord reports whether the lowercase ASCII word is already valid.
	IsWord(lower string) bool
	// Candidates appends the distinct diacritic candidates for the folded
	// key to buf and returns it.
	Candidates(folded string, buf []string) []string
}

// Options configures the corrector.
type Options struct {
	MinWordLength int // shorter words are never corrected (default 2)
	MaxWordLength int // longer words are never corrected (default 32)

	OnSpace   bool // correct when Space commits a word
	OnEnter   bool // correct when Enter commits a word
	OnTab     bool // correct when Tab commits a word
	OnPunct   bool // correct on . , ! ? : ;
	OnClosers bool // correct on ) ] } "

	Undo   bool // immediate Backspace reverts the last correction
	Toggle keymap.Shortcut

	// ShouldCorrect, if non-nil, is consulted at each word boundary; it
	// returns false when the active application is excluded. It must be
	// fast and non-blocking.
	ShouldCorrect func() bool
}

// DefaultOptions returns the conservative defaults.
func DefaultOptions() Options {
	return Options{
		MinWordLength: 2,
		MaxWordLength: 32,
		OnSpace:       true,
		OnPunct:       true,
		OnClosers:     true,
		Undo:          true,
	}
}

// undoState remembers the last correction while it is still revertible.
type undoState struct {
	active   bool
	typed    string // what the user had typed, with case
	outRunes int    // rune length of the corrected word we emitted
}

// Corrector is the autocorrection state machine. All methods except
// Enabled/SetEnabled must be called from a single goroutine (the event
// loop).
type Corrector struct {
	opts    Options
	enabled atomic.Bool
	lookup  Lookup

	buf      []rune
	impure   bool // token contains non-letters (digits, '-', "'", ...)
	overflow bool

	// suppressed disables correction for the current word: set after
	// cursor movement, editing shortcuts, undo, or anything else that
	// makes the buffer an unreliable picture of the text on screen. It
	// clears when a boundary or a fresh word start is typed.
	suppressed bool

	shift, ctrl, lalt, altgr, meta bool
	caps                           bool

	undo    undoState
	candBuf []string

	// pending holds a correction that fired while Shift was physically
	// held (e.g. on '!'). Compositors merge modifier state across
	// keyboards, so typing through the virtual keyboard now would come
	// out uppercase; the plan is released when Shift goes up, or dropped
	// if any other key intervenes.
	pending     *Plan
	pendingUndo undoState
}

// New creates a Corrector. The dictionary can be attached later with
// SetLookup (it loads in the background at startup).
func New(opts Options) *Corrector {
	if opts.MinWordLength <= 0 {
		opts.MinWordLength = 2
	}
	if opts.MaxWordLength <= 0 {
		opts.MaxWordLength = 32
	}
	c := &Corrector{
		opts:    opts,
		buf:     make([]rune, 0, 64),
		candBuf: make([]string, 0, 8),
	}
	c.enabled.Store(true)
	return c
}

// SetLookup attaches the dictionary index. Must be called from the event
// loop goroutine.
func (c *Corrector) SetLookup(l Lookup) { c.lookup = l }

// SetOptions replaces the options (config hot-reload) and invalidates the
// current word. Enabled state and the dictionary are preserved. Must be
// called from the event loop goroutine.
func (c *Corrector) SetOptions(opts Options) {
	if opts.MinWordLength <= 0 {
		opts.MinWordLength = 2
	}
	if opts.MaxWordLength <= 0 {
		opts.MaxWordLength = 32
	}
	c.opts = opts
	c.Invalidate()
}

// Ready reports whether a dictionary is attached.
func (c *Corrector) Ready() bool { return c.lookup != nil }

// Enabled reports whether autocorrection is on. Safe from any goroutine.
func (c *Corrector) Enabled() bool { return c.enabled.Load() }

// SetEnabled turns autocorrection on or off. Safe from any goroutine.
func (c *Corrector) SetEnabled(v bool) { c.enabled.Store(v) }

// SetCapsLock seeds the Caps Lock state (read from the keyboard LED).
func (c *Corrector) SetCapsLock(on bool) { c.caps = on }

// Reset clears all transient typing state (keyboard hotplug, external
// text expansion, etc.). Modifier *held* state is cleared too, matching
// the expander's behaviour on device changes.
func (c *Corrector) Reset() {
	c.clearWord()
	c.suppressed = false
	c.undo.active = false
	c.pending = nil
	c.shift, c.ctrl, c.lalt, c.altgr, c.meta = false, false, false, false, false
}

// Invalidate marks the current word as unreliable (e.g. the expander just
// rewrote text) without touching modifier state.
func (c *Corrector) Invalidate() {
	c.clearWord()
	c.suppressed = true
	c.undo.active = false
	c.pending = nil
}

func (c *Corrector) clearWord() {
	c.buf = c.buf[:0]
	c.impure = false
	c.overflow = false
}

// maybeReleasePending emits a deferred correction once no output-altering
// modifier (Shift, AltGr) is physically held anymore.
func (c *Corrector) maybeReleasePending() Result {
	if c.pending == nil || c.shift || c.altgr {
		return Result{}
	}
	plan := c.pending
	c.pending = nil
	c.undo = c.pendingUndo
	return Result{Plan: plan}
}

// separator returns the committed separator rune and whether correction
// is enabled for it, for a key that ends a word. ok is false when the key
// is not a word boundary.
func (c *Corrector) separator(code evdev.EvCode, ch rune, hasChar bool) (sep rune, correct bool, ok bool) {
	switch code {
	case evdev.KEY_ENTER, evdev.KEY_KPENTER:
		return '\n', c.opts.OnEnter, true
	case evdev.KEY_TAB:
		return '\t', c.opts.OnTab, true
	}
	if !hasChar {
		return 0, false, false
	}
	switch ch {
	case ' ':
		return ch, c.opts.OnSpace, true
	case '.', ',', '!', '?', ':', ';':
		return ch, c.opts.OnPunct, true
	case ')', ']', '}', '"':
		return ch, c.opts.OnClosers, true
	}
	return 0, false, false
}

// HandleEvent processes one key event. The ordinary-typing path (letters,
// modifiers) performs no allocation and no dictionary access.
func (c *Corrector) HandleEvent(ev KeyEvent) Result {
	// Modifier state tracks presses and releases.
	switch ev.Code {
	case evdev.KEY_LEFTSHIFT, evdev.KEY_RIGHTSHIFT:
		c.shift = ev.Value > 0
		return c.maybeReleasePending()
	case evdev.KEY_LEFTCTRL, evdev.KEY_RIGHTCTRL:
		c.ctrl = ev.Value > 0
		return Result{}
	case evdev.KEY_LEFTALT:
		c.lalt = ev.Value > 0
		return Result{}
	case evdev.KEY_RIGHTALT: // AltGr on the Polish Programmer layout
		c.altgr = ev.Value > 0
		return c.maybeReleasePending()
	case evdev.KEY_LEFTMETA, evdev.KEY_RIGHTMETA:
		c.meta = ev.Value > 0
		return Result{}
	}

	if ev.Value == 0 {
		return Result{}
	}

	// Any key-down before Shift was released: the text has moved past the
	// separator, so a pending correction no longer applies.
	c.pending = nil

	if ev.Code == evdev.KEY_CAPSLOCK {
		if ev.Value == 1 {
			c.caps = !c.caps
		}
		return Result{}
	}

	// The toggle shortcut works even while disabled.
	if ev.Value == 1 && !c.opts.Toggle.IsZero() && ev.Code == c.opts.Toggle.Code &&
		c.ctrl == c.opts.Toggle.Ctrl && c.lalt == c.opts.Toggle.Alt &&
		c.shift == c.opts.Toggle.Shift && c.meta == c.opts.Toggle.Meta {
		c.Invalidate()
		return Result{Toggled: true}
	}

	if !c.enabled.Load() {
		if len(c.buf) > 0 || c.suppressed || c.undo.active {
			c.clearWord()
			c.suppressed = false
			c.undo.active = false
		}
		return Result{}
	}

	// A chord with Ctrl/Alt/Super is a shortcut, not text: whatever it
	// did (paste, delete-word, switch tab...) the buffer no longer
	// reflects the screen.
	if c.ctrl || c.lalt || c.meta {
		c.clearWord()
		c.suppressed = true
		c.undo.active = false
		return Result{}
	}

	if ev.Code == evdev.KEY_BACKSPACE {
		if c.undo.active && ev.Value == 1 {
			// The physical Backspace has just deleted the separator; we
			// delete the corrected word and restore what was typed.
			plan := &Plan{Backspaces: c.undo.outRunes, Type: c.undo.typed}
			c.buf = append(c.buf[:0], []rune(c.undo.typed)...)
			c.impure = false
			c.overflow = false
			c.suppressed = true // do not re-correct the restored word
			c.undo.active = false
			return Result{Plan: plan, Undo: true}
		}
		c.undo.active = false
		if len(c.buf) > 0 {
			c.buf = c.buf[:len(c.buf)-1]
		} else {
			// Deleting into text we did not observe.
			c.suppressed = true
		}
		return Result{}
	}

	// Any key other than Backspace commits the previous correction.
	c.undo.active = false

	kc, hasChar := keymap.Chars[ev.Code]
	var ch rune
	if hasChar {
		s := kc.Normal
		if c.shift {
			s = kc.Shifted
		}
		ch, _ = utf8.DecodeRuneInString(s)
	}

	// Word boundary?
	if sep, correctHere, ok := c.separator(ev.Code, ch, hasChar); ok {
		return c.commitWord(correctHere, sep)
	}

	// AltGr diacritics: the user typed a Polish letter directly.
	if c.altgr {
		if lower, ok := keymap.AltGr[ev.Code]; ok {
			r := lower
			if c.shift != c.caps {
				r = unicode.ToUpper(r)
			}
			c.appendRune(r, true)
			return Result{}
		}
		// AltGr chord we do not understand (e.g. AltGr+u = € on some
		// layouts): the buffer no longer matches the screen.
		c.clearWord()
		c.suppressed = true
		return Result{}
	}

	if hasChar {
		if ch >= 'a' && ch <= 'z' {
			if c.caps != c.shift {
				ch = unicode.ToUpper(ch)
			}
			c.appendRune(ch, true)
			return Result{}
		}
		if ch >= 'A' && ch <= 'Z' {
			if c.caps {
				ch = unicode.ToLower(ch)
			}
			c.appendRune(ch, true)
			return Result{}
		}
		switch ch {
		case '(', '[', '{':
			// Openers start a fresh word context.
			c.clearWord()
			c.suppressed = false
			return Result{}
		}
		// Every other printable character (digits, '-', "'", '/', '@',
		// ...) joins the token but marks it uncorrectable: identifiers,
		// paths, e-mails, contractions and hyphenated compounds are
		// never rewritten.
		c.appendRune(ch, false)
		return Result{}
	}

	// Unknown or navigation key (arrows, Home/End, Delete, F-keys,
	// keypad...): the cursor may have moved — the buffer is unreliable.
	c.clearWord()
	c.suppressed = true
	return Result{}
}

func (c *Corrector) appendRune(r rune, pure bool) {
	if !pure {
		c.impure = true
	}
	if len(c.buf) >= cap(c.buf) {
		// Never grow: an over-long token is not correctable anyway.
		c.overflow = true
		return
	}
	c.buf = append(c.buf, r)
}

// commitWord handles a word boundary: possibly produce a correction plan,
// then reset per-word state.
func (c *Corrector) commitWord(correctHere bool, sep rune) Result {
	word := c.buf
	suppressed := c.suppressed
	impure := c.impure || c.overflow

	defer func() {
		c.clearWord()
		c.suppressed = false
	}()

	if !correctHere || suppressed || impure {
		return Result{}
	}
	if len(word) < c.opts.MinWordLength || len(word) > c.opts.MaxWordLength {
		return Result{}
	}
	if c.lookup == nil {
		return Result{}
	}
	if c.opts.ShouldCorrect != nil && !c.opts.ShouldCorrect() {
		return Result{}
	}

	typed := string(word)
	lower := strings.ToLower(typed)

	// 1. Words already containing diacritics are left alone.
	if fold.HasDiacritics(lower) {
		return Result{}
	}
	if !fold.IsASCIILetters(lower) {
		return Result{}
	}
	// 2. Words that are already valid are left alone.
	if c.lookup.IsWord(lower) {
		return Result{}
	}
	// 3. Exactly one distinct candidate.
	c.candBuf = c.candBuf[:0]
	cands := c.lookup.Candidates(lower, c.candBuf)
	c.candBuf = cands[:0]
	if len(cands) != 1 {
		return Result{}
	}
	// 4. The candidate must differ (guaranteed — candidates always carry
	// diacritics — but cheap to assert).
	if cands[0] == lower {
		return Result{}
	}
	cased, ok := applyCase(word, cands[0])
	if !ok {
		return Result{}
	}

	plan := &Plan{
		Backspaces: len(word) + 1, // the word plus the separator
		Type:       cased + string(sep),
	}
	var undo undoState
	if c.opts.Undo {
		undo = undoState{active: true, typed: typed, outRunes: utf8.RuneCountInString(cased)}
	}
	if c.shift || c.altgr {
		// Shift/AltGr is physically held (shifted separator, or a fast
		// typist still holding AltGr): compositors merge modifier state
		// across keyboards, so typing now would garble the output. Defer
		// until the modifier is released.
		c.pending = plan
		c.pendingUndo = undo
		return Result{}
	}
	c.undo = undo
	return Result{Plan: plan}
}

// applyCase maps the typed word's case pattern onto the lowercase
// candidate. Unusual mixed-case input yields ok=false (skip correction).
func applyCase(typed []rune, cand string) (string, bool) {
	upper, lower := 0, 0
	firstUpper := false
	for i, r := range typed {
		if unicode.IsUpper(r) {
			upper++
			if i == 0 {
				firstUpper = true
			}
		} else {
			lower++
		}
	}
	switch {
	case upper == 0:
		return cand, true
	case firstUpper && upper == 1:
		r, size := utf8.DecodeRuneInString(cand)
		return string(unicode.ToUpper(r)) + cand[size:], true
	case lower == 0 && len(typed) >= 2:
		return strings.ToUpper(cand), true
	default:
		return "", false
	}
}
