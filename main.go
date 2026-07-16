package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/bendahl/uinput"
	"github.com/fsnotify/fsnotify"
	evdev "github.com/holoplot/go-evdev"

	"github.com/andresousadotpt/texpand/internal/control"
	"github.com/andresousadotpt/texpand/internal/correct"
)

var (
	version     = "dev"
	debugLog    bool
	debugUnsafe bool
)

func init() {
	if version == "dev" {
		if info, ok := debug.ReadBuildInfo(); ok {
			v := info.Main.Version
			if v != "" && v != "(devel)" && !strings.Contains(v, "+dirty") {
				version = strings.TrimPrefix(v, "v")
			}
		}
	}
}

func dbg(format string, args ...any) {
	if debugLog {
		fmt.Fprintf(os.Stderr, "texpand [DEBUG] "+format+"\n", args...)
	}
}

// dbgUnsafe logs lines that can contain typed text. --debug alone never
// prints captured words; this requires the explicit --debug-unsafe flag.
func dbgUnsafe(format string, args ...any) {
	if debugUnsafe {
		fmt.Fprintf(os.Stderr, "texpand [DEBUG-UNSAFE] "+format+"\n", args...)
	}
}

func ensureWaylandEnv() {
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		return
	}
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = fmt.Sprintf("/run/user/%d", os.Getuid())
	}
	entries, err := os.ReadDir(runtimeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "texpand: WARNING: WAYLAND_DISPLAY not set and could not auto-detect\n")
		return
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "wayland-") && !strings.HasSuffix(name, ".lock") {
			os.Setenv("WAYLAND_DISPLAY", name)
			fmt.Printf("texpand: auto-detected %s\n", name)
			return
		}
	}
	fmt.Fprintf(os.Stderr, "texpand: WARNING: WAYLAND_DISPLAY not set and could not auto-detect\n")
}

func configDir() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "texpand")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "texpand")
}

