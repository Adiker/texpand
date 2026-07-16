package main

import (
	"fmt"
	"os"
	"sync/atomic"

	"github.com/andresousadotpt/texpand/internal/appfilter"
	"github.com/andresousadotpt/texpand/internal/control"
	"github.com/andresousadotpt/texpand/internal/correct"
	"github.com/andresousadotpt/texpand/internal/dict"
	"github.com/andresousadotpt/texpand/internal/output"
)

// autocorrect bundles the Polish autocorrection subsystem: the state
// machine, output writer, app exclusion, dictionary loading and the DBus
// control surface. Event handling happens on the main loop goroutine;
// DBus callbacks only touch atomics and channels.
type autocorrect struct {
	settings  AutocorrectSettings
	corrector *correct.Corrector
	writer    *output.Writer
	tracker   *appfilter.Tracker
	excluder  *appfilter.Excluder
	server    *control.Server
	kwinStop  func()

	index       atomic.Pointer[dict.Index]
	dictCh      chan *dict.Index // loader → main loop
	loadReq     chan struct{}    // control goroutine → main loop
	loadStarted bool

	// notifyOnToggle is read from the DBus goroutine while the main loop
	// may hot-reload settings, hence atomic.
	notifyOnToggle atomic.Bool
}

// newAutocorrect builds the subsystem from validated settings. The
// dictionary is NOT loaded here; the main loop starts loading via
// maybeStartDictLoad so the daemon is monitoring keys immediately.
func newAutocorrect(settings AutocorrectSettings, vkbd output.Keyboard) *autocorrect {
	ac := &autocorrect{
		settings: settings,
		tracker:  &appfilter.Tracker{},
		dictCh:   make(chan *dict.Index, 1),
		loadReq:  make(chan struct{}, 1),
	}
	ac.excluder = appfilter.NewExcluder(ac.tracker, settings.ExcludedApps, settings.CorrectOnUnknownApp)
	ac.corrector = correct.New(ac.correctorOptions(settings))
	ac.corrector.SetEnabled(settings.Enabled)
	ac.writer = &output.Writer{
		Kbd:      vkbd,
		Backends: buildBackends(settings, vkbd),
		Debug:    dbg,
	}
	ac.notifyOnToggle.Store(settings.NotifyOnToggle)
	return ac
}

func (ac *autocorrect) correctorOptions(s AutocorrectSettings) correct.Options {
	return correct.Options{
		MinWordLength: s.MinWordLength,
		OnSpace:       s.OnSpace,
		OnEnter:       s.OnEnter,
		OnTab:         s.OnTab,
		OnPunct:       s.OnPunct,
		OnClosers:     s.OnClosers,
		Undo:          s.Undo,
		Toggle:        s.Toggle,
		ShouldCorrect: ac.excluder.ShouldCorrect,
	}
}

func buildBackends(s AutocorrectSettings, vkbd output.Keyboard) []output.Backend {
	var backends []output.Backend
	wt := &output.Wtype{}
	switch s.Output {
	case "uinput":
		backends = []output.Backend{&output.Uinput{Kbd: vkbd}}
	case "wtype":
		backends = []output.Backend{wt}
	default: // auto
		backends = []output.Backend{&output.Uinput{Kbd: vkbd}}
		if wt.Available() {
			backends = append(backends, wt)
		}
	}
	if s.AllowClipboardFallback {
		backends = append(backends, &output.Clipboard{Kbd: vkbd})
	}
	return backends
}

// startControl claims the DBus name and hooks up the KWin active-window
// script. Both are best-effort: without them the daemon still corrects,
// just without CLI control / app exclusions.
func (ac *autocorrect) startControl() {
	server, err := control.StartServer(ac)
	if err != nil {
		fmt.Fprintf(os.Stderr, "texpand: WARNING: DBus control unavailable: %v\n", err)
		return
	}
	ac.server = server
	stop, err := appfilter.LoadKWinScript(server.Conn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "texpand: WARNING: active-window detection unavailable: %v\n", err)
		fmt.Fprintf(os.Stderr, "texpand: app exclusions degrade to on_unknown_app policy\n")
		return
	}
	ac.kwinStop = stop
}

