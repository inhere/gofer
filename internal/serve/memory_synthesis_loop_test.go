package serve

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/memorysynthesis/engine"
	"github.com/inhere/gofer/internal/testutil/wait"
)

type countingTicker struct{ n atomic.Int32 }

func (c *countingTicker) Tick(context.Context) []engine.TickResult {
	c.n.Add(1)
	return []engine.TickResult{{Project: "p", Day: "2026-10-30", State: "succeeded"}}
}

// The loop ticks once at start (restart catch-up) and then on every interval; closing stop
// ends it and the returned wait only returns after the goroutine is gone.
func TestMemorySynthesisLoopTicksAndStops(t *testing.T) {
	cr, _ := buildGateCore(t)
	tk := &countingTicker{}
	stop := make(chan struct{})
	waitLoop := startMemorySynthesisLoop(gcli.NewCommand("t", ""), cr.Jobs, tk, time.Millisecond, stop)
	wait.Until(t, 10*time.Second, "loop ticked repeatedly", func() bool { return tk.n.Load() >= 3 })
	close(stop)
	done := make(chan struct{})
	go func() { waitLoop(); close(done) }()
	wait.Until(t, 10*time.Second, "loop stopped", func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	})
	n := tk.n.Load()
	if tk.n.Load() != n {
		t.Fatal("ticked after stop")
	}
}
