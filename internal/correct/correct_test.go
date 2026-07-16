package correct

import (
	"testing"

	evdev "github.com/holoplot/go-evdev"

	"github.com/andresousadotpt/texpand/internal/keymap"
)

// fakeLookup is a tiny in-memory dictionary for tests.
type fakeLookup struct {
	words map[string]bool
	cands map[string][]string
}

func testLookup() *fakeLookup {
	return &fakeLookup{
		words: map[string]bool{
			"laska": true, "pisze": true, "kot": true, "ma": true,
		},
		cands: map[string][]string{
			"zolw":    {"żółw"},
			"zrodlo":  {"źródło"},
			"wlasnie": {"właśnie"},
			"laska":   {"łaska"},
			"pisze":   {"piszę"},
			"zle":     {"złe", "źle"},
			"sa":      {"są"},
			"lw":      {"lś"}, // fragment with a candidate, for invalidation tests
		},
	}
}

func (f *fakeLookup) IsWord(w string) bool { return f.words[w] }
func (f *fakeLookup) Candidates(k string, buf []string) []string {
	return append(buf, f.cands[k]...)
}

// driver feeds synthetic events and records plans.
type driver struct {
	t *testing.T
	c *Corrector
	// every non-empty result, in order
	results []Result
}

func newDriver(t *testing.T, opts Options) *driver {
	c := New(opts)
	c.SetLookup(testLookup())
	return &driver{t: t, c: c}
}

// send feeds one event, recording any non-empty result (plans can surface
// on Shift release, so every event must be captured).
func (d *driver) send(code evdev.EvCode, value int32) Result {
	r := d.c.HandleEvent(KeyEvent{Code: code, Value: value})
	if r.Plan != nil || r.Toggled {
		d.results = append(d.results, r)
	}
	return r
}

// key sends press+release and returns the press result.
func (d *driver) key(code evdev.EvCode) Result {
	r := d.send(code, 1)
	d.send(code, 0)
	return r
}

// typeString types s using the Polish Programmer layout, pressing and
// releasing Shift/AltGr around each character like a human would. It
// returns the results recorded while typing s.
func (d *driver) typeString(s string) []Result {
	start := len(d.results)
	for _, r := range s {
		rk, ok := keymap.Reverse[r]
		if !ok {
			d.t.Fatalf("typeString: no key for %q", r)
		}
		if rk.Shift {
			d.send(evdev.KEY_LEFTSHIFT, 1)
		}
		if rk.AltGr {
			d.send(evdev.KEY_RIGHTALT, 1)
		}
		d.key(evdev.EvCode(rk.Code))
		if rk.AltGr {
			d.send(evdev.KEY_RIGHTALT, 0)
		}
		if rk.Shift {
			d.send(evdev.KEY_LEFTSHIFT, 0)
		}
	}
	return d.results[start:]
}

func expectPlan(t *testing.T, results []Result, backspaces int, text string) {
	t.Helper()
	if len(results) != 1 || results[0].Plan == nil {
		t.Fatalf("expected exactly one plan, got %+v", results)
	}
	p := results[0].Plan
	if p.Backspaces != backspaces || p.Type != text {
		t.Fatalf("plan = {%d, %q}, want {%d, %q}", p.Backspaces, p.Type, backspaces, text)
	}
}

func expectNoPlan(t *testing.T, results []Result) {
	t.Helper()
	if len(results) != 0 {
		t.Fatalf("expected no plan, got %+v", results)
	}
}

func TestBasicCorrection(t *testing.T) {
	d := newDriver(t, DefaultOptions())
	expectPlan(t, d.typeString("zolw "), 5, "żółw ")
}

func TestPunctuationSeparators(t *testing.T) {
	for _, sep := range []string{".", ",", "!", "?", ":", ";", ")", "]", "}", "\""} {
		d := newDriver(t, DefaultOptions())
		res := d.typeString("zrodlo" + sep)
		expectPlan(t, res, 7, "źródło"+sep)
	}
}

func TestAmbiguousAndValidWordsUntouched(t *testing.T) {
	for _, w := range []string{"pisze ", "laska ", "zle ", "kot "} {
		d := newDriver(t, DefaultOptions())
		expectNoPlan(t, d.typeString(w))
	}
}

