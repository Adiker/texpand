// Package keymap holds the keycode/character tables for the US-compatible
// Polish Programmer layout, shared by the expander, the autocorrector and
// the output backends.
package keymap

import (
	"fmt"
	"strings"

	evdev "github.com/holoplot/go-evdev"
)

// KeyChar maps an evdev keycode to its normal and shifted characters.
type KeyChar struct {
	Normal  string
	Shifted string
}

// Chars maps evdev key codes to their character representations for a
// US/International (and Polish Programmer) keyboard layout.
var Chars = map[evdev.EvCode]KeyChar{
	evdev.KEY_A: {"a", "A"}, evdev.KEY_B: {"b", "B"},
	evdev.KEY_C: {"c", "C"}, evdev.KEY_D: {"d", "D"},
	evdev.KEY_E: {"e", "E"}, evdev.KEY_F: {"f", "F"},
	evdev.KEY_G: {"g", "G"}, evdev.KEY_H: {"h", "H"},
	evdev.KEY_I: {"i", "I"}, evdev.KEY_J: {"j", "J"},
	evdev.KEY_K: {"k", "K"}, evdev.KEY_L: {"l", "L"},
	evdev.KEY_M: {"m", "M"}, evdev.KEY_N: {"n", "N"},
	evdev.KEY_O: {"o", "O"}, evdev.KEY_P: {"p", "P"},
	evdev.KEY_Q: {"q", "Q"}, evdev.KEY_R: {"r", "R"},
	evdev.KEY_S: {"s", "S"}, evdev.KEY_T: {"t", "T"},
	evdev.KEY_U: {"u", "U"}, evdev.KEY_V: {"v", "V"},
	evdev.KEY_W: {"w", "W"}, evdev.KEY_X: {"x", "X"},
	evdev.KEY_Y: {"y", "Y"}, evdev.KEY_Z: {"z", "Z"},

	evdev.KEY_1: {"1", "!"}, evdev.KEY_2: {"2", "@"},
	evdev.KEY_3: {"3", "#"}, evdev.KEY_4: {"4", "$"},
	evdev.KEY_5: {"5", "%"}, evdev.KEY_6: {"6", "^"},
	evdev.KEY_7: {"7", "&"}, evdev.KEY_8: {"8", "*"},
	evdev.KEY_9: {"9", "("}, evdev.KEY_0: {"0", ")"},

	evdev.KEY_MINUS:      {"-", "_"},
	evdev.KEY_EQUAL:      {"=", "+"},
	evdev.KEY_LEFTBRACE:  {"[", "{"},
	evdev.KEY_RIGHTBRACE: {"]", "}"},
	evdev.KEY_SEMICOLON:  {";", ":"},
	evdev.KEY_APOSTROPHE: {"'", "\""},
	evdev.KEY_GRAVE:      {"`", "~"},
	evdev.KEY_BACKSLASH:  {"\\", "|"},
	evdev.KEY_COMMA:      {",", "<"},
	evdev.KEY_DOT:        {".", ">"},
	evdev.KEY_SLASH:      {"/", "?"},
	evdev.KEY_SPACE:      {" ", " "},
}

// AltGr maps letter keycodes to the Polish diacritic produced with AltGr
// held on the Polish Programmer layout. Uppercase forms are produced with
// AltGr+Shift.
var AltGr = map[evdev.EvCode]rune{
	evdev.KEY_A: 'ą',
	evdev.KEY_C: 'ć',
	evdev.KEY_E: 'ę',
	evdev.KEY_L: 'ł',
	evdev.KEY_N: 'ń',
	evdev.KEY_O: 'ó',
	evdev.KEY_S: 'ś',
	evdev.KEY_X: 'ź',
	evdev.KEY_Z: 'ż',
}

// BufferResetKeys are keys that clear the expander's typing buffer.
var BufferResetKeys = map[evdev.EvCode]bool{
	evdev.KEY_ENTER:     true,
	evdev.KEY_ESC:       true,
	evdev.KEY_TAB:       true,
	evdev.KEY_UP:        true,
	evdev.KEY_DOWN:      true,
	evdev.KEY_LEFT:      true,
	evdev.KEY_RIGHT:     true,
	evdev.KEY_HOME:      true,
	evdev.KEY_END:       true,
	evdev.KEY_PAGEUP:    true,
	evdev.KEY_PAGEDOWN:  true,
	evdev.KEY_DELETE:    true,
	evdev.KEY_INSERT:    true,
	evdev.KEY_LEFTCTRL:  true,
	evdev.KEY_RIGHTCTRL: true,
	evdev.KEY_LEFTALT:   true,
	evdev.KEY_RIGHTALT:  true,
	evdev.KEY_LEFTMETA:  true,
	evdev.KEY_RIGHTMETA: true,
}