func run() error {
	ensureWaylandEnv()

	dir := configDir()
	dbg("config directory: %s", dir)

	appCfg, err := LoadAppConfig(dir)
	if err != nil {
		return fmt.Errorf("load app config: %w", err)
	}
	dbg("trigger_mode: %q", appCfg.TriggerMode)

	cfg, err := LoadConfig(dir, appCfg)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	for _, m := range cfg.Matches {
		dbg("  trigger=%q replace=%q", m.Trigger, m.Replace)
	}

	// Retry device initialization — at boot, /dev/uinput and keyboard
	// devices may not be available yet (module not loaded, udev rules
	// not applied). Retry with backoff for up to ~30 seconds.
	const maxRetries = 10
	var keyboards []*evdev.InputDevice
	var vkbd uinput.Keyboard
	for attempt := 1; attempt <= maxRetries; attempt++ {
		keyboards, err = FindKeyboards()
		if err != nil {
			if attempt == maxRetries {
				return fmt.Errorf("find keyboards: %w", err)
			}
			fmt.Fprintf(os.Stderr, "texpand: waiting for keyboard devices (attempt %d/%d): %v\n", attempt, maxRetries, err)
			time.Sleep(time.Duration(attempt) * time.Second)
			continue
		}
		if len(keyboards) == 0 {
			if attempt == maxRetries {
				return fmt.Errorf("no keyboard devices found\nMake sure you are in the 'input' group:\n  sudo usermod -aG input $USER\nThen log out and back in")
			}
			fmt.Fprintf(os.Stderr, "texpand: no keyboards found yet (attempt %d/%d), retrying...\n", attempt, maxRetries)
			time.Sleep(time.Duration(attempt) * time.Second)
			continue
		}

		vkbd, err = uinput.CreateKeyboard("/dev/uinput", []byte(VirtualKeyboardName))
		if err != nil {
			// Close any keyboards we opened before retrying
			for _, kb := range keyboards {
				kb.Close()
			}
			keyboards = nil
			if attempt == maxRetries {
				return fmt.Errorf("create virtual keyboard: %w", err)
			}
			fmt.Fprintf(os.Stderr, "texpand: /dev/uinput not ready (attempt %d/%d): %v\n", attempt, maxRetries, err)
			time.Sleep(time.Duration(attempt) * time.Second)
			continue
		}
		break
	}
	defer vkbd.Close()

	ch := make(chan KeyEvent, 64)
	keyboardDone := make(chan keyboardMonitorExit, 64)
	expander := NewExpander(cfg, vkbd)

	acSettings, err := appCfg.Autocorrect.Normalized()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	ac := newAutocorrect(acSettings, vkbd)
	ac.startControl()
	defer ac.shutdown()
	ac.corrector.SetCapsLock(capsLockOn(keyboards))
	if ac.corrector.Enabled() {
		ac.maybeStartDictLoad()
		fmt.Println("texpand: autocorrect enabled — loading Polish dictionary in background")
	} else {
		fmt.Println("texpand: autocorrect disabled (enable with: texpand autocorrect enable)")
	}

	fmt.Printf("texpand: monitoring %d keyboard(s) — %d triggers loaded\n",
		len(keyboards), len(cfg.Matches))
	for _, kb := range keyboards {
		name, _ := kb.Name()
		fmt.Printf("  %s\n", name)
	}

	// Watch config directory for changes
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("create file watcher: %w", err)
	}
	defer watcher.Close()

	if err := watcher.Add(dir); err != nil {
		fmt.Fprintf(os.Stderr, "texpand: WARNING: could not watch %s: %v\n", dir, err)
	}
	matchDir := filepath.Join(dir, "match")
	if err := watcher.Add(matchDir); err != nil {
		fmt.Fprintf(os.Stderr, "texpand: WARNING: could not watch %s: %v\n", matchDir, err)
	}
	if err := watcher.Add("/dev/input"); err != nil {
		fmt.Fprintf(os.Stderr, "texpand: WARNING: could not watch /dev/input for keyboard hotplug: %v\n", err)
	}

	keyboardMonitors := make(map[string]monitoredKeyboard, len(keyboards))
	for _, kb := range keyboards {
		startKeyboardMonitor(keyboardMonitors, kb, ch, keyboardDone)
	}

	// Clean shutdown on SIGINT/SIGTERM
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	configDebounce := newStoppedTimer()
	keyboardDebounce := newStoppedTimer()
	keyboardRescan := time.NewTicker(5 * time.Second)
	defer keyboardRescan.Stop()

	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return nil
			}
			if expander.HandleEvent(ev) {
				// The expander rewrote text: the corrector's view of the
				// screen is stale.
				ac.corrector.Invalidate()
				// Brief pause after expansion to avoid processing
				// stale events from the physical keyboard.
				time.Sleep(5 * time.Millisecond)
				drainEvents(ch)
				continue
			}
			res := ac.corrector.HandleEvent(correct.KeyEvent{Code: ev.Code, Value: ev.Value})
			if res.Toggled {
				enabled := !ac.corrector.Enabled()
				ac.corrector.SetEnabled(enabled)
				if enabled {
					ac.maybeStartDictLoad()
				}
				ac.notifyToggle(enabled)
				fmt.Printf("texpand: autocorrect %s (keyboard shortcut)\n", onOff(enabled))
			}
			if res.Plan != nil {
				dbgUnsafe("correction: -%d chars, +%q (undo=%v)", res.Plan.Backspaces, res.Plan.Type, res.Undo)
				if err := ac.writer.Apply(res.Plan.Backspaces, res.Plan.Type); err != nil {
					fmt.Fprintf(os.Stderr, "texpand: correction output failed: %v\n", err)
				}
				// Our own uinput echo is invisible here (the virtual
				// device is never monitored), but physical events queued
				// while we typed are stale.
				expander.ResetInputState()
				time.Sleep(5 * time.Millisecond)
				drainEvents(ch)
			}
		case ix := <-ac.dictCh:
			ac.corrector.SetLookup(ix)
			words, cands := ix.Stats()
			fmt.Printf("texpand: Polish dictionary ready — %d word forms, %d correction candidates\n", words, cands)
		case <-ac.loadReq:
			ac.maybeStartDictLoad()
		case stopped := <-keyboardDone:
			if mon, ok := keyboardMonitors[stopped.path]; ok && mon.dev == stopped.dev {
				mon.dev.Close()
				delete(keyboardMonitors, stopped.path)
				expander.ResetInputState()
				ac.corrector.Reset()
				ac.corrector.SetCapsLock(capsLockOnMonitors(keyboardMonitors))
				fmt.Printf("texpand: keyboard disconnected: %s\n", mon.name)
			}
			resetTimer(keyboardDebounce, 500*time.Millisecond)
		case <-keyboardDebounce.C:
			changed, err := RefreshKeyboardMonitors(keyboardMonitors, ch, keyboardDone)
			if err != nil {
				fmt.Fprintf(os.Stderr, "texpand: keyboard rescan error: %v\n", err)
				continue
			}
			if changed {
				expander.ResetInputState()
				ac.corrector.Reset()
				ac.corrector.SetCapsLock(capsLockOnMonitors(keyboardMonitors))
				fmt.Printf("texpand: monitoring %d keyboard(s)\n", len(keyboardMonitors))
			}
		case <-keyboardRescan.C:
			changed, err := RefreshKeyboardMonitors(keyboardMonitors, ch, keyboardDone)
			if err != nil {
				dbg("keyboard rescan error: %v", err)
				continue
			}
			if changed {
				expander.ResetInputState()
				ac.corrector.Reset()
				ac.corrector.SetCapsLock(capsLockOnMonitors(keyboardMonitors))
				fmt.Printf("texpand: monitoring %d keyboard(s)\n", len(keyboardMonitors))
			}
		case <-configDebounce.C:
			newAppCfg, err := LoadAppConfig(dir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "texpand: reload error: %v\n", err)
				continue
			}
			newCfg, err := LoadConfig(dir, newAppCfg)
			if err != nil {
				fmt.Fprintf(os.Stderr, "texpand: reload error: %v\n", err)
				continue
			}
			expander.Reload(newCfg)
			if newSettings, err := newAppCfg.Autocorrect.Normalized(); err != nil {
				fmt.Fprintf(os.Stderr, "texpand: reload error (autocorrect settings kept): %v\n", err)
			} else {
				ac.applySettings(newSettings, vkbd)
			}
			fmt.Printf("texpand: config reloaded — %d triggers loaded\n", len(newCfg.Matches))
		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if isRelevantChange(event) {
				dbg("config change detected: %s %s", event.Op, event.Name)
				resetTimer(configDebounce, 500*time.Millisecond)
			}
			if isInputDeviceChange(event) {
				dbg("input device change detected: %s %s", event.Op, event.Name)
				resetTimer(keyboardDebounce, 500*time.Millisecond)
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			fmt.Fprintf(os.Stderr, "texpand: watch error: %v\n", err)
		case <-sigCh:
			fmt.Println("\ntexpand: shutting down")
			for _, mon := range keyboardMonitors {
				mon.dev.Close()
			}
			return nil
		}
	}
}

