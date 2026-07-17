package inputstate

import (
	"testing"

	evdev "github.com/holoplot/go-evdev"
)

func TestOverlappingModifiersAcrossDevices(t *testing.T) {
	tr := New(false)
	tr.Handle("kbd-a", evdev.KEY_LEFTSHIFT, 1)
	tr.Handle("kbd-b", evdev.KEY_RIGHTSHIFT, 1)
	if !tr.Snapshot().Shift {
		t.Fatal("shift not active after two presses")
	}
	tr.Handle("kbd-a", evdev.KEY_LEFTSHIFT, 0)
	if !tr.Snapshot().Shift {
		t.Fatal("one release cleared shift still held on another device")
	}
	tr.Handle("kbd-b", evdev.KEY_RIGHTSHIFT, 0)
	if tr.Snapshot().Shift {
		t.Fatal("shift remained active after both releases")
	}
}

func TestLeftAndRightModifierOnSameDevice(t *testing.T) {
	tr := New(false)
	tr.Handle("kbd", evdev.KEY_LEFTSHIFT, 1)
	tr.Handle("kbd", evdev.KEY_RIGHTSHIFT, 1)
	tr.Handle("kbd", evdev.KEY_LEFTSHIFT, 0)
	if !tr.Snapshot().Shift {
		t.Fatal("left release cleared right shift on the same device")
	}
	tr.Handle("kbd", evdev.KEY_RIGHTSHIFT, 0)
	if tr.Snapshot().Shift {
		t.Fatal("shift remained active after both sides released")
	}
}

func TestDisconnectRemovesOnlyOneDevicesState(t *testing.T) {
	tr := New(false)
	tr.Handle("kbd-a", evdev.KEY_LEFTCTRL, 1)
	tr.Handle("kbd-a", evdev.KEY_LEFTALT, 1)
	tr.Handle("kbd-b", evdev.KEY_RIGHTALT, 1)
	mods := tr.Remove("kbd-a")
	if mods.Ctrl || mods.Alt || !mods.AltGr {
		t.Fatalf("state after remove = %+v", mods)
	}
}

func TestRepeatDoesNotDoubleCountAndCapsTogglesOnce(t *testing.T) {
	tr := New(false)
	tr.Handle("kbd", evdev.KEY_LEFTMETA, 1)
	tr.Handle("kbd", evdev.KEY_LEFTMETA, 2)
	tr.Handle("kbd", evdev.KEY_LEFTMETA, 0)
	if tr.Snapshot().Meta {
		t.Fatal("repeat double-counted modifier")
	}
	tr.Handle("kbd", evdev.KEY_CAPSLOCK, 1)
	tr.Handle("kbd", evdev.KEY_CAPSLOCK, 2)
	if !tr.Snapshot().Caps {
		t.Fatal("caps did not toggle on press")
	}
	tr.Reset(false)
	if tr.Snapshot() != (Modifiers{}) {
		t.Fatalf("reset state = %+v", tr.Snapshot())
	}
}
