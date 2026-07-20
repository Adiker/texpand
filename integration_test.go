package main

import (
	"strings"
	"testing"

	"github.com/bendahl/uinput"
	evdev "github.com/holoplot/go-evdev"

	"github.com/andresousadotpt/texpand/internal/appfilter"
	"github.com/andresousadotpt/texpand/internal/correct"
	"github.com/andresousadotpt/texpand/internal/inputstate"
	"github.com/andresousadotpt/texpand/internal/keymap"
	"github.com/andresousadotpt/texpand/internal/output"
)

// screen simulates the focused application: it receives both the user's
// physical keystrokes and the daemon's virtual-keyboard output, applying
// the Polish Programmer layout. It implements output.Keyboard.
type screen struct {
	text      []rune
	cursor    int
	shift     int
	altgr     int
	caps      bool
	spaceHeld int // physical Space still down — models toolkits that ignore arrows then
}

func (s *screen) insert(runes ...rune) {
	s.text = append(s.text, 0)
	copy(s.text[s.cursor+len(runes):], s.text[s.cursor:len(s.text)-len(runes)])
	copy(s.text[s.cursor:], runes)
	s.cursor += len(runes)
}

func (s *screen) KeyDown(k int) error {
	switch k {
	case uinput.KeyLeftshift, uinput.KeyRightshift:
		s.shift++
	case uinput.KeyRightalt:
		s.altgr++
	default:
		return s.KeyPress(k)
	}
	return nil
}

func (s *screen) KeyUp(k int) error {
	switch k {
	case uinput.KeyLeftshift, uinput.KeyRightshift:
		if s.shift > 0 {
			s.shift--
		}
	case uinput.KeyRightalt:
		if s.altgr > 0 {
			s.altgr--
		}
	}
	return nil
}

