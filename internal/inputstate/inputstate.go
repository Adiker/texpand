// Package inputstate aggregates modifier and Caps Lock state across all
// monitored physical keyboards.
package inputstate

import evdev "github.com/holoplot/go-evdev"

// Modifiers is the effective compositor-visible keyboard state.
type Modifiers struct {
	Shift bool
	Ctrl  bool
	Alt   bool
	AltGr bool
	Meta  bool
	Caps  bool
}

type pressedKey uint16

const (
	keyLeftShift pressedKey = 1 << iota
	keyRightShift
	keyLeftCtrl
	keyRightCtrl
	keyLeftAlt
	keyRightAlt
	keyLeftMeta
	keyRightMeta
)

// Tracker keeps pressed modifier bits per evdev device. This prevents a
// release from one keyboard (or one side of a modifier pair) from clearing a
// modifier that remains held elsewhere.
type Tracker struct {
	devices map[string]pressedKey
	counts  [5]uint16
	caps    bool
}

// New creates a tracker seeded with the current Caps Lock LED state.
func New(caps bool) *Tracker {
	return &Tracker{devices: make(map[string]pressedKey), caps: caps}
}

func keyFor(code evdev.EvCode) (pressedKey, int) {
	switch code {
	case evdev.KEY_LEFTSHIFT:
		return keyLeftShift, 0
	case evdev.KEY_RIGHTSHIFT:
		return keyRightShift, 0
	case evdev.KEY_LEFTCTRL:
		return keyLeftCtrl, 1
	case evdev.KEY_RIGHTCTRL:
		return keyRightCtrl, 1
	case evdev.KEY_LEFTALT:
		return keyLeftAlt, 2
	case evdev.KEY_RIGHTALT:
		return keyRightAlt, 3
	case evdev.KEY_LEFTMETA:
		return keyLeftMeta, 4
	case evdev.KEY_RIGHTMETA:
		return keyRightMeta, 4
	default:
		return 0, -1
	}
}

// Handle records one raw event and returns the new effective state. Repeat
// events do not alter pressed state.
func (t *Tracker) Handle(device string, code evdev.EvCode, value int32) Modifiers {
	if code == evdev.KEY_CAPSLOCK && value == 1 {
		t.caps = !t.caps
	}
	bit, idx := keyFor(code)
	if bit == 0 || value == 2 {
		return t.Snapshot()
	}
	current := t.devices[device]
	pressed := current&bit != 0
	wantPressed := value > 0
	if pressed == wantPressed {
		return t.Snapshot()
	}
	if wantPressed {
		t.devices[device] = current | bit
		t.counts[idx]++
	} else {
		next := current &^ bit
		if next == 0 {
			delete(t.devices, device)
		} else {
			t.devices[device] = next
		}
		if t.counts[idx] > 0 {
			t.counts[idx]--
		}
	}
	return t.Snapshot()
}

// Remove forgets every pressed modifier reported by a disconnected device.
func (t *Tracker) Remove(device string) Modifiers {
	bits, ok := t.devices[device]
	if !ok {
		return t.Snapshot()
	}
	delete(t.devices, device)
	for bit := keyLeftShift; bit <= keyRightMeta; bit <<= 1 {
		if bits&bit == 0 {
			continue
		}
		idx := 0
		switch bit {
		case keyLeftCtrl, keyRightCtrl:
			idx = 1
		case keyLeftAlt:
			idx = 2
		case keyRightAlt:
			idx = 3
		case keyLeftMeta, keyRightMeta:
			idx = 4
		}
		if t.counts[idx] > 0 {
			t.counts[idx]--
		}
	}
	return t.Snapshot()
}

// Reset clears all held modifiers and reseeds Caps Lock after a hotplug
// reconciliation.
func (t *Tracker) Reset(caps bool) Modifiers {
	clear(t.devices)
	t.counts = [5]uint16{}
	t.caps = caps
	return t.Snapshot()
}

// SetCaps sets the Caps Lock state from an evdev LED query.
func (t *Tracker) SetCaps(caps bool) { t.caps = caps }

// Snapshot returns the current effective state.
func (t *Tracker) Snapshot() Modifiers {
	return Modifiers{
		Shift: t.counts[0] > 0,
		Ctrl:  t.counts[1] > 0,
		Alt:   t.counts[2] > 0,
		AltGr: t.counts[3] > 0,
		Meta:  t.counts[4] > 0,
		Caps:  t.caps,
	}
}