func TestUnknownWordNoCandidates(t *testing.T) {
	d := newDriver(t, DefaultOptions())
	expectNoPlan(t, d.typeString("qwerty "))
}

func TestCasePreservation(t *testing.T) {
	cases := []struct{ in, out string }{
		{"zolw ", "żółw "},
		{"Zolw ", "Żółw "},
		{"ZOLW ", "ŻÓŁW "},
		{"Zolw.", "Żółw."},
		{"WLASNIE!", "WŁAŚNIE!"},
	}
	for _, c := range cases {
		d := newDriver(t, DefaultOptions())
		res := d.typeString(c.in)
		expectPlan(t, res, len([]rune(c.in)), c.out)
	}
}

func TestMixedCaseSkipped(t *testing.T) {
	for _, w := range []string{"zOlw ", "ZoLW ", "zolW "} {
		d := newDriver(t, DefaultOptions())
		expectNoPlan(t, d.typeString(w))
	}
}

func TestCapsLockUppercase(t *testing.T) {
	d := newDriver(t, DefaultOptions())
	d.key(evdev.KEY_CAPSLOCK)
	// With Caps Lock on, plain letters are uppercase on screen.
	res := d.typeString("zolw ")
	expectPlan(t, res, 5, "ŻÓŁW ")
}

func TestCapsLockSeededFromLED(t *testing.T) {
	d := newDriver(t, DefaultOptions())
	d.c.SetCapsLock(true)
	res := d.typeString("zolw ")
	expectPlan(t, res, 5, "ŻÓŁW ")
}

func TestAltGrDiacriticsDisableCorrection(t *testing.T) {
	// User typed żółw themselves: contains diacritics → rule 1 skips.
	d := newDriver(t, DefaultOptions())
	expectNoPlan(t, d.typeString("żółw "))
	// Partially: "zóltko" has ó typed manually.
	d = newDriver(t, DefaultOptions())
	expectNoPlan(t, d.typeString("zólw "))
}

func TestMinWordLength(t *testing.T) {
	d := newDriver(t, DefaultOptions())
	// "sa" (len 2) corrects with default min 2...
	expectPlan(t, d.typeString("sa "), 3, "są ")
	// ...but not with min 3.
	opts := DefaultOptions()
	opts.MinWordLength = 3
	d = newDriver(t, opts)
	expectNoPlan(t, d.typeString("sa "))
}

func TestImpureTokensNeverCorrected(t *testing.T) {
	for _, w := range []string{"zolw1 ", "zo-lw ", "zol'w ", "zolw@x ", "zolw/a ", "1zolw "} {
		d := newDriver(t, DefaultOptions())
		expectNoPlan(t, d.typeString(w))
	}
}

func TestHyphenatedCompoundUntouched(t *testing.T) {
	d := newDriver(t, DefaultOptions())
	expectNoPlan(t, d.typeString("bialo-czerwony "))
}

func TestOpenersStartFreshWord(t *testing.T) {
	d := newDriver(t, DefaultOptions())
	res := d.typeString("(zolw)")
	expectPlan(t, res, 5, "żółw)")
	d = newDriver(t, DefaultOptions())
	res = d.typeString("\"zolw\"")
	expectPlan(t, res, 5, "żółw\"")
}

func TestBackspaceEditing(t *testing.T) {
	d := newDriver(t, DefaultOptions())
	d.typeString("zolwx")
	d.key(evdev.KEY_BACKSPACE)
	res := d.typeString(" ")
	expectPlan(t, res, 5, "żółw ")
}

func TestBackspaceIntoUnknownTextSuppresses(t *testing.T) {
	d := newDriver(t, DefaultOptions())
	// Deleting past what we observed → next word must not be corrected.
	d.key(evdev.KEY_BACKSPACE)
	expectNoPlan(t, d.typeString("zolw "))
	// The word after that clean boundary corrects again.
	expectPlan(t, d.typeString("zolw "), 5, "żółw ")
}

