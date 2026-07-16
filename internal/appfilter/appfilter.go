// Package appfilter decides whether autocorrection may run in the
// currently focused application.
//
// On KDE Plasma Wayland there is no stable public "active window class"
// query, so texpand loads a tiny KWin script (see kwin.go) that pushes the
// active window's resourceClass to texpand's DBus name on every window
// activation. The Tracker stores the latest value; the Excluder matches it
// against the configured patterns. If no detection is available the
// behaviour is configurable (correct anyway / skip), defaulting to
// correct.
package appfilter

import (
	"path"
	"strings"
	"sync/atomic"
)

// Tracker records the most recently activated application class. Safe for
// concurrent use: the DBus goroutine writes, the event loop reads.
type Tracker struct {
	v atomic.Value // string; "" = unknown
}

// Set records the active application class ("" clears it).
func (t *Tracker) Set(class string) {
	t.v.Store(strings.ToLower(class))
}

// ActiveApp returns the active application class and whether it is known.
func (t *Tracker) ActiveApp() (string, bool) {
	s, _ := t.v.Load().(string)
	return s, s != ""
}

// DefaultExcludedApps lists window classes where autocorrection is off by
// default: terminals, IDEs/code editors, and remote-desktop clients, where
// rewriting "commands" or code is destructive.
var DefaultExcludedApps = []string{
	// terminals
	"org.kde.konsole", "konsole", "org.kde.yakuake", "yakuake",
	"kitty", "alacritty", "foot", "footclient",
	"org.wezfurlong.wezterm", "wezterm", "xterm", "tilix",
	"org.gnome.ptyxis", "ptyxis", "com.mitchellh.ghostty", "ghostty",
	// IDEs and code editors
	"code", "code-oss", "codium", "vscodium", "code-url-handler",
	"jetbrains-*", "org.kde.kate", "kate", "org.kde.kdevelop",
	"emacs", "sublime_text", "dev.zed.zed",
	// remote desktop / VMs
	"remmina", "org.remmina.remmina", "org.kde.krdc",
	"virt-manager", "vncviewer", "org.freerdp.freerdp",
}

// Excluder matches the active application against exclusion patterns.
type Excluder struct {
	tracker  *Tracker
	patterns atomic.Value // []string, lowercase globs
	// correctOnUnknown: what to do when the active app cannot be
	// determined.
	correctOnUnknown atomic.Bool
}

// NewExcluder creates an Excluder over the tracker.
func NewExcluder(t *Tracker, patterns []string, correctOnUnknown bool) *Excluder {
	e := &Excluder{tracker: t}
	e.Configure(patterns, correctOnUnknown)
	return e
}

// Configure replaces the exclusion patterns (safe at runtime, e.g. config
// hot-reload).
func (e *Excluder) Configure(patterns []string, correctOnUnknown bool) {
	lowered := make([]string, len(patterns))
	for i, p := range patterns {
		lowered[i] = strings.ToLower(strings.TrimSpace(p))
	}
	e.patterns.Store(lowered)
	e.correctOnUnknown.Store(correctOnUnknown)
}

// ShouldCorrect reports whether correction may run in the active app.
// Fast and non-blocking: two atomic loads and a small pattern scan.
func (e *Excluder) ShouldCorrect() bool {
	app, known := e.tracker.ActiveApp()
	if !known {
		return e.correctOnUnknown.Load()
	}
	patterns, _ := e.patterns.Load().([]string)
	for _, p := range patterns {
		if matchClass(p, app) {
			return false
		}
	}
	return true
}

// matchClass matches a lowercase glob pattern against a lowercase window
// class. Invalid patterns fall back to literal comparison.
func matchClass(pattern, class string) bool {
	if !strings.ContainsAny(pattern, "*?[") {
		return pattern == class
	}
	ok, err := path.Match(pattern, class)
	if err != nil {
		return pattern == class
	}
	return ok
}
