package job

import (
	"errors"
	"strings"
	"testing"
)

// A runner pinned to a worker (type=worker + worker_id in config) names exactly ONE
// target, and that pin is an authorization rather than a default route: a project
// says "only this box may run me on the worker side" by listing that runner in
// allowed_runners, and the statement is worthless if a request can re-point the
// runner at a different worker.
//
// It used to be able to. runner/worker.Runner honours Forward.WorkerID over the
// runner's own pin and selectTargetWorker prefers the label branch, so a job
// submitted through a runner pinned to w1 would execute on w2 whenever w2 happened
// to carry the project — the pin only decided where an EMPTY worker_id landed.
// (Reproduced live 2026-07-14; found by an adversarial review of the federation
// design, which had assumed allowed_runners already expressed worker-level
// placement.)
//
// The fixture (newWorkerTestService) pins runner "remote-w1" to worker w1 and also
// registers w2, so each test below is exactly that override attempt.

func TestPinnedRunnerRejectsForeignWorkerID(t *testing.T) {
	s := newWorkerTestService(t, t.TempDir(), &stubWorkerRunner{})
	_, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "remote-w1",
		WorkerID: "w2", // w2 is a registered worker — but NOT the one remote-w1 pins
		Cmd:      []string{"echo", "hi"}, Cwd: ".",
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("pinned runner + foreign worker_id: got %v, want ErrInvalidRequest", err)
	}
	// The message must name the pin — an operator hitting this needs to know the
	// runner is bound, not merely that "something was wrong".
	if !strings.Contains(err.Error(), "pinned to worker \"w1\"") {
		t.Fatalf("error must name the pin, got: %v", err)
	}
}

func TestPinnedRunnerRejectsWorkerLabels(t *testing.T) {
	s := newWorkerTestService(t, t.TempDir(), &stubWorkerRunner{})
	_, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "remote-w1",
		WorkerLabels: []string{"gpu"}, // labels would hand the target to selectWorker
		Cmd:          []string{"echo", "hi"}, Cwd: ".",
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("pinned runner + worker_labels: got %v, want ErrInvalidRequest", err)
	}
	if !strings.Contains(err.Error(), "worker_labels cannot re-route") {
		t.Fatalf("error must say labels cannot re-route a pinned runner, got: %v", err)
	}
}

// An explicit worker_id EQUAL to the pin is a no-op restatement, not an override —
// it must stay legal (the web form and rebuild path both send it).
func TestPinnedRunnerAcceptsMatchingWorkerID(t *testing.T) {
	s := newWorkerTestService(t, t.TempDir(), &stubWorkerRunner{})
	if _, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "remote-w1",
		WorkerID: "w1",
		Cmd:      []string{"echo", "hi"}, Cwd: ".",
	}); err != nil {
		t.Fatalf("worker_id equal to the pin must be accepted, got: %v", err)
	}
}

// And an EMPTY worker_id still falls back to the pin (the common path — the web form
// stops sending worker_id once the runner names its worker).
func TestPinnedRunnerEmptyWorkerIDStillWorks(t *testing.T) {
	s := newWorkerTestService(t, t.TempDir(), &stubWorkerRunner{})
	if _, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "remote-w1",
		Cmd: []string{"echo", "hi"}, Cwd: ".",
	}); err != nil {
		t.Fatalf("empty worker_id must fall back to the pin, got: %v", err)
	}
}
