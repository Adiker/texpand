package main

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bendahl/uinput"
	evdev "github.com/holoplot/go-evdev"

	"github.com/andresousadotpt/texpand/internal/inputstate"
	"github.com/andresousadotpt/texpand/internal/keymap"
	"github.com/andresousadotpt/texpand/internal/output"
)

type pendingExpansion struct {
	match           Match
	extraBackspaces int
}

// Expander maintains a rolling keystroke buffer and triggers text
// expansion when a match is detected.
type Expander struct {
	config    *Config
	vkbd      output.Keyboard
	writer    *output.Writer
	buf       string
	modifiers inputstate.Modifiers
	pending   *pendingExpansion
	maxLen    int
}

// NewExpander creates an Expander with the given config and virtual keyboard.
// Replacement text goes through the output writer (uinput with wtype and
// clipboard fallbacks, matching the pre-refactor behaviour).
func NewExpander(cfg *Config, vkbd output.Keyboard, capsLock func() bool) *Expander {
	maxLen := 0
	for _, m := range cfg.Matches {
		if len(m.Trigger) > maxLen {
			maxLen = len(m.Trigger)
		}
	}
	backends := []output.Backend{&output.Uinput{Kbd: vkbd, CapsLock: capsLock}}
	wt := &output.Wtype{}
	if wt.Available() {
		backends = append(backends, wt)
	}
	backends = append(backends, &output.Clipboard{Kbd: vkbd, Report: reportOutputError})
	writer := &output.Writer{Kbd: vkbd, Backends: backends, Debug: dbg, CapsLock: capsLock}
	return &Expander{config: cfg, vkbd: vkbd, writer: writer, maxLen: maxLen}
}

// Reload swaps the config and recalculates maxLen. Typing session state
// (buf, shift) is preserved so in-progress typing is not disrupted.
func (e *Expander) Reload(cfg *Config) {
	e.config = cfg
	e.maxLen = 0
	for _, m := range cfg.Matches {
		if len(m.Trigger) > e.maxLen {
			e.maxLen = len(m.Trigger)
		}
	}
	if len(e.buf) > e.maxLen {
		e.buf = e.buf[len(e.buf)-e.maxLen:]
	}
}

// ResetInputState clears transient keyboard state after a physical keyboard
// disconnects or reconnects.
func (e *Expander) ResetInputState() {
	e.buf = ""
	e.pending = nil
	e.modifiers = inputstate.Modifiers{Caps: e.modifiers.Caps}
}

func (e *Expander) expandOrDefer(m Match, extraBackspaces int) bool {
	if e.modifiers.Shift || e.modifiers.AltGr {
		e.pending = &pendingExpansion{match: m, extraBackspaces: extraBackspaces}
		return false
	}
	e.performExpansion(m, extraBackspaces)
	return true
}

func (e *Expander) maybeReleasePending() bool {
	if e.pending == nil || e.modifiers.Shift || e.modifiers.AltGr {
		return false
	}
	pending := e.pending
	e.pending = nil
	e.performExpansion(pending.match, pending.extraBackspaces)
	return true
}

// performExpansion handles the full expansion sequence: backspace the
// trigger, type/paste the replacement, and position the cursor.
// extraBackspaces is 1 in space mode (to delete the trailing space) and 0 in immediate mode.
func (e *Expander) performExpansion(m Match, extraBackspaces int) {
	replacement := e.resolveReplacement(m)

	// Handle $|$ cursor marker
	cursorOffset := 0
	if idx := strings.Index(replacement, "$|$"); idx != -1 {
		after := replacement[idx+3:]
		cursorOffset = utf8.RuneCountInString(after)
		replacement = replacement[:idx] + after
	}

	backspaces := utf8.RuneCountInString(m.Trigger) + extraBackspaces
	restore := m.Trigger + strings.Repeat(" ", extraBackspaces)
	if err := e.writer.Apply(output.Edit{Backspaces: backspaces, Text: replacement, Restore: restore}); err != nil {
		dbg("expansion output failed: %v", err)
		return
	}

	// Move cursor back if $|$ was present
	if cursorOffset > 0 {
		for i := 0; i < cursorOffset; i++ {
			if err := e.vkbd.KeyPress(uinput.KeyLeft); err != nil {
				dbg("cursor positioning failed: %v", err)
				return
			}
		}
	}
}

