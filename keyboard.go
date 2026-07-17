package main

import (
	"fmt"

	evdev "github.com/holoplot/go-evdev"

	"github.com/andresousadotpt/texpand/internal/inputstate"
)

// KeyEvent carries a key code and value (1=press, 0=release, 2=repeat)
// from a keyboard monitoring goroutine.
type KeyEvent struct {
	Device    string
	Source    *evdev.InputDevice
	Code      evdev.EvCode
	Value     int32
	Modifiers inputstate.Modifiers
}

// isMonitorableKeyboard decides whether a device with the given name and
// key capabilities should be monitored. A keyboard must be able to produce
// KEY_A and KEY_ENTER; the daemon's own virtual keyboard is skipped so its
// generated events can never re-enter the pipeline.
func isMonitorableKeyboard(name string, keyCodes []evdev.EvCode) bool {
	if name == VirtualKeyboardName {
		return false
	}
	hasA := false
	hasEnter := false
	for _, c := range keyCodes {
		if c == evdev.KEY_A {
			hasA = true
		}
		if c == evdev.KEY_ENTER {
			hasEnter = true
		}
	}
	return hasA && hasEnter
}

// VirtualKeyboardName is the uinput device name texpand creates; devices
// carrying it are never monitored (feedback-loop prevention).
const VirtualKeyboardName = "texpand"

// FindKeyboards enumerates /dev/input/ devices and returns those that
// have both KEY_A and KEY_ENTER capabilities (i.e., physical keyboards).
func FindKeyboards() ([]*evdev.InputDevice, error) {
	paths, err := evdev.ListDevicePaths()
	if err != nil {
		return nil, fmt.Errorf("list input devices: %w", err)
	}

	var kbds []*evdev.InputDevice
	for _, p := range paths {
		dev, err := evdev.Open(p.Path)
		if err != nil {
			continue
		}

		name, _ := dev.Name()
		if isMonitorableKeyboard(name, dev.CapableEvents(evdev.EV_KEY)) {
			kbds = append(kbds, dev)
		} else {
			dev.Close()
		}
	}

	return kbds, nil
}

// capsLockOn reads the Caps Lock LED from the first keyboard that reports
// one. Best effort: keyboards without the LED yield false.
func capsLockOn(devs []*evdev.InputDevice) bool {
	for _, dev := range devs {
		st, err := dev.State(evdev.EV_LED)
		if err != nil {
			continue
		}
		if on, ok := st[evdev.LED_CAPSL]; ok && on {
			return true
		}
	}
	return false
}

// capsLockOnMonitors is capsLockOn over the currently monitored devices.
func capsLockOnMonitors(monitors map[string]monitoredKeyboard) bool {
	devs := make([]*evdev.InputDevice, 0, len(monitors))
	for _, mon := range monitors {
		devs = append(devs, mon.dev)
	}
	return capsLockOn(devs)
}

type monitoredKeyboard struct {
	dev  *evdev.InputDevice
	name string
}

// isCurrentKeyboardEvent rejects events queued by a monitor that has already
// stopped. Comparing the device pointer as well as its path also covers an
// event node that was recreated at the same path with a new monitor.
func isCurrentKeyboardEvent(monitors map[string]monitoredKeyboard, ev KeyEvent) bool {
	mon, ok := monitors[ev.Device]
	return ok && mon.dev == ev.Source
}

type keyboardMonitorExit struct {
	path string
	dev  *evdev.InputDevice
}

func startKeyboardMonitor(monitors map[string]monitoredKeyboard, dev *evdev.InputDevice, ch chan<- KeyEvent, done chan<- keyboardMonitorExit) {
	path := dev.Path()
	name, _ := dev.Name()
	monitors[path] = monitoredKeyboard{dev: dev, name: name}
	go MonitorKeyboard(dev, ch, done)
}

// RefreshKeyboardMonitors reconciles running keyboard monitors with the
// currently available evdev keyboard devices. It starts monitors for new
// keyboards and closes monitors whose device nodes have disappeared.
func RefreshKeyboardMonitors(monitors map[string]monitoredKeyboard, ch chan<- KeyEvent, done chan<- keyboardMonitorExit) (bool, error) {
	keyboards, err := FindKeyboards()
	if err != nil {
		return false, err
	}

	changed := false
	seen := make(map[string]bool, len(keyboards))
	for _, kb := range keyboards {
		path := kb.Path()
		seen[path] = true
		if _, ok := monitors[path]; ok {
			kb.Close()
			continue
		}

		name, _ := kb.Name()
		fmt.Printf("texpand: keyboard connected: %s\n", name)
		startKeyboardMonitor(monitors, kb, ch, done)
		changed = true
	}

	for path, mon := range monitors {
		if seen[path] {
			continue
		}
		fmt.Printf("texpand: keyboard removed: %s\n", mon.name)
		mon.dev.Close()
		delete(monitors, path)
		changed = true
	}

	return changed, nil
}

// MonitorKeyboard reads events from a single keyboard device and sends key
// events on the channel. It exits when the device is closed or errors, and
// reports the stopped device path so the main loop can rescan hotplugged
// keyboards.
func MonitorKeyboard(dev *evdev.InputDevice, ch chan<- KeyEvent, done chan<- keyboardMonitorExit) {
	path := dev.Path()
	name, _ := dev.Name()
	defer func() {
		done <- keyboardMonitorExit{path: path, dev: dev}
	}()
	for {
		ev, err := dev.ReadOne()
		if err != nil {
			dbg("keyboard monitor stopped: %s (%s): %v", name, path, err)
			return
		}
		if ev.Type == evdev.EV_KEY {
			ch <- KeyEvent{Device: path, Source: dev, Code: ev.Code, Value: ev.Value}
		}
	}
}
