package output

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bendahl/uinput"
	evdev "github.com/holoplot/go-evdev"
)

// fakeKbd records emitted key actions.
type fakeKbd struct {
	log  []string
	fail func(action string) error
}

func (f *fakeKbd) KeyDown(k int) error {
	return f.record("down:" + keyName(k))
}
func (f *fakeKbd) KeyUp(k int) error {
	return f.record("up:" + keyName(k))
}
func (f *fakeKbd) KeyPress(k int) error {
	return f.record("press:" + keyName(k))
}

func (f *fakeKbd) record(action string) error {
	f.log = append(f.log, action)
	if f.fail != nil {
		return f.fail(action)
	}
	return nil
}

func keyName(k int) string {
	switch k {
	case uinput.KeyLeftshift:
		return "shift"
	case uinput.KeyRightalt:
		return "altgr"
	case uinput.KeyBackspace:
		return "bs"
	case int(evdev.KEY_Z):
		return "z"
	case int(evdev.KEY_X):
		return "x"
	case int(evdev.KEY_O):
		return "o"
	case int(evdev.KEY_L):
		return "l"
	case int(evdev.KEY_W):
		return "w"
	case int(evdev.KEY_A):
		return "a"
	case int(evdev.KEY_B):
		return "b"
	case int(evdev.KEY_C):
		return "c"
	case int(evdev.KEY_V):
		return "v"
	case int(evdev.KEY_SPACE):
		return "space"
	default:
		return "?"
	}
}

func TestUinputTypesPolishViaAltGr(t *testing.T) {
	kbd := &fakeKbd{}
	u := &Uinput{Kbd: kbd}
	if err := u.Type("żółw "); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"down:altgr", "down:z", "up:z", "up:altgr", // ż
		"down:altgr", "down:o", "up:o", "up:altgr", // ó
		"down:altgr", "down:l", "up:l", "up:altgr", // ł
		"down:w", "up:w",
		"down:space", "up:space",
	}
	if !slices.Equal(kbd.log, want) {
		t.Fatalf("log = %v\nwant  %v", kbd.log, want)
	}
}

func TestUinputUppercaseDiacritics(t *testing.T) {
	kbd := &fakeKbd{}
	u := &Uinput{Kbd: kbd}
	if err := u.Type("Ź"); err != nil {
		t.Fatal(err)
	}
	want := []string{"down:shift", "down:altgr", "down:x", "up:x", "up:altgr", "up:shift"}
	if !slices.Equal(kbd.log, want) {
		t.Fatalf("log = %v, want %v", kbd.log, want)
	}
}

func TestUinputCompensatesForCapsLock(t *testing.T) {
	kbd := &fakeKbd{}
	u := &Uinput{Kbd: kbd, CapsLock: func() bool { return true }}
	if err := u.Type("aA"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"down:shift", "down:a", "up:a", "up:shift",
		"down:a", "up:a",
	}
	if !slices.Equal(kbd.log, want) {
		t.Fatalf("log = %v, want %v", kbd.log, want)
	}
}

func TestUinputUnmappableEmitsNothing(t *testing.T) {
	kbd := &fakeKbd{}
	u := &Uinput{Kbd: kbd}
	err := u.Type("ż→x") // arrow has no key
	if !errors.Is(err, ErrUnmappable) {
		t.Fatalf("err = %v, want ErrUnmappable", err)
	}
	if len(kbd.log) != 0 {
		t.Fatalf("partial output emitted: %v", kbd.log)
	}
}

type stubBackend struct {
	name        string
	validateErr error
	typeErr     error
	typed       []string
}

func (s *stubBackend) Name() string { return s.name }
func (s *stubBackend) Validate(string) error {
	return s.validateErr
}
func (s *stubBackend) Type(text string) error {
	if s.typeErr != nil {
		return s.typeErr
	}
	s.typed = append(s.typed, text)
	return nil
}

func TestWriterFallbackChain(t *testing.T) {
	kbd := &fakeKbd{}
	first := &stubBackend{name: "first", validateErr: ErrUnmappable}
	second := &stubBackend{name: "second"}
	w := &Writer{Kbd: kbd, Backends: []Backend{first, second}}
	if err := w.Apply(Edit{Backspaces: 3, Text: "żółw", Restore: "abc"}); err != nil {
		t.Fatal(err)
	}
	if got := len(kbd.log); got != 6 {
		t.Fatalf("backspaces = %v", kbd.log)
	}
	if !slices.Equal(second.typed, []string{"żółw"}) {
		t.Fatalf("second backend got %v", second.typed)
	}
}

func TestWriterStopsOnHardFailure(t *testing.T) {
	kbd := &fakeKbd{}
	first := &stubBackend{name: "first", typeErr: errors.New("boom")}
	second := &stubBackend{name: "second"}
	w := &Writer{Kbd: kbd, Backends: []Backend{first, second}}
	if err := w.Apply(Edit{Text: "x"}); err == nil {
		t.Fatal("expected error")
	}
	if len(second.typed) != 0 {
		t.Fatal("fallback ran after a non-unmappable failure (risk of double-typing)")
	}
}