// HandleEvent processes a single key event, manages the rolling buffer, and
// fires expansions. It returns true when an expansion was performed.
func (e *Expander) HandleEvent(ev KeyEvent) bool {
	e.modifiers = ev.Modifiers

	// Modifier events update shared state before either text subsystem sees
	// them. Output-altering modifiers may release a deferred expansion.
	switch ev.Code {
	case evdev.KEY_LEFTSHIFT, evdev.KEY_RIGHTSHIFT:
		return e.maybeReleasePending()
	case evdev.KEY_RIGHTALT:
		if ev.Value == 1 && e.pending == nil {
			e.buf = ""
		}
		return e.maybeReleasePending()
	case evdev.KEY_LEFTCTRL, evdev.KEY_RIGHTCTRL,
		evdev.KEY_LEFTALT, evdev.KEY_LEFTMETA, evdev.KEY_RIGHTMETA:
		if ev.Value == 1 {
			e.buf = ""
			e.pending = nil
		}
		return false
	}

	// Only process key-down events
	if ev.Value != 1 {
		return false
	}
	if e.pending != nil {
		e.pending = nil
	}
	if e.modifiers.Ctrl || e.modifiers.Alt || e.modifiers.AltGr || e.modifiers.Meta {
		e.buf = ""
		return false
	}

	// Buffer reset keys
	if keymap.BufferResetKeys[ev.Code] {
		e.buf = ""
		return false
	}

	// Backspace: remove last rune from buffer
	if ev.Code == evdev.KEY_BACKSPACE {
		if len(e.buf) > 0 {
			_, size := utf8.DecodeLastRuneInString(e.buf)
			e.buf = e.buf[:len(e.buf)-size]
		}
		return false
	}

	// In "space" mode: check matches on space, then clear buffer
	if e.config.TriggerMode != "immediate" && ev.Code == evdev.KEY_SPACE {
		dbgUnsafe("space pressed, buffer=%q, checking matches", e.buf)
		for _, m := range e.config.Matches {
			if !strings.HasSuffix(e.buf, m.Trigger) {
				continue
			}
			dbg("match: trigger=%q → expanding", m.Trigger)
			e.buf = ""
			return e.expandOrDefer(m, 1) // +1 for the space
		}
		e.buf = ""
		return false
	}

	// Map keycode to character
	kc, ok := keymap.Chars[ev.Code]
	if !ok {
		return false
	}

	useShift := e.modifiers.Shift
	if len(kc.Normal) == 1 && kc.Normal[0] >= 'a' && kc.Normal[0] <= 'z' && e.modifiers.Caps {
		useShift = !useShift
	}
	ch := kc.Normal
	if useShift {
		ch = kc.Shifted
	}

	e.buf += ch
	if len(e.buf) > e.maxLen {
		e.buf = e.buf[len(e.buf)-e.maxLen:]
	}

	// In "immediate" mode: check matches after every keystroke
	if e.config.TriggerMode == "immediate" {
		dbgUnsafe("key %q, buffer=%q, checking matches", ch, e.buf)
		for _, m := range e.config.Matches {
			if !strings.HasSuffix(e.buf, m.Trigger) {
				continue
			}
			dbg("match: trigger=%q → expanding", m.Trigger)
			e.buf = ""
			return e.expandOrDefer(m, 0)
		}
	}
	return false
}

// resolveReplacement computes the final replacement text for a match,
// resolving date variables and {{ref}} placeholders.
func (e *Expander) resolveReplacement(m Match) string {
	now := time.Now()
	vars := ResolveVars(m.GlobalVars, m.Vars, now)
	return expandRefs(m.Replace, vars)
}
