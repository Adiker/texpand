// Package output types corrected text into the focused application.
//
// The primary backend produces Polish diacritics directly through uinput
// AltGr key combinations (Polish Programmer layout) — no subprocesses, no
// clipboard. wtype and clipboard-paste exist as explicit fallbacks; the
// clipboard backend is disabled unless the user opts in.
//
// Writers are only ever invoked from the event-loop goroutine during an
// actual correction, which serializes replacements by construction.
package output

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bendahl/uinput"

	"github.com/andresousadotpt/texpand/internal/keymap"
)

// ErrUnmappable reports that a backend cannot produce the requested text
// and the next backend in the chain should be tried.
var ErrUnmappable = errors.New("text not typeable by this backend")

// ErrOutputMayBePartial marks an operational backend failure that happened
// after output may already have reached the focused application.
var ErrOutputMayBePartial = errors.New("output may be partial")

// Keyboard is the subset of the uinput virtual keyboard the writer needs.
// (bendahl/uinput's Keyboard satisfies it.)
type Keyboard interface {
	KeyDown(key int) error
	KeyUp(key int) error
	KeyPress(key int) error
}

// keyStroke splits a press into down/up operations so callers know whether
// the key-down (the operation visible to applications) was emitted. A failed
// release is retried once to avoid leaving a modifier or Backspace held.
func keyStroke(kbd Keyboard, key int) (emitted bool, err error) {
	if err := kbd.KeyDown(key); err != nil {
		return false, err
	}
	if err := kbd.KeyUp(key); err != nil {
		if retryErr := kbd.KeyUp(key); retryErr != nil {
			return true, errors.Join(err, fmt.Errorf("release retry: %w", retryErr))
		}
	}
	return true, nil
}

// Backend produces text in the focused application.
type Backend interface {
	Name() string
	Validate(text string) error
	Type(text string) error
}

// Edit describes one replacement at the cursor. Restore must be the exact
// text covered by Backspaces so it can be put back after a safe failure.
type Edit struct {
	Backspaces int
	Text       string
	Restore    string
}

// Uinput types text through virtual key events, using AltGr combinations
// for Polish diacritics. Fails with ErrUnmappable (before emitting
// anything) if any rune has no key sequence.
type Uinput struct {
	Kbd Keyboard
}

func (u *Uinput) Name() string { return "uinput" }

func (u *Uinput) Validate(text string) error {
	for _, r := range text {
		if _, ok := keymap.Reverse[r]; !ok {
			return fmt.Errorf("rune %q: %w", r, ErrUnmappable)
		}
	}
	return nil
}

func (u *Uinput) Type(text string) error {
	if err := u.Validate(text); err != nil {
		return err
	}
	emitted := 0
	fail := func(err error) error {
		if emitted > 0 {
			return fmt.Errorf("%w: %v", ErrOutputMayBePartial, err)
		}
		return err
	}
	for _, r := range text {
		rk := keymap.Reverse[r]
		if rk.Shift {
			if err := u.Kbd.KeyDown(uinput.KeyLeftshift); err != nil {
				return fail(fmt.Errorf("shift down: %w", err))
			}
		}
		if rk.AltGr {
			if err := u.Kbd.KeyDown(uinput.KeyRightalt); err != nil {
				if rk.Shift {
					_ = u.Kbd.KeyUp(uinput.KeyLeftshift)
				}
				return fail(fmt.Errorf("altgr down: %w", err))
			}
		}
		keyEmitted, err := keyStroke(u.Kbd, rk.Code)
		if keyEmitted {
			emitted++
		}
		if err != nil {
			if rk.AltGr {
				_ = u.Kbd.KeyUp(uinput.KeyRightalt)
			}
			if rk.Shift {
				_ = u.Kbd.KeyUp(uinput.KeyLeftshift)
			}
			return fail(fmt.Errorf("key %d: %w", rk.Code, err))
		}
		if rk.AltGr {
			if err := u.Kbd.KeyUp(uinput.KeyRightalt); err != nil {
				if rk.Shift {
					_ = u.Kbd.KeyUp(uinput.KeyLeftshift)
				}
				return fmt.Errorf("%w: altgr up: %v", ErrOutputMayBePartial, err)
			}
		}
		if rk.Shift {
			if err := u.Kbd.KeyUp(uinput.KeyLeftshift); err != nil {
				return fmt.Errorf("%w: shift up: %v", ErrOutputMayBePartial, err)
			}
		}
	}
	return nil
}

