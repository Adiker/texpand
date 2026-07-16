package correct

import (
	"testing"

	evdev "github.com/holoplot/go-evdev"
)

// BenchmarkOrdinaryKey measures the per-event cost of the normal typing
// path (letter press + release). This is the latency-critical path: it
// must not touch the dictionary, allocate, or do I/O.
func BenchmarkOrdinaryKey(b *testing.B) {
	c := New(DefaultOptions())
	c.SetLookup(testLookup())
	press := KeyEvent{Code: evdev.KEY_A, Value: 1}
	release := KeyEvent{Code: evdev.KEY_A, Value: 0}
	bs := KeyEvent{Code: evdev.KEY_BACKSPACE, Value: 1}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		c.HandleEvent(press)
		c.HandleEvent(release)
		if i%8 == 7 {
			c.HandleEvent(bs) // keep the buffer bounded without a boundary
		}
	}
}

// BenchmarkWordCommit measures a full word-boundary commit including the
// dictionary decision (against the tiny in-memory fake; see the dict
// package benchmarks for real-dictionary lookup costs).
func BenchmarkWordCommit(b *testing.B) {
	c := New(DefaultOptions())
	c.SetLookup(testLookup())
	word := []KeyEvent{
		{Code: evdev.KEY_Z, Value: 1}, {Code: evdev.KEY_Z, Value: 0},
		{Code: evdev.KEY_O, Value: 1}, {Code: evdev.KEY_O, Value: 0},
		{Code: evdev.KEY_L, Value: 1}, {Code: evdev.KEY_L, Value: 0},
		{Code: evdev.KEY_W, Value: 1}, {Code: evdev.KEY_W, Value: 0},
		{Code: evdev.KEY_SPACE, Value: 1}, {Code: evdev.KEY_SPACE, Value: 0},
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		for _, ev := range word {
			c.HandleEvent(ev)
		}
	}
}
