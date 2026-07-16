package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/andresousadotpt/texpand/internal/dict"
)

type lifecycleKeyboard struct{}

func (lifecycleKeyboard) KeyDown(int) error  { return nil }
func (lifecycleKeyboard) KeyUp(int) error    { return nil }
func (lifecycleKeyboard) KeyPress(int) error { return nil }

type loaderResponse struct {
	index *dict.Index
	err   error
}

type loaderCall struct {
	settings AutocorrectSettings
	response chan loaderResponse
}

func controlledLoader() (dictionaryLoader, <-chan loaderCall) {
	calls := make(chan loaderCall, 8)
	loader := func(settings AutocorrectSettings) (*dict.Index, error) {
		call := loaderCall{settings: settings, response: make(chan loaderResponse, 1)}
		calls <- call
		response := <-call.response
		return response.index, response.err
	}
	return loader, calls
}

func waitLoaderCall(t *testing.T, calls <-chan loaderCall) loaderCall {
	t.Helper()
	select {
	case call := <-calls:
		return call
	case <-time.After(time.Second):
		t.Fatal("dictionary loader was not called")
		return loaderCall{}
	}
}

func waitLoadResult(t *testing.T, ac *autocorrect) dictionaryLoadResult {
	t.Helper()
	select {
	case result := <-ac.dictCh:
		return result
	case <-time.After(time.Second):
		t.Fatal("dictionary load result was not delivered")
		return dictionaryLoadResult{}
	}
}

func lifecycleSettings(t *testing.T) AutocorrectSettings {
	t.Helper()
	var cfg AutocorrectConfig
	settings, err := cfg.Normalized()
	if err != nil {
		t.Fatal(err)
	}
	settings.Output = "uinput"
	return settings
}

func buildLifecycleIndex(t *testing.T, words ...string) *dict.Index {
	t.Helper()
	dir := t.TempDir()
	dicPath := filepath.Join(dir, "test.dic")
	affPath := filepath.Join(dir, "test.aff")
	dic := []byte("0\n")
	for _, word := range words {
		dic = append(dic, []byte(word+"\n")...)
	}
	if err := os.WriteFile(dicPath, dic, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(affPath, []byte("SET UTF-8\n"), 0600); err != nil {
		t.Fatal(err)
	}
	index, err := dict.Build(dicPath, affPath)
	if err != nil {
		t.Fatal(err)
	}
	return index
}

func TestDictionaryReloadIgnoresStaleGeneration(t *testing.T) {
	settings := lifecycleSettings(t)
	settings.Dictionary = "first.dic"
	keyboard := lifecycleKeyboard{}
	capsLock := func() bool { return false }
	ac := newAutocorrect(settings, keyboard, capsLock)
	loader, calls := controlledLoader()
	ac.loader = loader

	ac.maybeStartDictLoad()
	first := waitLoaderCall(t, calls)
	if got := ac.dictionaryStatus().State; got != dictionaryLoading {
		t.Fatalf("state = %q, want loading", got)
	}

	next := settings
	next.Dictionary = "second.dic"
	ac.applySettings(next, keyboard, capsLock)
	second := waitLoaderCall(t, calls)
	if second.settings.Dictionary != "second.dic" {
		t.Fatalf("second loader used %q", second.settings.Dictionary)
	}

	secondIndex := buildLifecycleIndex(t, "żółw", "źródło")
	second.response <- loaderResponse{index: secondIndex}
	if current := waitLoadResult(t, ac); !ac.handleDictLoadResult(current) {
		t.Fatal("current generation was ignored")
	}
	if got := ac.index.Load(); got != secondIndex {
		t.Fatal("new dictionary index was not installed")
	}

	first.response <- loaderResponse{index: buildLifecycleIndex(t, "żółw")}
	if stale := waitLoadResult(t, ac); ac.handleDictLoadResult(stale) {
		t.Fatal("stale dictionary generation was applied")
	}
	if got := ac.index.Load(); got != secondIndex {
		t.Fatal("stale generation replaced the current index")
	}
}

func TestDictionaryFailureCanBeRetried(t *testing.T) {
	settings := lifecycleSettings(t)
	settings.Dictionary = "missing.dic"
	keyboard := lifecycleKeyboard{}
	ac := newAutocorrect(settings, keyboard, func() bool { return false })
	loader, calls := controlledLoader()
	ac.loader = loader

	ac.maybeStartDictLoad()
	failed := waitLoaderCall(t, calls)
	failed.response <- loaderResponse{err: errors.New("dictionary unavailable")}
	ac.handleDictLoadResult(waitLoadResult(t, ac))
	status := ac.AutocorrectStatus()
	if status.DictState != dictionaryFailed || status.DictError != "dictionary unavailable" || status.DictReady {
		t.Fatalf("failed status = %+v", status)
	}

	// `enable` on an already-enabled daemon is also the explicit retry action.
	ac.SetAutocorrectEnabled(true)
	select {
	case <-ac.loadReq:
		ac.maybeStartDictLoad()
	case <-time.After(time.Second):
		t.Fatal("enable did not request a retry")
	}
	retry := waitLoaderCall(t, calls)
	retryIndex := buildLifecycleIndex(t, "żółw")
	retry.response <- loaderResponse{index: retryIndex}
	ac.handleDictLoadResult(waitLoadResult(t, ac))
	status = ac.AutocorrectStatus()
	if status.DictState != dictionaryReady || !status.DictReady || status.DictError != "" {
		t.Fatalf("ready status after retry = %+v", status)
	}
}

func TestDictionaryChangeWhileDisabledInvalidatesOldIndex(t *testing.T) {
	settings := lifecycleSettings(t)
	keyboard := lifecycleKeyboard{}
	capsLock := func() bool { return false }
	ac := newAutocorrect(settings, keyboard, capsLock)
	index := buildLifecycleIndex(t, "żółw")
	ac.loadGeneration = 1
	ac.hasLoadKey = true
	ac.loadKey = loadKeyFor(settings)
	ac.handleDictLoadResult(dictionaryLoadResult{Generation: 1, Index: index})

	ac.SetAutocorrectEnabled(false)
	next := settings
	next.Dictionary = "replacement.dic"
	ac.applySettings(next, keyboard, capsLock)
	if got := ac.dictionaryStatus().State; got != dictionaryIdle {
		t.Fatalf("state = %q, want idle", got)
	}
	if ac.index.Load() != nil || ac.corrector.Ready() {
		t.Fatal("old dictionary remained attached after disabled config change")
	}
}

func TestCacheSettingChangeStartsNewGeneration(t *testing.T) {
	settings := lifecycleSettings(t)
	settings.Cache = true
	keyboard := lifecycleKeyboard{}
	capsLock := func() bool { return false }
	ac := newAutocorrect(settings, keyboard, capsLock)
	loader, calls := controlledLoader()
	ac.loader = loader

	ac.maybeStartDictLoad()
	first := waitLoaderCall(t, calls)
	first.response <- loaderResponse{index: buildLifecycleIndex(t, "żółw")}
	ac.handleDictLoadResult(waitLoadResult(t, ac))
	firstGeneration := ac.loadGeneration

	next := settings
	next.Cache = false
	ac.applySettings(next, keyboard, capsLock)
	second := waitLoaderCall(t, calls)
	if second.settings.Cache {
		t.Fatal("cache-disabled reload used the old cache setting")
	}
	if ac.loadGeneration <= firstGeneration {
		t.Fatal("cache setting change did not advance load generation")
	}
	second.response <- loaderResponse{index: buildLifecycleIndex(t, "źródło")}
	ac.handleDictLoadResult(waitLoadResult(t, ac))
}
