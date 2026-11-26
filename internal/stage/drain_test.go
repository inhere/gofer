package stage

import (
	"context"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
)

// TestDrainWaitsForAnInFlightRemind: the remind turn runs in its own goroutine in production,
// so Drain must not return while one is still being delivered (it would journal into a
// closed store otherwise).
func TestDrainWaitsForAnInFlightRemind(t *testing.T) {
	f := newFix(t)
	started := make(chan struct{})
	release := make(chan struct{})
	f.svc = New(Deps{
		Store: f.store, Config: func() *config.Config { return f.cfg },
		Deliver: func(context.Context, Delivery) (string, error) {
			close(started)
			<-release
			return DeliveredNow, nil
		},
		Now: func() time.Time { return time.Unix(f.clock.Load(), 0) },
	})
	id := f.item(TemplateSmall)
	f.attach(id, "s-1")
	if _, err := f.svc.SetLevel(id, "remind", "human:a"); err != nil {
		t.Fatal(err)
	}
	if res, err := f.svc.Done(id, "session:s-1"); err != nil || !res.Advanced {
		t.Fatalf("done = %+v %v", res, err)
	}
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the remind turn never started")
	}
	drained := make(chan struct{})
	go func() { f.svc.Drain(); close(drained) }()
	select {
	case <-drained:
		t.Fatal("Drain returned while a remind turn was still being delivered")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	select {
	case <-drained:
	case <-time.After(10 * time.Second):
		t.Fatal("Drain did not return after the delivery finished")
	}
	if !f.journalHas(id, "stage", "sent to session s-1") {
		t.Fatal("the delivery outcome was not journaled before Drain returned")
	}
}
