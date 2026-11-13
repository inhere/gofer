package wshub

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type replaceSpy struct {
	mu   sync.Mutex
	seen []ReplaceInfo
}

func (s *replaceSpy) add(i ReplaceInfo) { s.mu.Lock(); s.seen = append(s.seen, i); s.mu.Unlock() }
func (s *replaceSpy) all() []ReplaceInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ReplaceInfo(nil), s.seen...)
}

// TestReplaceObserverFiresOnlyForDifferentInstance: a registration that replaces a
// live connection reports it when the instance_id differs (two processes), and
// stays silent when it is the same process reconnecting.
func TestReplaceObserverFiresOnlyForDifferentInstance(t *testing.T) {
	hub := shortHeartbeat(map[string]string{"w1": "w1"}, time.Second, 10*time.Second)
	spy := &replaceSpy{}
	hub.SetReplaceObserver(spy.add)
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	c1, _ := dialAndRegisterInstance(t, ctx, wsURL, "w1", "inst-a")
	defer c1.Close(websocket.StatusNormalClosure, "done")
	waitWorkerOnline(t, hub, "w1")

	// Same instance_id: a transient reconnect, not a takeover.
	c2, _ := dialAndRegisterInstance(t, ctx, wsURL, "w1", "inst-a")
	defer c2.Close(websocket.StatusNormalClosure, "done")
	time.Sleep(100 * time.Millisecond) // nothing to wait for: assert absence
	if got := spy.all(); len(got) != 0 {
		t.Fatalf("same instance_id reported as replaced: %+v", got)
	}

	// Different instance_id: another process took the worker_id over.
	c3, _ := dialAndRegisterInstance(t, ctx, wsURL, "w1", "inst-b")
	defer c3.Close(websocket.StatusNormalClosure, "done")
	waitFor(t, func() bool { return len(spy.all()) == 1 })
	got := spy.all()[0]
	if got.WorkerID != "w1" || got.OldInstance != "inst-a" || got.NewInstance != "inst-b" || got.NewRemote == "" {
		t.Fatalf("replace info = %+v", got)
	}
}