// Wtype types text via the wtype Wayland tool. Used only during a
// correction, never per keystroke.
type Wtype struct {
	broken bool
}

func (w *Wtype) Name() string { return "wtype" }

// Available reports whether wtype exists on PATH.
func (w *Wtype) Available() bool {
	_, err := exec.LookPath("wtype")
	return err == nil
}

func (w *Wtype) Validate(string) error {
	if w.broken {
		return fmt.Errorf("wtype previously failed: %w", ErrUnmappable)
	}
	if !w.Available() {
		return fmt.Errorf("wtype not found: %w", ErrUnmappable)
	}
	return nil
}

func (w *Wtype) Type(text string) error {
	if err := w.Validate(text); err != nil {
		return err
	}
	out, err := exec.Command("wtype", "--", text).CombinedOutput()
	if err != nil {
		w.broken = true
		return fmt.Errorf("wtype: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Clipboard pastes text via wl-copy + Ctrl+V, restoring the previous
// clipboard afterwards. Off by default: it mutates the clipboard and can
// misbehave in password fields and remote sessions.
type Clipboard struct {
	Kbd    Keyboard
	Report func(error)
}

func (c *Clipboard) Name() string { return "clipboard" }

func (c *Clipboard) Validate(string) error {
	for _, command := range []string{"wl-copy", "wl-paste"} {
		if _, err := exec.LookPath(command); err != nil {
			return fmt.Errorf("%s not found: %w", command, ErrUnmappable)
		}
	}
	return nil
}

type clipboardSnapshot struct {
	text  []byte
	empty bool
}

// clipboardState coalesces overlapping paste operations. A second paste
// before the first restore reuses the original snapshot instead of treating
// texpand's temporary value as the user's clipboard.
var clipboardState struct {
	sync.Mutex
	generation uint64
	active     bool
	original   clipboardSnapshot
}

func readClipboard() (clipboardSnapshot, error) {
	out, err := exec.Command("wl-paste", "-n").Output()
	if err == nil {
		return clipboardSnapshot{text: out}, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return clipboardSnapshot{empty: true}, nil
	}
	return clipboardSnapshot{}, fmt.Errorf("wl-paste: %w", err)
}

func restoreClipboard(s clipboardSnapshot) error {
	if s.empty {
		return exec.Command("wl-copy", "--clear").Run()
	}
	return exec.Command("wl-copy", "--", string(s.text)).Run()
}

func beginClipboardPaste() (uint64, error) {
	clipboardState.Lock()
	defer clipboardState.Unlock()
	if !clipboardState.active {
		snapshot, err := readClipboard()
		if err != nil {
			return 0, err
		}
		clipboardState.original = snapshot
		clipboardState.active = true
	}
	clipboardState.generation++
	return clipboardState.generation, nil
}

func scheduleClipboardRestore(generation uint64, report func(error)) {
	go func() {
		time.Sleep(200 * time.Millisecond)
		clipboardState.Lock()
		defer clipboardState.Unlock()
		if !clipboardState.active || clipboardState.generation != generation {
			return
		}
		if err := restoreClipboard(clipboardState.original); err != nil && report != nil {
			report(fmt.Errorf("clipboard restore: %w", err))
		}
		clipboardState.active = false
	}()
}

func abortClipboardPaste(generation uint64) error {
	clipboardState.Lock()
	defer clipboardState.Unlock()
	if !clipboardState.active || clipboardState.generation != generation {
		return nil
	}
	err := restoreClipboard(clipboardState.original)
	clipboardState.active = false
	return err
}

func (c *Clipboard) Type(text string) error {
	if err := c.Validate(text); err != nil {
		return err
	}
	generation, err := beginClipboardPaste()
	if err != nil {
		return err
	}
	if err := exec.Command("wl-copy", "--", text).Run(); err != nil {
		restoreErr := abortClipboardPaste(generation)
		return errors.Join(fmt.Errorf("wl-copy: %w", err), restoreErr)
	}
	if err := c.Kbd.KeyDown(uinput.KeyLeftctrl); err != nil {
		restoreErr := abortClipboardPaste(generation)
		return errors.Join(fmt.Errorf("ctrl down: %w", err), restoreErr)
	}
	time.Sleep(5 * time.Millisecond)
	if err := c.Kbd.KeyPress(uinput.KeyV); err != nil {
		_ = c.Kbd.KeyUp(uinput.KeyLeftctrl)
		scheduleClipboardRestore(generation, c.Report)
		return fmt.Errorf("%w: paste key: %v", ErrOutputMayBePartial, err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := c.Kbd.KeyUp(uinput.KeyLeftctrl); err != nil {
		scheduleClipboardRestore(generation, c.Report)
		return fmt.Errorf("%w: ctrl up: %v", ErrOutputMayBePartial, err)
	}
	time.Sleep(20 * time.Millisecond)
	scheduleClipboardRestore(generation, c.Report)
	return nil
}

// Writer executes correction plans: backspaces through the virtual
// keyboard, text through the first backend that can produce it.
type Writer struct {
	Kbd      Keyboard
	Backends []Backend
	Debug    func(format string, args ...any)
}

// Apply validates and selects a backend before deleting anything, then
// executes the edit. Safe failures restore the deleted suffix through uinput.
func (w *Writer) Apply(edit Edit) error {
	if edit.Backspaces < 0 {
		return fmt.Errorf("negative backspace count %d", edit.Backspaces)
	}
	restoreRunes := utf8.RuneCountInString(edit.Restore)
	if restoreRunes != edit.Backspaces {
		return fmt.Errorf("restore text has %d runes, want %d", restoreRunes, edit.Backspaces)
	}

	var selected Backend
	var lastErr error
	for _, b := range w.Backends {
		err := b.Validate(edit.Text)
		if err == nil {
			selected = b
			break
		}
		lastErr = err
		if w.Debug != nil {
			w.Debug("output backend %s rejected edit: %v", b.Name(), err)
		}
	}
	if selected == nil {
		if lastErr == nil {
			lastErr = errors.New("no output backend configured")
		}
		return lastErr
	}

	deleted := 0
	for deleted < edit.Backspaces {
		emitted, err := keyStroke(w.Kbd, uinput.KeyBackspace)
		if emitted {
			deleted++
		}
		if err != nil {
			if emitted {
				return fmt.Errorf("%w: backspace %d/%d: %v", ErrOutputMayBePartial, deleted, edit.Backspaces, err)
			}
			restore := lastRunes(edit.Restore, deleted)
			return w.withRestore(fmt.Errorf("backspace %d/%d: %w", deleted+1, edit.Backspaces, err), restore)
		}
	}
	if err := selected.Type(edit.Text); err != nil {
		if errors.Is(err, ErrOutputMayBePartial) {
			return fmt.Errorf("output backend %s: %w", selected.Name(), err)
		}
		return w.withRestore(fmt.Errorf("output backend %s: %w", selected.Name(), err), edit.Restore)
	}
	return nil
}

func lastRunes(s string, n int) string {
	runes := []rune(s)
	if n <= 0 {
		return ""
	}
	if n >= len(runes) {
		return s
	}
	return string(runes[len(runes)-n:])
}

func (w *Writer) withRestore(operationErr error, restore string) error {
	if restore == "" {
		return operationErr
	}
	recovery := &Uinput{Kbd: w.Kbd}
	if err := recovery.Type(restore); err != nil {
		return errors.Join(operationErr, fmt.Errorf("restore %q: %w", restore, err))
	}
	return fmt.Errorf("%w (deleted text restored)", operationErr)
}
