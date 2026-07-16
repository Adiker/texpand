package appfilter

import "testing"

func TestExcluderMatching(t *testing.T) {
	tr := &Tracker{}
	e := NewExcluder(tr, []string{"konsole", "jetbrains-*", "Org.KDE.Kate"}, true)

	cases := []struct {
		app  string
		want bool // ShouldCorrect
	}{
		{"konsole", false},
		{"Konsole", false}, // case-insensitive
		{"jetbrains-idea", false},
		{"jetbrains-goland", false},
		{"org.kde.kate", false}, // pattern lowercased
		{"firefox", true},
		{"konsole2", true}, // no implicit prefix match
	}
	for _, c := range cases {
		tr.Set(c.app)
		if got := e.ShouldCorrect(); got != c.want {
			t.Errorf("ShouldCorrect(%s) = %v, want %v", c.app, got, c.want)
		}
	}
}

func TestExcluderUnknownApp(t *testing.T) {
	tr := &Tracker{}
	e := NewExcluder(tr, []string{"konsole"}, true)
	if !e.ShouldCorrect() {
		t.Error("on_unknown_app=correct should allow correction")
	}
	e.Configure([]string{"konsole"}, false)
	if e.ShouldCorrect() {
		t.Error("on_unknown_app=skip should block correction")
	}
}

func TestExcluderReconfigure(t *testing.T) {
	tr := &Tracker{}
	tr.Set("newterm")
	e := NewExcluder(tr, nil, true)
	if !e.ShouldCorrect() {
		t.Error("empty exclusions should correct")
	}
	e.Configure([]string{"newterm"}, true)
	if e.ShouldCorrect() {
		t.Error("reconfigured exclusion not applied")
	}
}

func TestInvalidGlobFallsBackToLiteral(t *testing.T) {
	tr := &Tracker{}
	tr.Set("app[1")
	e := NewExcluder(tr, []string{"app[1"}, true)
	if e.ShouldCorrect() {
		t.Error("literal match of invalid glob failed")
	}
}
