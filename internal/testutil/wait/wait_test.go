package wait

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestUntilReturnsOnceConditionHolds(t *testing.T) {
	var n atomic.Int32
	go func() {
		time.Sleep(20 * time.Millisecond)
		n.Store(1)
	}()
	Until(t, 2*time.Second, "flag set", func() bool { return n.Load() == 1 })
}

// fakeTB records Fatalf instead of stopping the real test.
type fakeTB struct {
	testing.TB
	failed string
}

func (f *fakeTB) Helper() {}
func (f *fakeTB) Fatalf(format string, args ...any) {
	f.failed = format
	panic(f)
}

func TestForFailsWithLastObservation(t *testing.T) {
	f := &fakeTB{TB: t}
	defer func() {
		if r := recover(); r != f {
			t.Fatalf("expected the fake Fatalf, got %v", r)
		}
		if f.failed == "" {
			t.Fatal("Fatalf not called")
		}
	}()
	For(f, 30*time.Millisecond, "never", func() (bool, any) { return false, "state=x" })
}

func TestTimeoutScales(t *testing.T) {
	if got := Timeout(t, time.Second); got < time.Second {
		t.Fatalf("Timeout shrank an unscaled deadline: %v", got)
	}
}
