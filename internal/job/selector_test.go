package job

import (
	"testing"
	"time"
)

// TestSelectWorkerLabelsAllRequired: a worker is eligible only when it advertises
// every required label (AND semantics); one missing label excludes it.
func TestSelectWorkerLabelsAllRequired(t *testing.T) {
	cands := []WorkerCandidate{
		{WorkerID: "w-gpu", Labels: []string{"gpu", "linux"}, HeartbeatAge: time.Second},
		{WorkerID: "w-cpu", Labels: []string{"cpu", "linux"}, HeartbeatAge: time.Second},
	}
	if got := selectWorker(cands, []string{"gpu"}, false); got != "w-gpu" {
		t.Fatalf("single-label match = %q, want w-gpu", got)
	}
	if got := selectWorker(cands, []string{"gpu", "linux"}, false); got != "w-gpu" {
		t.Fatalf("all-labels match = %q, want w-gpu", got)
	}
	// w-cpu has linux but not gpu: a required "gpu" excludes it; no other candidate
	// has both gpu+windows, so the result is empty.
	if got := selectWorker(cands, []string{"gpu", "windows"}, false); got != "" {
		t.Fatalf("missing-label should exclude all, got %q", got)
	}
}

// TestSelectWorkerStaleExcluded: a candidate whose heartbeat age exceeds the
// staleness threshold is treated as offline and never selected.
func TestSelectWorkerStaleExcluded(t *testing.T) {
	cands := []WorkerCandidate{
		{WorkerID: "w-stale", Labels: []string{"gpu"}, HeartbeatAge: workerStaleAfter + time.Second},
		{WorkerID: "w-fresh", Labels: []string{"gpu"}, HeartbeatAge: 2 * time.Second},
	}
	if got := selectWorker(cands, []string{"gpu"}, false); got != "w-fresh" {
		t.Fatalf("stale candidate must be excluded, got %q want w-fresh", got)
	}
	// Only the stale candidate matches → no eligible worker.
	only := []WorkerCandidate{{WorkerID: "w-stale", Labels: []string{"gpu"}, HeartbeatAge: workerStaleAfter + time.Second}}
	if got := selectWorker(only, []string{"gpu"}, false); got != "" {
		t.Fatalf("a single stale candidate must yield empty, got %q", got)
	}
}

// TestSelectWorkerOrdersByLoadThenAge: among eligible candidates the least loaded
// (in_flight) wins; ties break to the freshest (smallest heartbeat age).
func TestSelectWorkerOrdersByLoadThenAge(t *testing.T) {
	cands := []WorkerCandidate{
		{WorkerID: "w-busy", Labels: []string{"gpu"}, InFlight: 5, HeartbeatAge: time.Second},
		{WorkerID: "w-idle", Labels: []string{"gpu"}, InFlight: 0, HeartbeatAge: 10 * time.Second},
		{WorkerID: "w-mid", Labels: []string{"gpu"}, InFlight: 2, HeartbeatAge: time.Second},
	}
	if got := selectWorker(cands, []string{"gpu"}, false); got != "w-idle" {
		t.Fatalf("least-loaded should win, got %q want w-idle", got)
	}

	// Equal in_flight: the freshest (smaller age) wins the tie.
	tie := []WorkerCandidate{
		{WorkerID: "w-old", Labels: []string{"gpu"}, InFlight: 1, HeartbeatAge: 9 * time.Second},
		{WorkerID: "w-new", Labels: []string{"gpu"}, InFlight: 1, HeartbeatAge: 1 * time.Second},
	}
	if got := selectWorker(tie, []string{"gpu"}, false); got != "w-new" {
		t.Fatalf("age tiebreak should pick freshest, got %q want w-new", got)
	}
}

// TestSelectWorkerInteractiveRequiresPtyCapable verifies pty capability is only
// part of selection for interactive requests; non-interactive label selection is
// unchanged.
func TestSelectWorkerInteractiveRequiresPtyCapable(t *testing.T) {
	cands := []WorkerCandidate{
		{WorkerID: "w-old", Labels: []string{"gpu"}, InFlight: 0, PtyCapable: false, HeartbeatAge: time.Second},
		{WorkerID: "w-pty", Labels: []string{"gpu"}, InFlight: 1, PtyCapable: true, HeartbeatAge: time.Second},
	}
	if got := selectWorker(cands, []string{"gpu"}, false); got != "w-old" {
		t.Fatalf("non-interactive should ignore pty capability, got %q want w-old", got)
	}
	if got := selectWorker(cands, []string{"gpu"}, true); got != "w-pty" {
		t.Fatalf("interactive should require pty-capable worker, got %q want w-pty", got)
	}
}

// TestSelectWorkerNoCandidates: an empty candidate list (or no match) returns "".
func TestSelectWorkerNoCandidates(t *testing.T) {
	if got := selectWorker(nil, []string{"gpu"}, false); got != "" {
		t.Fatalf("nil candidates should yield empty, got %q", got)
	}
	cands := []WorkerCandidate{{WorkerID: "w1", Labels: []string{"cpu"}, HeartbeatAge: time.Second}}
	if got := selectWorker(cands, []string{"gpu"}, false); got != "" {
		t.Fatalf("no matching candidate should yield empty, got %q", got)
	}
}

// TestHasAllLabels covers the AND-containment helper directly, including the
// empty-required (matches anything) edge.
func TestHasAllLabels(t *testing.T) {
	if !hasAllLabels([]string{"a", "b"}, nil) {
		t.Fatal("empty required should match any worker")
	}
	if !hasAllLabels([]string{"a", "b", "c"}, []string{"a", "c"}) {
		t.Fatal("subset required should match")
	}
	if hasAllLabels([]string{"a", "b"}, []string{"a", "z"}) {
		t.Fatal("a missing required label must fail containment")
	}
}
