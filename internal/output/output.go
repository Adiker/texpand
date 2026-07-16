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
	"time"

	"github.com/bendahl/uinput"

	"github.com/andresousadotpt/texpand/internal/keymap"
)

// ErrUnmappable reports that a backend cannot produce the requested text
// and the next backend in the chain should be tried.
var ErrUnmappable = errors.New("text not typeable by this backend")

// Keyboard is the subset of the uinput virtual keyboard the writer needs.
// (bendahl/uinput's Keyboard satisfies it.)
type Keyboard interface {
	KeyDown(key int) error
	KeyUp(key int) error
	KeyPress(key int) error
}

// Backend produces text in the focused application.
type Backend interface {
	Name() string
	Type(text string) error
}

// Uinput types text through virtual key events, using AltGr combinations
// for Polish diacritics. Fails with ErrUnmappable (before emitting
// anything) if any rune has no key sequence.
type Uinput struct {
	Kbd Keyboard
}

func (u *Uinput) Name() string { return "uinput" }

func (u *Uinput) Type(text string) error {
	// Validate first so a fallback backend never receives a half-typed
	// replacement.
	for _, r := range text {
		if _, ok := keymap.Reverse[r]; !ok {
			return fmt.Errorf("rune %q: %w", r, ErrUnmappable)
		}
	}
	for _, r := range text {
		rk := keymap.Reverse[r]
		if rk.Shift {
			u.Kbd.KeyDown(uinput.KeyLeftshift)
		}
		if rk.AltGr {
			u.Kbd.KeyDown(uinput.KeyRightalt)
		}
		u.Kbd.KeyPress(rk.Code)
		if rk.AltGr {
			u.Kbd.KeyUp(uinput.KeyRightalt)
		}
		if rk.Shift {
			u.Kbd.KeyUp(uinput.KeyLeftshift)
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

func (w *Wtype) Type(text string) error {
	if w.broken {
		return fmt.Errorf("wtype previously failed: %w", ErrUnmappable)
	}
	out, err := exec.Command("wtype", "--", text).CombinedOutput()
	if err != nil {
		// Mark broken and let the chain fall through (the pre-existing
		// expander behaviour: a failing wtype falls back to clipboard).
		w.broken = true
		return fmt.Errorf("wtype: %v: %s: %w", err, strings.TrimSpace(string(out)), ErrUnmappable)
	}
	return nil
}

// Clipboard pastes text via wl-copy + Ctrl+V, restoring the previous
// clipboard afterwards. Off by default: it mutates the clipboard and can
// misbehave in password fields and remote sessions.
type Clipboard struct {
	Kbd Keyboard
}

func (c *Clipboard) Name() string { return "clipboard" }

func (c *Clipboard) Type(text string) error {
	oldClip, _ := exec.Command("wl-paste", "-n").Output()
	if err := exec.Command("wl-copy", "--", text).Run(); err != nil {
		return fmt.Errorf("wl-copy: %w", err)
	}
	c.Kbd.KeyDown(uinput.KeyLeftctrl)
	time.Sleep(5 * time.Millisecond)
	c.Kbd.KeyPress(uinput.KeyV)
	time.Sleep(5 * time.Millisecond)
	c.Kbd.KeyUp(uinput.KeyLeftctrl)
	time.Sleep(20 * time.Millisecond)
	if len(oldClip) > 0 {
		go func() {
			time.Sleep(200 * time.Millisecond)
			exec.Command("wl-copy", "--", string(oldClip)).Run()
		}()
	}
	return nil
}

// Writer executes correction plans: backspaces through the virtual
// keyboard, text through the first backend that can produce it.
type Writer struct {
	Kbd      Keyboard
	Backends []Backend
	Debug    func(format string, args ...any)
}

// Apply deletes backspaces characters and types text.
func (w *Writer) Apply(backspaces int, text string) error {
	for i := 0; i < backspaces; i++ {
		w.Kbd.KeyPress(uinput.KeyBackspace)
	}
	var lastErr error
	for _, b := range w.Backends {
		err := b.Type(text)
		if err == nil {
			return nil
		}
		lastErr = err
		if w.Debug != nil {
			w.Debug("output backend %s: %v", b.Name(), err)
		}
		if !errors.Is(err, ErrUnmappable) {
			// The backend tried and failed; retrying with another could
			// double-type. Stop.
			return err
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no output backend configured")
	}
	return lastErr
}
