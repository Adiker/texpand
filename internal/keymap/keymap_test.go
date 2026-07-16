package keymap

import (
	"testing"

	evdev "github.com/holoplot/go-evdev"
)

func TestReversePolishDiacritics(t *testing.T) {
	cases := []struct {
		r     rune
		code  evdev.EvCode
		shift bool
		altgr bool
	}{
		{'a', evdev.KEY_A, false, false},
		{'A', evdev.KEY_A, true, false},
		{'ą', evdev.KEY_A, false, true},
		{'Ą', evdev.KEY_A, true, true},
		{'ż', evdev.KEY_Z, false, true},
		{'ź', evdev.KEY_X, false, true},
		{'ł', evdev.KEY_L, false, true},
		{'Ó', evdev.KEY_O, true, true},
		{'!', evdev.KEY_1, true, false},
		{' ', evdev.KEY_SPACE, false, false},
		{'\n', evdev.KEY_ENTER, false, false},
	}
	for _, c := range cases {
		rk, ok := Reverse[c.r]
		if !ok {
			t.Errorf("Reverse[%q] missing", c.r)
			continue
		}
		if rk.Code != int(c.code) || rk.Shift != c.shift || rk.AltGr != c.altgr {
			t.Errorf("Reverse[%q] = %+v, want code=%d shift=%v altgr=%v", c.r, rk, c.code, c.shift, c.altgr)
		}
	}
}

func TestAllPolishLettersTypeable(t *testing.T) {
	for _, r := range "ąćęłńóśźżĄĆĘŁŃÓŚŹŻ" {
		if _, ok := Reverse[r]; !ok {
			t.Errorf("no key sequence for %q", r)
		}
	}
}

func TestParseShortcut(t *testing.T) {
	sc, err := ParseShortcut("ctrl+alt+slash")
	if err != nil {
		t.Fatal(err)
	}
	if !sc.Ctrl || !sc.Alt || sc.Shift || sc.Meta || sc.Code != evdev.KEY_SLASH {
		t.Errorf("shortcut = %+v", sc)
	}

	if sc, err := ParseShortcut(""); err != nil || !sc.IsZero() {
		t.Errorf("empty shortcut: %+v, %v", sc, err)
	}
	if sc, err := ParseShortcut("none"); err != nil || !sc.IsZero() {
		t.Errorf("none shortcut: %+v, %v", sc, err)
	}
	if _, err := ParseShortcut("ctrl+alt+nosuchkey"); err == nil {
		t.Error("bogus key accepted")
	}
	if _, err := ParseShortcut("z"); err == nil {
		t.Error("modifier-less shortcut accepted")
	}
	if _, err := ParseShortcut("ctrl+alt"); err == nil {
		t.Error("key-less shortcut accepted")
	}
	if sc, err := ParseShortcut("Meta+F9"); err != nil || !sc.Meta || sc.Code != evdev.KEY_F9 {
		t.Errorf("case-insensitive parse failed: %+v, %v", sc, err)
	}
}
