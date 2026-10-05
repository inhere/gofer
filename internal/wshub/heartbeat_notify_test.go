package wshub

import (
	"sync/atomic"
	"testing"
)

// TestNoteBeatThrottled: a worker's frames reach the heartbeat observer at most once
// per BeatNotifyInterval, each worker on its own clock, and a nil observer is inert.
func TestNoteBeatThrottled(t *testing.T) {
	hub := New(map[string]string{"w1": "w1", "w2": "w2"})
	w1 := &workerConn{workerID: "w1"}
	w2 := &workerConn{workerID: "w2"}

	hub.noteBeat(w1, 100) // no observer: must not panic nor consume the throttle slot

	var n1, n2 atomic.Int32
	hub.SetHeartbeatObserver(func(id string) {
		if id == "w1" {
			n1.Add(1)
		} else {
			n2.Add(1)
		}
	})
	step := int64(BeatNotifyInterval.Seconds())
	hub.noteBeat(w1, 100) // fires
	hub.noteBeat(w1, 100+step-1)
	hub.noteBeat(w1, 101) // within the window: swallowed
	if got := n1.Load(); got != 1 {
		t.Fatalf("w1 within window: %d calls, want 1", got)
	}
	hub.noteBeat(w2, 101) // other worker has its own window
	if got := n2.Load(); got != 1 {
		t.Fatalf("w2 first beat: %d calls, want 1", got)
	}
	hub.noteBeat(w1, 100+step) // window elapsed
	if got := n1.Load(); got != 2 {
		t.Fatalf("w1 after window: %d calls, want 2", got)
	}
	hub.SetHeartbeatObserver(nil)
	hub.noteBeat(w1, 100+10*step)
	if got := n1.Load(); got != 2 {
		t.Fatalf("cleared observer still called: %d", got)
	}
}
