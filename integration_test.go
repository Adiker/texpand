package main

import (
	"strings"
	"testing"

	"github.com/bendahl/uinput"
	evdev "github.com/holoplot/go-evdev"

	"github.com/andresousadotpt/texpand/internal/appfilter"
	"github.com/andresousadotpt/texpand/internal/correct"
	"github.com/andresousadotpt/texpand/internal/keymap"
	"github.com/andresousadotpt/texpand/internal/output"
)

// screen simulates the focused application: it receives both the user's
// physical keystrokes and the daemon's virtual-keyboard output, applying
// the Polish Programmer layout. It implements output.Keyboard.
type screen struct {
	text  []rune
	shift bool
	altgr bool
}

func (s *screen) KeyDown(k int) error {
	switch k {
	case uinput.KeyLeftshift, uinput.KeyRightshift:
		s.shift = true
	case uinput.KeyRightalt:
		s.altgr = true
	default:
		return s.KeyPress(k)
	}
	return nil
}

func (s *screen) KeyUp(k int) error {
	switch k {
	case uinput.KeyLeftshift, uinput.KeyRightshift:
		s.shift = false
	case uinput.KeyRightalt:
		s.altgr = false
	}
	return nil
}

func (s *screen) KeyPress(k int) error {
	code := evdev.EvCode(k)
	switch code {
	case evdev.KEY_BACKSPACE:
		if len(s.text) > 0 {
			s.text = s.text[:len(s.text)-1]
		}
		return nil
	case evdev.KEY_ENTER, evdev.KEY_KPENTER:
		s.text = append(s.text, '\n')
		return nil
	case evdev.KEY_TAB:
		s.text = append(s.text, '\t')
		return nil
	}
	if s.altgr {
		if r, ok := keymap.AltGr[code]; ok {
			if s.shift {
				r = []rune(strings.ToUpper(string(r)))[0]
			}
			s.text = append(s.text, r)
		}
		return nil
	}
	if kc, ok := keymap.Chars[code]; ok {
		ch := kc.Normal
		if s.shift {
			ch = kc.Shifted
		}
		s.text = append(s.text, []rune(ch)...)
	}
	return nil
}

func (s *screen) String() string { return string(s.text) }

// rig wires a corrector and writer to a shared simulated screen — the full
// pipeline minus evdev/uinput device I/O.
type rig struct {
	t         *testing.T
	scr       *screen
	corrector *correct.Corrector
	writer    *output.Writer
}

type rigLookup struct{}

func (rigLookup) IsWord(w string) bool {
	return map[string]bool{"pisze": true, "laska": true, "kot": true, "ma": true, "ala": true}[w]
}

func (rigLookup) Candidates(k string, buf []string) []string {
	m := map[string][]string{
		"zolw":    {"żółw"},
		"zrodlo":  {"źródło"},
		"wlasnie": {"właśnie"},
		"pisze":   {"piszę"},
		"laska":   {"łaska"},
		"zle":     {"złe", "źle"},
	}
	return append(buf, m[k]...)
}

func newRig(t *testing.T, opts correct.Options) *rig {
	scr := &screen{}
	c := correct.New(opts)
	c.SetLookup(rigLookup{})
	w := &output.Writer{Kbd: scr, Backends: []output.Backend{&output.Uinput{Kbd: scr}}}
	return &rig{t: t, scr: scr, corrector: c, writer: w}
}

// event feeds one raw event through the pipeline: the "app" (screen) sees
// the physical key first (the daemon never delays real input), then the
// corrector reacts, possibly rewriting the screen through the writer.
func (r *rig) event(code evdev.EvCode, value int32) {
	if value >= 1 {
		r.scr.KeyPress(int(code))
	}
	switch code {
	case evdev.KEY_LEFTSHIFT, evdev.KEY_RIGHTSHIFT:
		if value > 0 {
			r.scr.KeyDown(uinput.KeyLeftshift)
		} else {
			r.scr.KeyUp(uinput.KeyLeftshift)
		}
	case evdev.KEY_RIGHTALT:
		if value > 0 {
			r.scr.KeyDown(uinput.KeyRightalt)
		} else {
			r.scr.KeyUp(uinput.KeyRightalt)
		}
	}
	res := r.corrector.HandleEvent(correct.KeyEvent{Code: code, Value: value})
	if res.Plan != nil {
		edit := output.Edit{Backspaces: res.Plan.Backspaces, Text: res.Plan.Type, Restore: res.Plan.Restore}
		if err := r.writer.Apply(edit); err != nil {
			r.t.Fatalf("writer: %v", err)
		}
	}
}

func (r *rig) key(code evdev.EvCode) {
	r.event(code, 1)
	r.event(code, 0)
}

func (r *rig) typeString(s string) {
	for _, ru := range s {
		rk, ok := keymap.Reverse[ru]
		if !ok {
			r.t.Fatalf("no key for %q", ru)
		}
		if rk.Shift {
			r.event(evdev.KEY_LEFTSHIFT, 1)
		}
		if rk.AltGr {
			r.event(evdev.KEY_RIGHTALT, 1)
		}
		r.key(evdev.EvCode(rk.Code))
		if rk.AltGr {
			r.event(evdev.KEY_RIGHTALT, 0)
		}
		if rk.Shift {
			r.event(evdev.KEY_LEFTSHIFT, 0)
		}
	}
}

func (r *rig) expect(want string) {
	r.t.Helper()
	if got := r.scr.String(); got != want {
		r.t.Fatalf("screen = %q, want %q", got, want)
	}
}