func (s *screen) KeyPress(k int) error {
	code := evdev.EvCode(k)
	switch code {
	case evdev.KEY_CAPSLOCK:
		s.caps = !s.caps
		return nil
	case evdev.KEY_BACKSPACE:
		if s.cursor > 0 {
			copy(s.text[s.cursor-1:], s.text[s.cursor:])
			s.text = s.text[:len(s.text)-1]
			s.cursor--
		}
		return nil
	case uinput.KeyLeft:
		// Many Wayland clients ignore arrow keys while Space is still held.
		// Correcting on Space key-down therefore moves Left into a no-op and
		// backspaces eat the separator — deferral until key-up avoids that.
		if s.spaceHeld > 0 {
			return nil
		}
		if s.cursor > 0 {
			s.cursor--
		}
		return nil
	case uinput.KeyRight:
		if s.spaceHeld > 0 {
			return nil
		}
		if s.cursor < len(s.text) {
			s.cursor++
		}
		return nil
	case evdev.KEY_ENTER, evdev.KEY_KPENTER:
		s.insert('\n')
		return nil
	case evdev.KEY_TAB:
		s.insert('\t')
		return nil
	}
	if s.altgr > 0 {
		if r, ok := keymap.AltGr[code]; ok {
			if (s.shift > 0) != s.caps {
				r = []rune(strings.ToUpper(string(r)))[0]
			}
			s.insert(r)
		}
		return nil
	}
	if kc, ok := keymap.Chars[code]; ok {
		ch := kc.Normal
		useShift := s.shift > 0
		if len(kc.Normal) == 1 && kc.Normal[0] >= 'a' && kc.Normal[0] <= 'z' && s.caps {
			useShift = !useShift
		}
		if useShift {
			ch = kc.Shifted
		}
		s.insert([]rune(ch)...)
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
	tracker   *inputstate.Tracker
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
	tracker := inputstate.New(false)
	c := correct.New(opts)
	c.SetLookup(rigLookup{})
	capsLock := func() bool { return tracker.Snapshot().Caps }
	w := &output.Writer{Kbd: scr, Backends: []output.Backend{&output.Uinput{Kbd: scr, CapsLock: capsLock}}, CapsLock: capsLock}
	return &rig{t: t, scr: scr, corrector: c, writer: w, tracker: tracker}
}

// event feeds one raw event through the pipeline: the "app" (screen) sees
// the physical key first (the daemon never delays real input), then the
// corrector reacts, possibly rewriting the screen through the writer.
func (r *rig) event(code evdev.EvCode, value int32) {
	if code == evdev.KEY_SPACE {
		if value >= 1 {
			r.scr.spaceHeld++
		} else if r.scr.spaceHeld > 0 {
			r.scr.spaceHeld--
		}
	}
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
	mods := r.tracker.Handle("kbd", code, value)
	res := r.corrector.HandleEvent(correct.KeyEvent{Code: code, Value: value, Modifiers: mods})
	if res.Plan != nil {
		edit := output.Edit{
			Backspaces:     res.Plan.Backspaces,
			Text:           res.Plan.Type,
			Restore:        res.Plan.Restore,
			PreserveSuffix: res.Plan.PreserveSuffix,
		}
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

func TestEndToEndTwoCorrectionsInSameField(t *testing.T) {
	r := newRig(t, correct.DefaultOptions())
	r.typeString("zolw ")
	r.expect("żółw ")
	r.typeString("zolw ")
	r.expect("żółw żółw ")
	r.typeString("zrodlo ")
	r.expect("żółw żółw źródło ")
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

func TestEndToEndCorrectionWithCapsLock(t *testing.T) {
	r := newRig(t, correct.DefaultOptions())
	r.key(evdev.KEY_CAPSLOCK)
	r.typeString("zolw ")
	r.expect("ŻÓŁW ")
}

func TestEndToEndCtrlShortcut(t *testing.T) {
	r := newRig(t, correct.DefaultOptions())
	r.typeString("zolw")
	r.event(evdev.KEY_LEFTCTRL, 1)
	mods := r.tracker.Snapshot()
	r.corrector.HandleEvent(correct.KeyEvent{Code: evdev.KEY_C, Value: 1, Modifiers: mods}) // Ctrl+C: app gets no char
	r.corrector.HandleEvent(correct.KeyEvent{Code: evdev.KEY_C, Value: 0, Modifiers: mods})
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
	t       *testing.T
	scr     *screen
	e       *Expander
	tracker *inputstate.Tracker
}

func newExpanderRig(t *testing.T, mode string) *expanderRig {
	scr := &screen{}
	tracker := inputstate.New(false)
	capsLock := func() bool { return tracker.Snapshot().Caps }
	e := NewExpander(expanderConfig(mode), scr, capsLock)
	// Restrict output to uinput so the test never shells out to wtype.
	e.writer = &output.Writer{Kbd: scr, Backends: []output.Backend{&output.Uinput{Kbd: scr, CapsLock: capsLock}}, CapsLock: capsLock}
	return &expanderRig{t: t, scr: scr, e: e, tracker: tracker}
}

func (r *expanderRig) event(code evdev.EvCode, value int32) bool {
	return r.eventDevice("kbd", code, value)
}

func (r *expanderRig) eventDevice(device string, code evdev.EvCode, value int32) bool {
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
	case evdev.KEY_CAPSLOCK:
		if value == 1 {
			r.scr.KeyPress(int(code))
		}
	}
	mods := r.tracker.Handle(device, code, value)
	return r.e.HandleEvent(KeyEvent{Device: device, Code: code, Value: value, Modifiers: mods})
}

func (r *expanderRig) typeString(s string) {
	for _, ru := range s {
		rk, ok := keymap.Reverse[ru]
		if !ok {
			r.t.Fatalf("no key for %q", ru)
		}
		if rk.Shift {
			r.event(evdev.KEY_LEFTSHIFT, 1)
		}
		r.scr.KeyPress(rk.Code)
		r.event(evdev.EvCode(rk.Code), 1)
		r.event(evdev.EvCode(rk.Code), 0)
		if rk.Shift {
			r.event(evdev.KEY_LEFTSHIFT, 0)
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

func TestExpanderDefersShiftedFinalCharacter(t *testing.T) {
	r := newExpanderRig(t, "immediate")
	r.e.Reload(&Config{TriggerMode: "immediate", Matches: []Match{{Trigger: ":A", Replace: "done"}}})
	r.typeString(":A")
	if got := r.scr.String(); got != "done" {
		t.Fatalf("screen = %q, want deferred expansion after Shift release", got)
	}
}

func TestExpanderWaitsForShiftOnEveryKeyboard(t *testing.T) {
	r := newExpanderRig(t, "immediate")
	r.e.Reload(&Config{TriggerMode: "immediate", Matches: []Match{{Trigger: ":A", Replace: "done"}}})
	r.typeString(":")
	r.eventDevice("kbd-a", evdev.KEY_LEFTSHIFT, 1)
	r.eventDevice("kbd-b", evdev.KEY_RIGHTSHIFT, 1)
	r.scr.KeyPress(int(evdev.KEY_A))
	if expanded := r.eventDevice("kbd-a", evdev.KEY_A, 1); expanded {
		t.Fatal("expanded while both Shift keys were held")
	}
	r.eventDevice("kbd-a", evdev.KEY_A, 0)
	if expanded := r.eventDevice("kbd-a", evdev.KEY_LEFTSHIFT, 0); expanded {
		t.Fatal("expanded while the second keyboard still held Shift")
	}
	if expanded := r.eventDevice("kbd-b", evdev.KEY_RIGHTSHIFT, 0); !expanded {
		t.Fatal("did not expand after the final Shift release")
	}
	if got := r.scr.String(); got != "done" {
		t.Fatalf("screen = %q, want %q", got, "done")
	}
}

func TestExpanderTracksCapsLockAndTypesDesiredCase(t *testing.T) {
	r := newExpanderRig(t, "immediate")
	r.e.Reload(&Config{TriggerMode: "immediate", Matches: []Match{{Trigger: "AB", Replace: "ok"}}})
	r.event(evdev.KEY_CAPSLOCK, 1)
	for _, code := range []evdev.EvCode{evdev.KEY_A, evdev.KEY_B} {
		r.scr.KeyPress(int(code))
		r.event(code, 1)
		r.event(code, 0)
	}
	if got := r.scr.String(); got != "ok" {
		t.Fatalf("screen = %q, want lowercase replacement under Caps Lock", got)
	}
}

func TestExpanderIgnoresKeysHeldWithCtrl(t *testing.T) {
	r := newExpanderRig(t, "immediate")
	r.e.Reload(&Config{TriggerMode: "immediate", Matches: []Match{{Trigger: "ba", Replace: "expanded"}}})
	r.event(evdev.KEY_LEFTCTRL, 1)
	if expanded := r.event(evdev.KEY_B, 1); expanded {
		t.Fatal("Ctrl+B polluted the trigger buffer")
	}
	r.event(evdev.KEY_B, 0)
	r.event(evdev.KEY_LEFTCTRL, 0)
	r.scr.KeyPress(int(evdev.KEY_A))
	if expanded := r.event(evdev.KEY_A, 1); expanded {
		t.Fatal("Ctrl+B remained in the buffer and formed a false trigger")
	}
	r.event(evdev.KEY_A, 0)
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