func TestWriterValidatesBeforeBackspacing(t *testing.T) {
	kbd := &fakeKbd{}
	w := &Writer{Kbd: kbd, Backends: []Backend{
		&stubBackend{name: "nope", validateErr: ErrUnmappable},
	}}
	if err := w.Apply(Edit{Backspaces: 3, Text: "→", Restore: "abc"}); err == nil {
		t.Fatal("expected validation error")
	}
	if len(kbd.log) != 0 {
		t.Fatalf("input was deleted before backend validation: %v", kbd.log)
	}
}

func TestWriterRestoresDeletedTextAfterSafeBackendFailure(t *testing.T) {
	kbd := &fakeKbd{}
	w := &Writer{Kbd: kbd, Backends: []Backend{
		&stubBackend{name: "safe-failure", typeErr: errors.New("not started")},
	}}
	err := w.Apply(Edit{Backspaces: 3, Text: "x", Restore: "abc"})
	if err == nil || !strings.Contains(err.Error(), "deleted text restored") {
		t.Fatalf("err = %v, want restored-text error", err)
	}
	want := []string{
		"down:bs", "up:bs", "down:bs", "up:bs", "down:bs", "up:bs",
		"down:a", "up:a", "down:b", "up:b", "down:c", "up:c",
	}
	if !slices.Equal(kbd.log, want) {
		t.Fatalf("log = %v, want %v", kbd.log, want)
	}
}

func TestWriterRestoresOnlySuccessfullyDeletedSuffix(t *testing.T) {
	backspaces := 0
	kbd := &fakeKbd{fail: func(action string) error {
		if action == "down:bs" {
			backspaces++
			if backspaces == 3 {
				return errors.New("device stopped")
			}
		}
		return nil
	}}
	w := &Writer{Kbd: kbd, Backends: []Backend{&stubBackend{name: "ready"}}}
	err := w.Apply(Edit{Backspaces: 3, Text: "x", Restore: "abc"})
	if err == nil || !strings.Contains(err.Error(), "deleted text restored") {
		t.Fatalf("err = %v, want restored-text error", err)
	}
	want := []string{
		"down:bs", "up:bs", "down:bs", "up:bs", "down:bs",
		"down:b", "up:b", "down:c", "up:c",
	}
	if !slices.Equal(kbd.log, want) {
		t.Fatalf("log = %v, want %v", kbd.log, want)
	}
}

func TestWriterDoesNotRestoreOrFallbackAfterPossiblePartialOutput(t *testing.T) {
	kbd := &fakeKbd{}
	first := &stubBackend{name: "partial", typeErr: fmt.Errorf("%w: boom", ErrOutputMayBePartial)}
	second := &stubBackend{name: "second"}
	w := &Writer{Kbd: kbd, Backends: []Backend{first, second}}
	err := w.Apply(Edit{Backspaces: 3, Text: "xyz", Restore: "abc"})
	if !errors.Is(err, ErrOutputMayBePartial) {
		t.Fatalf("err = %v, want ErrOutputMayBePartial", err)
	}
	if len(second.typed) != 0 {
		t.Fatal("fallback ran after possible partial output")
	}
	if got := len(kbd.log); got != 6 {
		t.Fatalf("unexpected recovery output: %v", kbd.log)
	}
}

func installFakeClipboardTools(t *testing.T, initial *string) string {
	t.Helper()
	dir := t.TempDir()
	state := filepath.Join(dir, "clipboard")
	if initial != nil {
		if err := os.WriteFile(state, []byte(*initial), 0600); err != nil {
			t.Fatal(err)
		}
	}
	paste := "#!/bin/sh\nif [ ! -f \"$CLIPBOARD_STATE\" ]; then exit 1; fi\ncat \"$CLIPBOARD_STATE\"\n"
	copy := "#!/bin/sh\nif [ \"$1\" = \"--clear\" ]; then rm -f \"$CLIPBOARD_STATE\"; exit 0; fi\nprintf %s \"$2\" >\"$CLIPBOARD_STATE\"\n"
	for name, body := range map[string]string{"wl-paste": paste, "wl-copy": copy} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CLIPBOARD_STATE", state)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	clipboardState.Lock()
	clipboardState.active = false
	clipboardState.generation = 0
	clipboardState.original = clipboardSnapshot{}
	clipboardState.Unlock()
	return state
}

func TestClipboardRestoresEmptyClipboard(t *testing.T) {
	state := installFakeClipboardTools(t, nil)
	c := &Clipboard{Kbd: &fakeKbd{}}
	if err := c.Type("temporary"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(250 * time.Millisecond)
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("empty clipboard was not restored: %v", err)
	}
}

func TestClipboardOverlappingPastesRestoreOriginal(t *testing.T) {
	original := "user clipboard"
	state := installFakeClipboardTools(t, &original)
	c := &Clipboard{Kbd: &fakeKbd{}}
	if err := c.Type("first"); err != nil {
		t.Fatal(err)
	}
	if err := c.Type("second"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(250 * time.Millisecond)
	got, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("clipboard = %q, want %q", got, original)
	}
}
