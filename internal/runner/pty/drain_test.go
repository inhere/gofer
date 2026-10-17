package ptyrunner

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/testutil/wait"
)

// lossyPty models a ConPTY: the child has already exited, but conhost forwards its
// last output late, and Close drops whatever has not been forwarded (gofer-663l).
type lossyPty struct {
	lag    time.Duration
	closed chan struct{}
	once   sync.Once
	step   int
}

func newLossyPty(lag time.Duration) *lossyPty {
	return &lossyPty{lag: lag, closed: make(chan struct{})}
}

func (p *lossyPty) Read(b []byte) (int, error) {
	p.step++
	if p.step == 1 {
		return copy(b, "init;"), nil
	}
	if p.step == 2 {
		select {
		case <-time.After(p.lag):
			return copy(b, "payload"), nil
		case <-p.closed:
			return 0, io.EOF
		}
	}
	<-p.closed
	return 0, io.EOF
}
func (p *lossyPty) Write(b []byte) (int, error) { return len(b), nil }
func (p *lossyPty) Resize(int, int) error       { return nil }
func (p *lossyPty) Close() error                { p.once.Do(func() { close(p.closed) }); return nil }
func (p *lossyPty) Wait(ctx context.Context) (int, error) {
	return 0, nil // the child is gone before its output is
}

func runLossy(t *testing.T, drainQuiet time.Duration) string {
	t.Helper()
	ps := newSession("drain-1", newLossyPty(150*time.Millisecond))
	ps.drainQuiet, ps.drainMax = drainQuiet, 3*time.Second
	var mu sync.Mutex
	var got []byte
	go func() {
		buf := make([]byte, 64)
		for {
			n, err := ps.Read(buf)
			mu.Lock()
			got = append(got, buf[:n]...)
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	if _, err := ps.run(context.Background()); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("run: %v", err)
	}
	var out string
	wait.Until(t, 2*time.Second, "reader to stop", func() bool {
		mu.Lock()
		defer mu.Unlock()
		out = string(got)
		return len(out) > 0
	})
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	return string(got)
}

// TestNaturalExitDrainsLateOutput: output the backend delivers after the child has
// exited must reach the reader — teardown may not close the master under it.
func TestNaturalExitDrainsLateOutput(t *testing.T) {
	if got := runLossy(t, 400*time.Millisecond); got != "init;payload" {
		t.Fatalf("late output lost: got %q", got)
	}
}

// TestNaturalExitWithoutDrainLosesLateOutput pins the failure model the drain
// exists for (and shows the test above is meaningful): with no drain the late
// output is dropped by Close.
func TestNaturalExitWithoutDrainLosesLateOutput(t *testing.T) {
	if got := runLossy(t, 0); got != "init;" {
		t.Fatalf("expected the undrained close to drop the payload, got %q", got)
	}
}
