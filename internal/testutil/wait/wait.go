// Package wait gives tests load-tolerant waits (gofer-r7am, design
// docs/design/2026-10-10-flaky-tests-root-cause.md §2.4).
//
// A test that waits for something asynchronous polls a condition until a deadline.
// Fixed deadlines tuned on an idle machine fail under a full -race run or on a busy
// Windows CI runner, so every deadline here is multiplied by Scale() (env
// GOFER_TEST_TIMEOUT_SCALE, default 1) and capped by the test binary's own -timeout.
// New waits use For / Until; the older per-package waitFor helpers migrate as their
// tests are touched.
package wait

import (
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

// EnvScale names the deadline multiplier, e.g. 3 on a slow CI runner.
const EnvScale = "GOFER_TEST_TIMEOUT_SCALE"

// deadlineMargin is left before the test binary's -timeout so a wait fails with its
// own message instead of the binary's panic dump.
const deadlineMargin = 5 * time.Second

var (
	scaleOnce sync.Once
	scale     = 1.0
)

// Scale returns the deadline multiplier from GOFER_TEST_TIMEOUT_SCALE (≥ 1; an unset,
// unparsable or smaller value means 1).
func Scale() float64 {
	scaleOnce.Do(func() {
		if v, err := strconv.ParseFloat(os.Getenv(EnvScale), 64); err == nil && v > 1 {
			scale = v
		}
	})
	return scale
}

// Timeout scales d and caps it so it ends before the test's own deadline (minus a
// margin). Use it where a test hands a timeout to the code under test.
func Timeout(t testing.TB, d time.Duration) time.Duration {
	t.Helper()
	d = time.Duration(float64(d) * Scale())
	if dt, ok := t.(interface{ Deadline() (time.Time, bool) }); ok {
		if end, ok := dt.Deadline(); ok {
			if left := time.Until(end) - deadlineMargin; left > 0 && left < d {
				d = left
			}
		}
	}
	return d
}

// For polls cond until it reports true or the scaled deadline passes; then it fails
// the test with what and the last value cond observed. cond returns (done, observed).
func For(t testing.TB, d time.Duration, what string, cond func() (bool, any)) {
	t.Helper()
	limit := Timeout(t, d)
	end := time.Now().Add(limit)
	interval := 5 * time.Millisecond
	var last any
	for {
		done, observed := cond()
		if done {
			return
		}
		last = observed
		if time.Now().After(end) {
			t.Fatalf("wait: %s: not reached within %v (scale %.1f); last observed: %s", what, limit, Scale(), describe(last))
		}
		time.Sleep(interval)
		if interval < 100*time.Millisecond {
			interval *= 2
		}
	}
}

// Until is For for a plain boolean condition.
func Until(t testing.TB, d time.Duration, what string, cond func() bool) {
	t.Helper()
	For(t, d, what, func() (bool, any) { return cond(), nil })
}

func describe(v any) string {
	if v == nil {
		return "(none)"
	}
	return fmt.Sprintf("%v", v)
}
