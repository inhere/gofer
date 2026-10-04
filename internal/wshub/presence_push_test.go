package wshub

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/inhere/gofer/internal/pushhub"
)

// TestRunnersInvalOnWorkerConnect wires the presence observer to a push hub the way
// serve does and checks that a worker coming online, and then going away, each reach
// a `runners` subscriber as an invalidation.
func TestRunnersInvalOnWorkerConnect(t *testing.T) {
	hub := New(map[string]string{"w1": "w1"})
	ph := pushhub.New(pushhub.Options{InvalInterval: 10 * time.Millisecond})
	hub.SetPresenceObserver(func(string, bool) { ph.Notify(pushhub.TopicRunners) })

	browser, err := ph.Register("alice")
	if err != nil {
		t.Fatal(err)
	}
	defer browser.Close()
	browser.Subscribe([]string{pushhub.TopicRunners}, nil)

	want := func(label string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		for {
			b, err := browser.Next(ctx)
			if err != nil {
				t.Fatalf("%s: no runners inval: %v", label, err)
			}
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			if m["t"] == "inval" && m["topic"] == "runners" {
				return
			}
		}
	}

	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, reg := dialAndRegister(t, ctx, wsURL, "w1")
	if !reg.Accepted {
		t.Fatalf("not accepted: %+v", reg)
	}
	want("connect")
	conn.Close(websocket.StatusNormalClosure, "bye")
	want("disconnect")
}