func TestEndToEndCorrection(t *testing.T) {
	cases := []struct{ typed, want string }{
		{"zolw ", "żółw "},
		{"zrodlo,", "źródło,"},
		{"pisze ", "pisze "}, // ambiguous/valid: unchanged
		{"laska ", "laska "}, // ambiguous/valid: unchanged
		{"zle ", "zle "},     // two candidates: unchanged
		{"ZOLW!", "ŻÓŁW!"},   // exclamation mark, uppercase
		{"Zolw.", "Żółw."},   // sentence case + period
		{"WLASNIE!", "WŁAŚNIE!"},
		{"ala ma zolw ", "ala ma żółw "},
		{"(zolw)", "(żółw)"},
	}
	for _, c := range cases {
		r := newRig(t, correct.DefaultOptions())
		r.typeString(c.typed)
		r.expect(c.want)
	}
}

func TestEndToEndUndo(t *testing.T) {
	r := newRig(t, correct.DefaultOptions())
	r.typeString("zolw ")
	r.expect("żółw ")
	r.key(evdev.KEY_BACKSPACE)
	r.expect("zolw")
	// Continuing with a space must not re-correct.
	r.typeString(" ")
	r.expect("zolw ")
}

func TestEndToEndCtrlShortcut(t *testing.T) {
	r := newRig(t, correct.DefaultOptions())
	r.typeString("zolw")
	r.event(evdev.KEY_LEFTCTRL, 1)
	r.corrector.HandleEvent(correct.KeyEvent{Code: evdev.KEY_C, Value: 1}) // Ctrl+C: app gets no char
	r.corrector.HandleEvent(correct.KeyEvent{Code: evdev.KEY_C, Value: 0})
	r.event(evdev.KEY_LEFTCTRL, 0)
	r.typeString(" ")
	r.expect("zolw ") // buffer invalidated → untouched
}

func TestEndToEndTerminalExclusion(t *testing.T) {
	tracker := &appfilter.Tracker{}
	excluder := appfilter.NewExcluder(tracker, appfilter.DefaultExcludedApps, true)
	opts := correct.DefaultOptions()
	opts.ShouldCorrect = excluder.ShouldCorrect

	tracker.Set("org.kde.konsole")
	r := newRig(t, opts)
	r.typeString("zolw ")
	r.expect("zolw ")

	tracker.Set("org.mozilla.firefox")
	r = newRig(t, opts)
	r.typeString("zolw ")
	r.expect("żółw ")

	// Glob pattern: any JetBrains IDE.
	tracker.Set("jetbrains-idea")
	r = newRig(t, opts)
	r.typeString("zolw ")
	r.expect("zolw ")
}

func TestEndToEndEnterDoesNotCorrectByDefault(t *testing.T) {
	r := newRig(t, correct.DefaultOptions())
	r.typeString("zolw")
	r.key(evdev.KEY_ENTER)
	r.expect("zolw\n")
}

// --- existing text expansion still works through the new output path ---

func expanderConfig(mode string) *Config {
	return &Config{
		TriggerMode: mode,
		Matches: []Match{
			{Trigger: ":mail", Replace: "user@example.com"},
			{Trigger: ":sig", Replace: "Cheers$|$!"},
		},
	}
}

// expanderRig drives the Expander against the simulated screen.
type expanderRig struct {
	t   *testing.T
	scr *screen
	e   *Expander
}

func newExpanderRig(t *testing.T, mode string) *expanderRig {
	scr := &screen{}
	e := NewExpander(expanderConfig(mode), scr)
	// Restrict output to uinput so the test never shells out to wtype.
	e.writer = &output.Writer{Kbd: scr, Backends: []output.Backend{&output.Uinput{Kbd: scr}}}
	return &expanderRig{t: t, scr: scr, e: e}
}

func (r *expanderRig) typeString(s string) {
	for _, ru := range s {
		rk, ok := keymap.Reverse[ru]
		if !ok {
			r.t.Fatalf("no key for %q", ru)
		}
		if rk.Shift {
			r.e.HandleEvent(KeyEvent{Code: evdev.KEY_LEFTSHIFT, Value: 1})
			r.scr.KeyDown(uinput.KeyLeftshift)
		}
		r.scr.KeyPress(rk.Code)
		r.e.HandleEvent(KeyEvent{Code: evdev.EvCode(rk.Code), Value: 1})
		r.e.HandleEvent(KeyEvent{Code: evdev.EvCode(rk.Code), Value: 0})
		if rk.Shift {
			r.e.HandleEvent(KeyEvent{Code: evdev.KEY_LEFTSHIFT, Value: 0})
			r.scr.KeyUp(uinput.KeyLeftshift)
		}
	}
}

func TestExpanderStillWorksSpaceMode(t *testing.T) {
	r := newExpanderRig(t, "space")
	r.typeString(":mail ")
	if got := r.scr.String(); got != "user@example.com" {
		t.Fatalf("screen = %q", got)
	}
}

func TestExpanderStillWorksImmediateMode(t *testing.T) {
	r := newExpanderRig(t, "immediate")
	r.typeString(":mail")
	if got := r.scr.String(); got != "user@example.com" {
		t.Fatalf("screen = %q", got)
	}
}

func TestOwnVirtualKeyboardNeverMonitored(t *testing.T) {
	caps := []evdev.EvCode{evdev.KEY_A, evdev.KEY_ENTER}
	if isMonitorableKeyboard(VirtualKeyboardName, caps) {
		t.Fatal("texpand's own uinput device would be monitored (feedback loop)")
	}
	if !isMonitorableKeyboard("AT Translated Set 2 keyboard", caps) {
		t.Fatal("real keyboard rejected")
	}
	if isMonitorableKeyboard("Some Mouse", []evdev.EvCode{evdev.BTN_LEFT}) {
		t.Fatal("mouse accepted as keyboard")
	}
}
