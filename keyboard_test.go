package main

import (
	"testing"

	evdev "github.com/holoplot/go-evdev"
)

func TestIsCurrentKeyboardEventRejectsStaleMonitor(t *testing.T) {
	const path = "/dev/input/event7"
	oldDevice := &evdev.InputDevice{}
	newDevice := &evdev.InputDevice{}
	monitors := map[string]monitoredKeyboard{
		path: {dev: newDevice, name: "replacement"},
	}

	if isCurrentKeyboardEvent(monitors, KeyEvent{Device: path, Source: oldDevice}) {
		t.Fatal("event from replaced monitor was accepted")
	}
	if !isCurrentKeyboardEvent(monitors, KeyEvent{Device: path, Source: newDevice}) {
		t.Fatal("event from current monitor was rejected")
	}

	delete(monitors, path)
	if isCurrentKeyboardEvent(monitors, KeyEvent{Device: path, Source: newDevice}) {
		t.Fatal("queued event from removed monitor was accepted")
	}
}