func TestCursorMovementInvalidates(t *testing.T) {
	for _, nav := range []evdev.EvCode{
		evdev.KEY_LEFT, evdev.KEY_RIGHT, evdev.KEY_UP, evdev.KEY_DOWN,
		evdev.KEY_HOME, evdev.KEY_END, evdev.KEY_DELETE, evdev.KEY_PAGEUP,
	} {
		d := newDriver(t, DefaultOptions())
		d.typeString("zo")
		d.key(nav)
		// "lw" alone has a candidate in the fake dictionary, but the
		// buffer is unreliable → no correction.
		expectNoPlan(t, d.typeString("lw "))
	}
}

func TestCtrlShortcutsInvalidate(t *testing.T) {
	d := newDriver(t, DefaultOptions())
	d.typeString("zo")
	// Ctrl+C while holding ctrl.
	d.c.HandleEvent(KeyEvent{Code: evdev.KEY_LEFTCTRL, Value: 1})
	d.key(evdev.KEY_C)
	d.c.HandleEvent(KeyEvent{Code: evdev.KEY_LEFTCTRL, Value: 0})
	expectNoPlan(t, d.typeString("lw "))
}

func TestCtrlHeldBlocksCorrection(t *testing.T) {
	d := newDriver(t, DefaultOptions())
	d.typeString("zolw")
	d.c.HandleEvent(KeyEvent{Code: evdev.KEY_LEFTCTRL, Value: 1})
	res := d.key(evdev.KEY_SPACE) // Ctrl+Space: a shortcut, not a boundary
	if res.Plan != nil {
		t.Fatal("correction fired during Ctrl chord")
	}
	d.c.HandleEvent(KeyEvent{Code: evdev.KEY_LEFTCTRL, Value: 0})
}

func TestMetaAndAltInvalidate(t *testing.T) {
	for _, mod := range []evdev.EvCode{evdev.KEY_LEFTMETA, evdev.KEY_LEFTALT} {
		d := newDriver(t, DefaultOptions())
		d.typeString("zo")
		d.c.HandleEvent(KeyEvent{Code: mod, Value: 1})
		d.key(evdev.KEY_TAB)
		d.c.HandleEvent(KeyEvent{Code: mod, Value: 0})
		expectNoPlan(t, d.typeString("lw "))
	}
}

func TestEnterAndTabDefaultCommitWithoutCorrecting(t *testing.T) {
	d := newDriver(t, DefaultOptions())
	res := d.key(evdev.KEY_ENTER)
	if res.Plan != nil {
		t.Fatal("plan on bare enter")
	}
	d.typeString("zolw")
	if r := d.key(evdev.KEY_ENTER); r.Plan != nil {
		t.Fatal("enter corrected by default")
	}
	// The boundary still commits: next word corrects normally.
	expectPlan(t, d.typeString("zolw "), 5, "żółw ")

	d = newDriver(t, DefaultOptions())
	d.typeString("zolw")
	if r := d.key(evdev.KEY_TAB); r.Plan != nil {
		t.Fatal("tab corrected by default")
	}
}

func TestEnterCorrectionOptIn(t *testing.T) {
	opts := DefaultOptions()
	opts.OnEnter = true
	d := newDriver(t, opts)
	d.typeString("zolw")
	r := d.key(evdev.KEY_ENTER)
	if r.Plan == nil || r.Plan.Backspaces != 5 || r.Plan.Type != "żółw\n" {
		t.Fatalf("enter opt-in plan = %+v", r.Plan)
	}
}

func TestShiftedSeparatorDefersUntilShiftRelease(t *testing.T) {
	// '!' is Shift+1; typing through the virtual keyboard while the user
	// still holds Shift would produce uppercase (modifier state is merged
	// across keyboards). The plan must only appear on Shift release.
	d := newDriver(t, DefaultOptions())
	d.typeString("zolw")
	d.send(evdev.KEY_LEFTSHIFT, 1)
	if r := d.send(evdev.KEY_1, 1); r.Plan != nil {
		t.Fatal("plan emitted while shift held")
	}
	d.send(evdev.KEY_1, 0)
	r := d.send(evdev.KEY_LEFTSHIFT, 0)
	if r.Plan == nil || r.Plan.Type != "żółw!" {
		t.Fatalf("plan on shift release = %+v", r.Plan)
	}
}