// drainEvents empties queued key events after we injected our own output,
// so stale physical events cannot re-trigger a match.
func drainEvents(ch <-chan KeyEvent) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

func onOff(v bool) string {
	if v {
		return "enabled"
	}
	return "disabled"
}

func newStoppedTimer() *time.Timer {
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	return timer
}

func resetTimer(timer *time.Timer, d time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(d)
}

// isRelevantChange returns true if the fsnotify event represents a
// write/create/remove of a .yml file (config or match file change).
func isRelevantChange(event fsnotify.Event) bool {
	if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove) == 0 {
		return false
	}
	return strings.HasSuffix(event.Name, ".yml")
}

func isInputDeviceChange(event fsnotify.Event) bool {
	if event.Op&(fsnotify.Create|fsnotify.Remove|fsnotify.Rename|fsnotify.Chmod) == 0 {
		return false
	}
	return filepath.Dir(event.Name) == "/dev/input" && strings.HasPrefix(filepath.Base(event.Name), "event")
}

func main() {
	args := os.Args[1:]

	// Parse flags
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "--debug", "-d":
			debugLog = true
		case "--debug-unsafe":
			debugLog = true
			debugUnsafe = true
		default:
			fmt.Fprintf(os.Stderr, "unknown flag: %s\n", args[0])
			fmt.Fprintf(os.Stderr, "usage: texpand [--debug] [init|version|migrate|autocorrect <cmd>]\n")
			os.Exit(1)
		}
		args = args[1:]
	}

	if len(args) > 0 {
		switch args[0] {
		case "autocorrect":
			if len(args) < 2 {
				fmt.Fprintf(os.Stderr, "usage: texpand autocorrect enable|disable|toggle|status\n")
				os.Exit(1)
			}
			out, err := control.ClientCommand(args[1])
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			fmt.Println(out)
			return
		case "init":
			dir := configDir()
			fmt.Printf("texpand: initializing config in %s\n", dir)
			if err := initConfig(dir); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("texpand: config initialized")
			return
		case "version":
			fmt.Printf("texpand %s\n", version)
			return
		case "migrate":
			dir := configDir()
			if err := migrateConfig(dir); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			return
		default:
			fmt.Fprintf(os.Stderr, "usage: texpand [--debug] [init|version|migrate|autocorrect <cmd>]\n")
			os.Exit(1)
		}
	}

	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "texpand: %v\n", err)
		os.Exit(1)
	}
}
