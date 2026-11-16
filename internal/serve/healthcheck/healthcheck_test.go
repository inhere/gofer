package healthcheck

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// stubProbe returns a probe whose outcome the test flips between runs.
type stubProbe struct{ err error }

func (s *stubProbe) probe() error { return s.err }

// TestRunnerNeedsConsecutiveFailures proves the fault is reported only on the threshold
// run (1-2 misses are noise), that a further failure does not raise it again, and that
// ONE success recovers immediately.
func TestRunnerNeedsConsecutiveFailures(t *testing.T) {
	stub := &stubProbe{err: errors.New("boom")}
	var failures, recoveries []string
	r := New(3, Check{Name: "x", Probe: stub.probe})
	r.OnTransition(func(res Result) { failures = append(failures, res.Name) },
		func(res Result) { recoveries = append(recoveries, res.Name) })

	r.RunOnce()
	r.RunOnce()
	if snap := r.Snapshot(); snap[0].Failing || snap[0].ConsecutiveFailures != 2 {
		t.Fatalf("after 2 failures = %+v, want not failing with count 2", snap[0])
	}
	if r.Degraded() || len(r.Failing()) != 0 {
		t.Fatal("2 failures must not degrade the server")
	}

	r.RunOnce()
	snap := r.Snapshot()[0]
	if !snap.Failing || snap.ConsecutiveFailures != 3 || !strings.Contains(snap.LastError, "boom") {
		t.Fatalf("third failure = %+v, want failing with count 3 and the error", snap)
	}
	if !r.Degraded() || len(r.Failing()) != 1 || r.Failing()[0] != "x" {
		t.Fatalf("third failure must degrade the server, failing = %v", r.Failing())
	}

	r.RunOnce() // still failing: no second announcement
	if len(failures) != 1 {
		t.Fatalf("failing callbacks = %v, want exactly one while the fault persists", failures)
	}

	stub.err = nil
	r.RunOnce()
	snap = r.Snapshot()[0]
	if !snap.OK || snap.Failing || snap.ConsecutiveFailures != 0 || snap.LastError != "" {
		t.Fatalf("after one success = %+v, want a clean result", snap)
	}
	if len(recoveries) != 1 || r.Degraded() {
		t.Fatalf("one success must recover once, recoveries = %v degraded = %v", recoveries, r.Degraded())
	}
	if _, rec := r.RunOnce(); len(rec) != 0 {
		t.Fatalf("a healthy run must report no recovery, got %v", rec)
	}
}

// TestRunnerThresholdFromConfig proves the threshold is honoured: with 1 the very first
// failure is a fault.
func TestRunnerThresholdFromConfig(t *testing.T) {
	var failures int
	r := New(1, Check{Name: "x", Probe: func() error { return errors.New("e") }})
	r.OnTransition(func(Result) { failures++ }, nil)
	r.RunOnce()
	if failures != 1 || !r.Degraded() {
		t.Fatalf("threshold 1: failures = %d degraded = %v, want an immediate fault", failures, r.Degraded())
	}
	if r.Threshold() != 1 {
		t.Fatalf("Threshold() = %d, want 1", r.Threshold())
	}
	// A zero threshold falls back to the default (0 = "use the default").
	if got := New(0).Threshold(); got != DefaultFailThreshold {
		t.Fatalf("threshold 0 = %d, want the default %d", got, DefaultFailThreshold)
	}
}

// TestSnapshotKeepsOrderAndNeverNil proves the snapshot lists the configured order and
// is an empty (non-nil) slice when there are no checks.
func TestSnapshotKeepsOrderAndNeverNil(t *testing.T) {
	r := New(2, Check{Name: "b", Probe: func() error { return nil }}, Check{Name: "a", Probe: func() error { return nil }})
	r.RunOnce()
	snap := r.Snapshot()
	if len(snap) != 2 || snap[0].Name != "b" || snap[1].Name != "a" {
		t.Fatalf("snapshot = %+v, want configured order b, a", snap)
	}
	if empty := New(0).Snapshot(); empty == nil || len(empty) != 0 {
		t.Fatalf("snapshot of no checks = %#v, want an empty slice", empty)
	}
}

type fakeWriter struct {
	err   error
	last  int64
	calls int
}

