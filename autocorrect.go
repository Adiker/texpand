package main

import (
	"errors"
	"fmt"
	"os"
	"sync/atomic"

	"github.com/andresousadotpt/texpand/internal/appfilter"
	"github.com/andresousadotpt/texpand/internal/control"
	"github.com/andresousadotpt/texpand/internal/correct"
	"github.com/andresousadotpt/texpand/internal/dict"
	"github.com/andresousadotpt/texpand/internal/output"
)

const (
	dictionaryIdle    = "idle"
	dictionaryLoading = "loading"
	dictionaryReady   = "ready"
	dictionaryFailed  = "failed"
)

type dictionaryStatus struct {
	State string
	Error string
}

type dictionaryLoadKey struct {
	Path  string
	Cache bool
}

type dictionaryLoadResult struct {
	Generation uint64
	Index      *dict.Index
	Err        error
}

type dictionaryLoader func(AutocorrectSettings) (*dict.Index, error)

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

	index      atomic.Pointer[dict.Index]
	dictStatus atomic.Value // dictionaryStatus; read by DBus goroutine
	dictCh     chan dictionaryLoadResult
	loadReq    chan struct{} // control goroutine → main loop
	loader     dictionaryLoader

	// The fields below are confined to the main event-loop goroutine.
	loadGeneration uint64
	loadKey        dictionaryLoadKey
	hasLoadKey     bool

	// notifyOnToggle is read from the DBus goroutine while the main loop
	// may hot-reload settings, hence atomic.
	notifyOnToggle atomic.Bool
}

// newAutocorrect builds the subsystem from validated settings. The
// dictionary is NOT loaded here; the main loop starts loading via
// maybeStartDictLoad so the daemon is monitoring keys immediately.
func newAutocorrect(settings AutocorrectSettings, vkbd output.Keyboard, capsLock func() bool) *autocorrect {
	ac := &autocorrect{
		settings: settings,
		tracker:  &appfilter.Tracker{},
		dictCh:   make(chan dictionaryLoadResult, 8),
		loadReq:  make(chan struct{}, 1),
		loader:   loadIndex,
	}
	ac.dictStatus.Store(dictionaryStatus{State: dictionaryIdle})
	ac.excluder = appfilter.NewExcluder(ac.tracker, settings.ExcludedApps, settings.CorrectOnUnknownApp)
	ac.corrector = correct.New(ac.correctorOptions(settings))
	ac.corrector.SetEnabled(settings.Enabled)
	ac.writer = &output.Writer{
		Kbd:      vkbd,
		Backends: buildBackends(settings, vkbd, capsLock),
		Debug:    dbg,
		CapsLock: capsLock,
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

func buildBackends(s AutocorrectSettings, vkbd output.Keyboard, capsLock func() bool) []output.Backend {
	var backends []output.Backend
	wt := &output.Wtype{}
	switch s.Output {
	case "uinput":
		backends = []output.Backend{&output.Uinput{Kbd: vkbd, CapsLock: capsLock}}
	case "wtype":
		backends = []output.Backend{wt}
	default: // auto
		backends = []output.Backend{&output.Uinput{Kbd: vkbd, CapsLock: capsLock}}
		if wt.Available() {
			backends = append(backends, wt)
		}
	}
	if s.AllowClipboardFallback {
		backends = append(backends, &output.Clipboard{Kbd: vkbd, Report: reportOutputError})
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

func loadKeyFor(s AutocorrectSettings) dictionaryLoadKey {
	return dictionaryLoadKey{Path: s.Dictionary, Cache: s.Cache}
}

func (ac *autocorrect) dictionaryStatus() dictionaryStatus {
	return ac.dictStatus.Load().(dictionaryStatus)
}

func (ac *autocorrect) setDictionaryStatus(state, errText string) {
	ac.dictStatus.Store(dictionaryStatus{State: state, Error: errText})
}

// maybeStartDictLoad starts or retries the desired dictionary unless the same
// configuration is already loading or ready. Runs on the main loop goroutine.
func (ac *autocorrect) maybeStartDictLoad() {
	key := loadKeyFor(ac.settings)
	status := ac.dictionaryStatus()
	if ac.hasLoadKey && ac.loadKey == key &&
		(status.State == dictionaryLoading || status.State == dictionaryReady) {
		return
	}
	ac.startDictLoad(key)
}

func (ac *autocorrect) startDictLoad(key dictionaryLoadKey) {
	ac.loadGeneration++
	generation := ac.loadGeneration
	ac.loadKey = key
	ac.hasLoadKey = true
	ac.setDictionaryStatus(dictionaryLoading, "")
	ac.index.Store(nil)
	ac.corrector.SetLookup(nil)
	settings := ac.settings
	loader := ac.loader
	go func() {
		ix, err := loader(settings)
		ac.dictCh <- dictionaryLoadResult{Generation: generation, Index: ix, Err: err}
	}()
}

// invalidateDictionary cancels the current generation logically. An in-flight
// loader may finish, but its result will be ignored.
func (ac *autocorrect) invalidateDictionary() {
	ac.loadGeneration++
	ac.hasLoadKey = false
	ac.setDictionaryStatus(dictionaryIdle, "")
	ac.index.Store(nil)
	ac.corrector.SetLookup(nil)
}

// handleDictLoadResult applies a loader result if it belongs to the current
// generation. Returns true when the result was current.
func (ac *autocorrect) handleDictLoadResult(result dictionaryLoadResult) bool {
	if result.Generation != ac.loadGeneration {
		dbg("ignoring stale dictionary load generation %d (current %d)", result.Generation, ac.loadGeneration)
		return false
	}
	if result.Err == nil && result.Index == nil {
		result.Err = errors.New("dictionary loader returned no index")
	}
	if result.Err != nil {
		ac.index.Store(nil)
		ac.corrector.SetLookup(nil)
		ac.setDictionaryStatus(dictionaryFailed, result.Err.Error())
		fmt.Fprintf(os.Stderr, "texpand: autocorrect dictionary failed: %v\n", result.Err)
		return true
	}
	ac.index.Store(result.Index)
	ac.corrector.SetLookup(result.Index)
	ac.setDictionaryStatus(dictionaryReady, "")
	words, cands := result.Index.Stats()
	fmt.Printf("texpand: Polish dictionary ready — %d word forms, %d correction candidates\n", words, cands)
	return true
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
func (ac *autocorrect) applySettings(s AutocorrectSettings, vkbd output.Keyboard, capsLock func() bool) {
	oldEnabled := ac.settings.Enabled
	dictionaryChanged := loadKeyFor(ac.settings) != loadKeyFor(s)
	ac.settings = s
	ac.notifyOnToggle.Store(s.NotifyOnToggle)
	ac.excluder.Configure(s.ExcludedApps, s.CorrectOnUnknownApp)
	ac.corrector.SetOptions(ac.correctorOptions(s))
	ac.writer.Backends = buildBackends(s, vkbd, capsLock)
	if s.Enabled != oldEnabled {
		ac.corrector.SetEnabled(s.Enabled)
	}
	if dictionaryChanged {
		if ac.corrector.Enabled() {
			ac.startDictLoad(loadKeyFor(s))
		} else {
			ac.invalidateDictionary()
		}
	} else if ac.corrector.Enabled() {
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
	dictStatus := ac.dictionaryStatus()
	st := control.Status{
		Enabled:   ac.corrector.Enabled(),
		DictState: dictStatus.State,
		DictError: dictStatus.Error,
	}
	if ix := ac.index.Load(); ix != nil && dictStatus.State == dictionaryReady {
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
