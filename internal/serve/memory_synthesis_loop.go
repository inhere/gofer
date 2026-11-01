package serve

import (
	"context"
	"sync"
	"time"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/memorysynthesis/engine"
)

// memorySynthesisTickEvery is how often the built-in daily synthesis trigger looks at the
// clock. A tick is a handful of indexed reads and does nothing off-schedule or when
// memory.synthesis.enabled is false, so a minute keeps the trigger within a minute of the
// configured time.
const memorySynthesisTickEvery = time.Minute

// synthesisTicker is the engine's built-in trigger (engine.Engine implements it).
type synthesisTicker interface {
	Tick(ctx context.Context) []engine.TickResult
}

// startMemorySynthesisLoop runs the daily memory synthesis trigger (design: the built-in
// loop, not a user-visible schedule) until stop is closed. It ticks once at start (a catch-up
// after a restart) and then every `every`. Each tick holds an upgrade-work permit like the
// schedule sweeper, so a managed upgrade drain never starts a model call. The returned
// function waits for the goroutine (a tick in flight is cancelled through its context).
func startMemorySynthesisLoop(c *gcli.Command, jobs *job.Service, tk synthesisTicker, every time.Duration, stop <-chan struct{}) (wait func()) {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()
		go func() {
			select {
			case <-stop:
				cancel()
			case <-ctx.Done():
			}
		}()
		tick := func() {
			permit, err := jobs.BeginUpgradeWork()
			if err != nil {
				return
			}
			defer permit.Release()
			for _, r := range tk.Tick(ctx) {
				switch {
				case r.Err != nil:
					c.Errorf("gofer: memory synthesis %s %s failed: %v\n", r.Project, r.Day, r.Err)
				case r.Run.ID != "":
					c.Printf("gofer: memory synthesis %s %s: %s (run %s)\n", r.Project, r.Day, r.State, r.Run.ID)
				default:
					c.Printf("gofer: memory synthesis %s %s: %s\n", r.Project, r.Day, r.State)
				}
			}
		}
		tick()
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				tick()
			}
		}
	}()
	return wg.Wait
}