func TestPendingPlanCancelledByNextKey(t *testing.T) {
	// User keeps Shift held and types more: the deferred correction must
	// be dropped, not applied to text that moved on.
	d := newDriver(t, DefaultOptions())
	d.typeString("zolw")
	d.send(evdev.KEY_LEFTSHIFT, 1)
	d.send(evdev.KEY_1, 1) // '!' → pending
	d.send(evdev.KEY_1, 0)
	d.send(evdev.KEY_A, 1) // still shifted: "A"
	d.send(evdev.KEY_A, 0)
	r := d.send(evdev.KEY_LEFTSHIFT, 0)
	if r.Plan != nil {
		t.Fatalf("stale pending plan emitted: %+v", r.Plan)
	}
}

func TestUndo(t *testing.T) {
	d := newDriver(t, DefaultOptions())
	expectPlan(t, d.typeString("zolw "), 5, "żółw ")

	r := d.c.HandleEvent(KeyEvent{Code: evdev.KEY_BACKSPACE, Value: 1})
	if r.Plan == nil || !r.Undo {
		t.Fatalf("backspace after correction: %+v", r)
	}
	// Backspace deleted the space; undo removes żółw (4 runes) and
	// restores the typed word.
	if r.Plan.Backspaces != 4 || r.Plan.Type != "zolw" {
		t.Fatalf("undo plan = %+v", r.Plan)
	}
	d.c.HandleEvent(KeyEvent{Code: evdev.KEY_BACKSPACE, Value: 0})

	// The restored word must not be re-corrected at the next boundary.
	expectNoPlan(t, d.typeString(" "))
	// But the word after it corrects again.
	expectPlan(t, d.typeString("zolw "), 5, "żółw ")
}

func TestUndoPreservesCase(t *testing.T) {
	d := newDriver(t, DefaultOptions())
	expectPlan(t, d.typeString("Zolw!"), 5, "Żółw!")
	r := d.c.HandleEvent(KeyEvent{Code: evdev.KEY_BACKSPACE, Value: 1})
	if r.Plan == nil || r.Plan.Type != "Zolw" {
		t.Fatalf("undo plan = %+v", r.Plan)
	}
}

func TestUndoOnlyImmediately(t *testing.T) {
	// Typing anything else commits the correction; Backspace then just
	// deletes normally.
	d := newDriver(t, DefaultOptions())
	expectPlan(t, d.typeString("zolw "), 5, "żółw ")
	d.typeString("a")
	r := d.c.HandleEvent(KeyEvent{Code: evdev.KEY_BACKSPACE, Value: 1})
	if r.Plan != nil {
		t.Fatalf("undo after intervening key: %+v", r.Plan)
	}
}

func TestSecondSeparatorCommits(t *testing.T) {
	d := newDriver(t, DefaultOptions())
	expectPlan(t, d.typeString("zolw "), 5, "żółw ")
	expectNoPlan(t, d.typeString(" ")) // second space: no new plan
	r := d.c.HandleEvent(KeyEvent{Code: evdev.KEY_BACKSPACE, Value: 1})
	if r.Plan != nil {
		t.Fatal("undo available after second separator")
	}
}

func TestUndoDisabled(t *testing.T) {
	opts := DefaultOptions()
	opts.Undo = false
	d := newDriver(t, opts)
	expectPlan(t, d.typeString("zolw "), 5, "żółw ")
	r := d.c.HandleEvent(KeyEvent{Code: evdev.KEY_BACKSPACE, Value: 1})
	if r.Plan != nil {
		t.Fatal("undo fired although disabled")
	}
}