// ReverseKey describes how to type a rune: key code plus the modifiers
// that must be held.
type ReverseKey struct {
	Code  int
	Shift bool
	AltGr bool
}

// Reverse maps runes to the key sequence that produces them on the Polish
// Programmer layout. Built at init from Chars and AltGr.
var Reverse map[rune]ReverseKey

func init() {
	// evdev key codes are numerically identical to uinput key codes.
	Reverse = make(map[rune]ReverseKey, len(Chars)*2+len(AltGr)*2)
	for evCode, kc := range Chars {
		code := int(evCode)
		for _, r := range kc.Normal {
			Reverse[r] = ReverseKey{Code: code}
		}
		for _, r := range kc.Shifted {
			if r != []rune(kc.Normal)[0] { // Normal == Shifted (e.g. space)
				Reverse[r] = ReverseKey{Code: code, Shift: true}
			}
		}
	}
	for evCode, lower := range AltGr {
		code := int(evCode)
		Reverse[lower] = ReverseKey{Code: code, AltGr: true}
		upper := []rune(strings.ToUpper(string(lower)))[0]
		Reverse[upper] = ReverseKey{Code: code, AltGr: true, Shift: true}
	}
	Reverse['\n'] = ReverseKey{Code: int(evdev.KEY_ENTER)}
	Reverse['\t'] = ReverseKey{Code: int(evdev.KEY_TAB)}
}

// Shortcut is a parsed keyboard shortcut: one key plus required modifiers.
type Shortcut struct {
	Ctrl, Alt, Shift, Meta bool
	Code                   evdev.EvCode
}

// IsZero reports whether the shortcut is unset.
func (s Shortcut) IsZero() bool { return s.Code == 0 }

// shortcutKeys names the non-modifier keys accepted in shortcut strings.
var shortcutKeys = map[string]evdev.EvCode{
	"space": evdev.KEY_SPACE, "slash": evdev.KEY_SLASH,
	"backslash": evdev.KEY_BACKSLASH, "semicolon": evdev.KEY_SEMICOLON,
	"apostrophe": evdev.KEY_APOSTROPHE, "comma": evdev.KEY_COMMA,
	"dot": evdev.KEY_DOT, "minus": evdev.KEY_MINUS,
	"equal": evdev.KEY_EQUAL, "grave": evdev.KEY_GRAVE,
	"f1": evdev.KEY_F1, "f2": evdev.KEY_F2, "f3": evdev.KEY_F3,
	"f4": evdev.KEY_F4, "f5": evdev.KEY_F5, "f6": evdev.KEY_F6,
	"f7": evdev.KEY_F7, "f8": evdev.KEY_F8, "f9": evdev.KEY_F9,
	"f10": evdev.KEY_F10, "f11": evdev.KEY_F11, "f12": evdev.KEY_F12,
	"pause": evdev.KEY_PAUSE, "scrolllock": evdev.KEY_SCROLLLOCK,
}

func init() {
	for code, kc := range Chars {
		if len(kc.Normal) == 1 {
			c := kc.Normal[0]
			if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
				shortcutKeys[kc.Normal] = code
			}
		}
	}
}

// ParseShortcut parses strings like "ctrl+alt+slash" or "meta+z". An empty
// string yields the zero Shortcut (disabled).
func ParseShortcut(s string) (Shortcut, error) {
	var sc Shortcut
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" || s == "none" {
		return sc, nil
	}
	parts := strings.Split(s, "+")
	for i, p := range parts {
		p = strings.TrimSpace(p)
		last := i == len(parts)-1
		switch p {
		case "ctrl", "control":
			sc.Ctrl = true
		case "alt":
			sc.Alt = true
		case "shift":
			sc.Shift = true
		case "meta", "super", "win":
			sc.Meta = true
		default:
			if !last {
				return Shortcut{}, fmt.Errorf("unknown modifier %q in shortcut %q", p, s)
			}
			code, ok := shortcutKeys[p]
			if !ok {
				return Shortcut{}, fmt.Errorf("unknown key %q in shortcut %q", p, s)
			}
			sc.Code = code
		}
	}
	if sc.Code == 0 {
		return Shortcut{}, fmt.Errorf("shortcut %q has no non-modifier key", s)
	}
	if !sc.Ctrl && !sc.Alt && !sc.Meta {
		return Shortcut{}, fmt.Errorf("shortcut %q needs at least one of ctrl/alt/meta", s)
	}
	return sc, nil
}