// shutdown unloads the KWin script and releases the bus name.
func (ac *autocorrect) shutdown() {
	if ac.kwinStop != nil {
		ac.kwinStop()
	}
	if ac.server != nil {
		ac.server.Close()
	}
}

// maybeStartDictLoad launches the background dictionary load once. Runs on
// the main loop goroutine.
func (ac *autocorrect) maybeStartDictLoad() {
	if ac.loadStarted {
		return
	}
	ac.loadStarted = true
	settings := ac.settings
	go func() {
		ix, err := loadIndex(settings)
		if err != nil {
			fmt.Fprintf(os.Stderr, "texpand: autocorrect disabled: %v\n", err)
			return
		}
		ac.index.Store(ix)
		ac.dictCh <- ix
	}()
}

// loadIndex loads the dictionary index from cache if valid, else builds it
// from the Hunspell files (and refreshes the cache).
func loadIndex(s AutocorrectSettings) (*dict.Index, error) {
	dicPath, affPath, err := dict.Locate(s.Dictionary)
	if err != nil {
		return nil, err
	}
	cachePath := dict.CachePath()
	if s.Cache {
		if ix, err := dict.LoadCache(cachePath, dicPath, affPath); err == nil {
			dbg("dictionary index loaded from cache %s", cachePath)
			return ix, nil
		} else {
			dbg("dictionary cache unusable: %v", err)
		}
	}
	ix, err := dict.Build(dicPath, affPath)
	if err != nil {
		return nil, fmt.Errorf("build dictionary index from %s: %w", dicPath, err)
	}
	if s.Cache {
		if err := dict.SaveCache(cachePath, ix, dicPath, affPath); err != nil {
			dbg("could not write dictionary cache: %v", err)
		}
	}
	return ix, nil
}

// applySettings applies a hot-reloaded configuration. Runs on the main
// loop goroutine.
func (ac *autocorrect) applySettings(s AutocorrectSettings, vkbd output.Keyboard) {
	oldEnabled := ac.settings.Enabled
	ac.settings = s
	ac.notifyOnToggle.Store(s.NotifyOnToggle)
	ac.excluder.Configure(s.ExcludedApps, s.CorrectOnUnknownApp)
	ac.corrector.SetOptions(ac.correctorOptions(s))
	ac.writer.Backends = buildBackends(s, vkbd)
	if s.Enabled != oldEnabled {
		ac.corrector.SetEnabled(s.Enabled)
	}
	if ac.corrector.Enabled() {
		ac.maybeStartDictLoad()
	}
}

// requestDictLoad asks the main loop to start loading (non-blocking; safe
// from any goroutine).
func (ac *autocorrect) requestDictLoad() {
	select {
	case ac.loadReq <- struct{}{}:
	default:
	}
}

// notifyToggle raises a desktop notification for an enable/disable flip.
// Safe from any goroutine.
func (ac *autocorrect) notifyToggle(enabled bool) {
	if !ac.notifyOnToggle.Load() || ac.server == nil {
		return
	}
	if enabled {
		ac.server.Notify("texpand", "Polish autocorrect enabled")
	} else {
		ac.server.Notify("texpand", "Polish autocorrect disabled")
	}
}

// --- control.Daemon interface (called from the DBus goroutine) ---

func (ac *autocorrect) SetAutocorrectEnabled(v bool) {
	old := ac.corrector.Enabled()
	ac.corrector.SetEnabled(v)
	if v {
		ac.requestDictLoad()
	}
	if old != v {
		ac.notifyToggle(v)
	}
}

func (ac *autocorrect) AutocorrectStatus() control.Status {
	st := control.Status{Enabled: ac.corrector.Enabled()}
	if ix := ac.index.Load(); ix != nil {
		st.DictReady = true
		words, cands := ix.Stats()
		st.Words = uint32(words)
		st.Candidates = uint32(cands)
	}
	st.ActiveApp, _ = ac.tracker.ActiveApp()
	return st
}

func (ac *autocorrect) SetActiveWindow(class string) {
	ac.tracker.Set(class)
	dbg("active window: %s", class)
}