func TestToggleShortcut(t *testing.T) {
	sc, err := keymap.ParseShortcut("ctrl+alt+slash")
	if err != nil {
		t.Fatal(err)
	}
	opts := DefaultOptions()
	opts.Toggle = sc
	d := newDriver(t, opts)

	d.c.HandleEvent(KeyEvent{Code: evdev.KEY_LEFTCTRL, Value: 1})
	d.c.HandleEvent(KeyEvent{Code: evdev.KEY_LEFTALT, Value: 1})
	r := d.c.HandleEvent(KeyEvent{Code: evdev.KEY_SLASH, Value: 1})
	if !r.Toggled {
		t.Fatal("toggle shortcut not detected")
	}
	d.c.HandleEvent(KeyEvent{Code: evdev.KEY_SLASH, Value: 0})
	d.c.HandleEvent(KeyEvent{Code: evdev.KEY_LEFTALT, Value: 0})
	d.c.HandleEvent(KeyEvent{Code: evdev.KEY_LEFTCTRL, Value: 0})

	// Plain slash is not a toggle.
	r = d.c.HandleEvent(KeyEvent{Code: evdev.KEY_SLASH, Value: 1})
	if r.Toggled {
		t.Fatal("bare slash toggled")
	}
}

func TestDisabledDoesNothing(t *testing.T) {
	d := newDriver(t, DefaultOptions())
	d.c.SetEnabled(false)
	expectNoPlan(t, d.typeString("zolw "))
	d.c.SetEnabled(true)
	expectPlan(t, d.typeString("zolw "), 5, "żółw ")
}

func TestExclusionCallback(t *testing.T) {
	excluded := true
	opts := DefaultOptions()
	opts.ShouldCorrect = func() bool { return !excluded }
	d := newDriver(t, opts)
	expectNoPlan(t, d.typeString("zolw "))
	excluded = false
	expectPlan(t, d.typeString("zolw "), 5, "żółw ")
}

func TestNoLookupNoCorrection(t *testing.T) {
	c := New(DefaultOptions())
	d := &driver{t: t, c: c} // dictionary still loading
	expectNoPlan(t, d.typeString("zolw "))
}

func TestKeyRepeat(t *testing.T) {
	d := newDriver(t, DefaultOptions())
	// "zol" then holding w: value 1 then repeats.
	d.typeString("zol")
	d.c.HandleEvent(KeyEvent{Code: evdev.KEY_W, Value: 1})
	d.c.HandleEvent(KeyEvent{Code: evdev.KEY_W, Value: 2})
	d.c.HandleEvent(KeyEvent{Code: evdev.KEY_W, Value: 0})
	// Buffer is "zolww" → no candidate → nothing.
	expectNoPlan(t, d.typeString(" "))
}

func TestOverflowNeverGrowsNorCorrects(t *testing.T) {
	d := newDriver(t, DefaultOptions())
	for i := 0; i < 100; i++ {
		d.typeString("z")
	}
	if cap(d.c.buf) != 64 {
		t.Fatalf("buffer grew to cap %d", cap(d.c.buf))
	}
	expectNoPlan(t, d.typeString(" "))
}

func TestResetClearsState(t *testing.T) {
	d := newDriver(t, DefaultOptions())
	d.c.HandleEvent(KeyEvent{Code: evdev.KEY_LEFTSHIFT, Value: 1}) // stuck shift
	d.typeString("zol")
	d.c.Reset() // keyboard hotplug
	expectNoPlan(t, d.typeString("w "))
	expectPlan(t, d.typeString("zolw "), 5, "żółw ")
}

func TestInvalidateSuppressesCurrentWord(t *testing.T) {
	d := newDriver(t, DefaultOptions())
	d.typeString("zo")
	d.c.Invalidate() // e.g. the expander rewrote text
	expectNoPlan(t, d.typeString("lw "))
}

func TestOrdinaryKeyPathAllocationFree(t *testing.T) {
	c := New(DefaultOptions())
	c.SetLookup(testLookup())
	ev := KeyEvent{Code: evdev.KEY_A, Value: 1}
	rel := KeyEvent{Code: evdev.KEY_A, Value: 0}
	bs := KeyEvent{Code: evdev.KEY_BACKSPACE, Value: 1}
	allocs := testing.AllocsPerRun(1000, func() {
		c.HandleEvent(ev)
		c.HandleEvent(rel)
		c.HandleEvent(bs)
	})
	if allocs != 0 {
		t.Errorf("ordinary key path allocated %v times per event group, want 0", allocs)
	}
}
