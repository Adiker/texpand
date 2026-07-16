package output

import (
	"errors"
	"slices"
	"testing"

	"github.com/bendahl/uinput"
	evdev "github.com/holoplot/go-evdev"
)

// fakeKbd records emitted key actions.
type fakeKbd struct {
	log []string
}

func (f *fakeKbd) KeyDown(k int) error {
	f.log = append(f.log, "down:"+keyName(k))
	return nil
}
func (f *fakeKbd) KeyUp(k int) error {
	f.log = append(f.log, "up:"+keyName(k))
	return nil
}
func (f *fakeKbd) KeyPress(k int) error {
	f.log = append(f.log, "press:"+keyName(k))
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
		"down:altgr", "press:z", "up:altgr", // ż
		"down:altgr", "press:o", "up:altgr", // ó
		"down:altgr", "press:l", "up:altgr", // ł
		"press:w",
		"press:space",
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
	want := []string{"down:shift", "down:altgr", "press:x", "up:altgr", "up:shift"}
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
	name  string
	err   error
	typed []string
}

func (s *stubBackend) Name() string { return s.name }
func (s *stubBackend) Type(text string) error {
	if s.err != nil {
		return s.err
	}
	s.typed = append(s.typed, text)
	return nil
}

func TestWriterFallbackChain(t *testing.T) {
	kbd := &fakeKbd{}
	first := &stubBackend{name: "first", err: ErrUnmappable}
	second := &stubBackend{name: "second"}
	w := &Writer{Kbd: kbd, Backends: []Backend{first, second}}
	if err := w.Apply(3, "żółw"); err != nil {
		t.Fatal(err)
	}
	if got := len(kbd.log); got != 3 {
		t.Fatalf("backspaces = %v", kbd.log)
	}
	if !slices.Equal(second.typed, []string{"żółw"}) {
		t.Fatalf("second backend got %v", second.typed)
	}
}

func TestWriterStopsOnHardFailure(t *testing.T) {
	kbd := &fakeKbd{}
	first := &stubBackend{name: "first", err: errors.New("boom")}
	second := &stubBackend{name: "second"}
	w := &Writer{Kbd: kbd, Backends: []Backend{first, second}}
	if err := w.Apply(0, "x"); err == nil {
		t.Fatal("expected error")
	}
	if len(second.typed) != 0 {
		t.Fatal("fallback ran after a non-unmappable failure (risk of double-typing)")
	}
}