func (f *fakeWriter) HealthProbeWrite(now int64) error {
	f.calls++
	if f.err != nil {
		return f.err
	}
	f.last = now
	return nil
}

// TestStorageProbe proves the storage probe writes and reports the writer's error.
func TestStorageProbe(t *testing.T) {
	now := func() time.Time { return time.Unix(1000, 0) }
	w := &fakeWriter{}
	if err := StorageProbe(w, now)(); err != nil {
		t.Fatalf("healthy writer: %v", err)
	}
	if w.calls != 1 || w.last != 1000 {
		t.Fatalf("writer calls = %d stamp = %d, want one write at 1000", w.calls, w.last)
	}
	w.err = errors.New("attempt to write a readonly database")
	if err := StorageProbe(w, now)(); err == nil {
		t.Fatal("read-only store must fail the probe")
	}
	if err := StorageProbe(nil, now)(); err == nil {
		t.Fatal("a missing store must fail the probe")
	}
}

// TestDiskProbe pins the free-space numbers through the seam: the 1GB floor and the 5%
// floor each fail on their own.
func TestDiskProbe(t *testing.T) {
	orig := freeSpaceFor
	t.Cleanup(func() { freeSpaceFor = orig })
	cases := []struct {
		name        string
		total, free uint64
		err         error
		wantFail    bool
		wantText    string
	}{
		{name: "plenty", total: 500 << 30, free: 100 << 30},
		{name: "below 1GiB floor", total: 500 << 30, free: 200 << 20, wantFail: true, wantText: "low disk space"},
		{name: "below 5 percent", total: 100 << 30, free: 2 << 30, wantFail: true, wantText: "below 5%"},
		{name: "just above both", total: 100 << 30, free: 6 << 30},
		{name: "read error", err: errors.New("no such volume"), wantFail: true, wantText: "no such volume"},
	}
	for _, tc := range cases {
		freeSpaceFor = func(string) (uint64, uint64, error) { return tc.total, tc.free, tc.err }
		err := DiskProbe("/data")()
		if tc.wantFail != (err != nil) {
			t.Fatalf("%s: err = %v, wantFail %v", tc.name, err, tc.wantFail)
		}
		if tc.wantFail && !strings.Contains(err.Error(), tc.wantText) {
			t.Fatalf("%s: err = %q, want it to mention %q", tc.name, err, tc.wantText)
		}
	}
}

// TestSweepProbe proves a stalled sweeper (or one that never ran) fails the probe and a
// fresh pass passes it.
func TestSweepProbe(t *testing.T) {
	now := func() time.Time { return time.Unix(10_000, 0) }
	last := time.Unix(9_950, 0)
	probe := SweepProbe(func() time.Time { return last }, 90*time.Second, now)
	if err := probe(); err != nil {
		t.Fatalf("fresh sweep: %v", err)
	}
	last = time.Unix(10_000-91, 0)
	if err := probe(); err == nil || !strings.Contains(err.Error(), "sweeper last ran") {
		t.Fatalf("stale sweep: err = %v, want an overdue report", err)
	}
	if err := SweepProbe(func() time.Time { return time.Time{} }, time.Minute, now)(); err == nil {
		t.Fatal("a sweeper that never ran must fail the probe")
	}
	if err := SweepProbe(nil, time.Minute, now)(); err == nil {
		t.Fatal("a missing sweeper must fail the probe")
	}
}

// TestNotifyProbe proves the per-channel streak decides the item and the failing channel
// is named.
func TestNotifyProbe(t *testing.T) {
	streaks := map[string]int{"webhook": 2, "desktop": 0}
	probe := NotifyProbe(func() map[string]int { return streaks }, 3)
	if err := probe(); err != nil {
		t.Fatalf("streaks below the threshold: %v", err)
	}
	streaks["webhook"] = 4
	err := probe()
	if err == nil || !strings.Contains(err.Error(), "webhook (4 in a row)") {
		t.Fatalf("failing webhook: err = %v, want it named", err)
	}
	streaks = map[string]int{"desktop": 3, "webhook": 0}
	if err := probe(); err == nil || strings.Contains(err.Error(), "webhook") {
		t.Fatalf("failing desktop only: err = %v", err)
	}
	if err := NotifyProbe(nil, 3)(); err != nil {
		t.Fatalf("no dispatcher must not fail the item: %v", err)
	}
}
