package control

import (
	"strings"
	"testing"
)

type statusDaemon struct{ status Status }

func (d statusDaemon) SetAutocorrectEnabled(bool) {}
func (d statusDaemon) AutocorrectStatus() Status  { return d.status }
func (d statusDaemon) SetActiveWindow(string)     {}

func TestStatusDetailsKeepsLegacyStatusCompatible(t *testing.T) {
	h := &handler{d: statusDaemon{status: Status{
		Enabled: true, DictReady: true, DictState: "ready",
		Words: 12, Candidates: 7, ActiveApp: "firefox",
	}}}
	enabled, ready, words, candidates, app, dbusErr := h.Status()
	if dbusErr != nil || !enabled || !ready || words != 12 || candidates != 7 || app != "firefox" {
		t.Fatalf("legacy Status = %v %v %d %d %q %v", enabled, ready, words, candidates, app, dbusErr)
	}
	enabled, state, words, candidates, app, errText, dbusErr := h.StatusDetails()
	if dbusErr != nil || !enabled || state != "ready" || words != 12 || candidates != 7 || app != "firefox" || errText != "" {
		t.Fatalf("StatusDetails = %v %q %d %d %q %q %v", enabled, state, words, candidates, app, errText, dbusErr)
	}
}

func TestFormatStatusStates(t *testing.T) {
	cases := []struct {
		status Status
		want   string
	}{
		{Status{Enabled: false, DictState: "idle"}, "dictionary:  idle"},
		{Status{Enabled: true, DictState: "loading"}, "dictionary:  loading"},
		{Status{Enabled: true, DictState: "ready", Words: 10, Candidates: 3}, "ready (10 word forms, 3 candidate pairs)"},
		{Status{Enabled: true, DictState: "failed", DictError: "missing file"}, "failed (missing file)"},
	}
	for _, tc := range cases {
		if got := formatStatus(tc.status); !strings.Contains(got, tc.want) {
			t.Errorf("formatStatus(%+v) = %q, want %q", tc.status, got, tc.want)
		}
	}
}
