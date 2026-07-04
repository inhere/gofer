package job

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
)

// stubWorkerRunner is a minimal runner.Runner standing in for the real
// workerRunner: it records the forwarded request and returns a fixed result. It
// lets the job-service worker branch be tested without a hub/worker.
type stubWorkerRunner struct {
	gotForward *runner.Forward
}

func (r *stubWorkerRunner) Name() string { return "remote-w1" }
func (r *stubWorkerRunner) Run(_ context.Context, req runner.Request) runner.Result {
	r.gotForward = req.Forward
	return runner.Result{ExitCode: 0}
}

type recordingRunner struct {
	name string
	reqs []runner.Request
}

func (r *recordingRunner) Name() string { return r.name }
func (r *recordingRunner) Run(_ context.Context, req runner.Request) runner.Result {
	r.reqs = append(r.reqs, req)
	return runner.Result{ExitCode: 0}
}

// newWorkerTestService builds a service with a server.workers entry (w1) and a
// type=worker runner (remote-w1) pointing at it, plus a stub runner instance. It
// uses a nil WorkerSelector (the explicit-worker_id tests never auto-select).
func newWorkerTestService(t *testing.T, root string, stub runner.Runner) *Service {
	t.Helper()
	// Register w1 + w2 so label-selection tests have >1 candidate; the runner is
	// still bound to w1 by default (D4) for the explicit-routing tests.
	workers := map[string]config.WorkerAuthConfig{
		"w1": {Token: "tok-w1"},
		"w2": {Token: "tok-w2"},
	}
	return newWorkerTestServiceSel(t, root, stub, workers, nil)
}

// newWorkerTestServiceSel is newWorkerTestService with an explicit workers set
// and WorkerSelector, so the label auto-selection path (P2) can be exercised with
// a fake candidate source.
func newWorkerTestServiceSel(t *testing.T, root string, stub runner.Runner, workers map[string]config.WorkerAuthConfig, sel WorkerSelector) *Service {
	t.Helper()
	cfg := &config.Config{
		Server: config.ServerConfig{
			Workers: workers,
		},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:                 root,
				AllowedAgents:            []string{"exec", "term"},
				AllowedRunners:           []string{"local", "remote-w1"},
				InteractiveAllowedAgents: []string{"term"},
				AllowExec:                true,
			},
		},
		Agents: map[string]config.AgentConfig{
			"term": {Type: agent.TypeCLIAgent, Command: "echo", Args: []string{"{{prompt}}"}, Interactive: true, NoRawCmd: true},
		},
		Runners: map[string]config.RunnerConfig{
			"remote-w1": {Type: "worker", WorkerID: "w1"},
		},
	}
	projReg := project.NewRegistry(cfg, "")
	agentReg := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{
		localrunner.Name: localrunner.New(),
		"remote-w1":      stub,
	}
	meta, err := jobstore.Open(filepath.Join(root, "gofer.db"))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	return NewService(cfg, projReg, agentReg, runners, meta, sel)
}

// fakeSelector is a job.WorkerSelector returning a fixed candidate list.
type fakeSelector struct {
	cands []WorkerCandidate
}

func (f fakeSelector) Candidates() []WorkerCandidate { return f.cands }

// TestSubmitWorkerNoWorkerIDFallsBack proves a worker job with neither worker_id
// nor labels is now ACCEPTED (P2 D4): Submit no longer rejects it; the worker
// runner falls back to its configured default worker. Forward.WorkerID stays
// empty so the runner uses r.workerID.
func TestSubmitWorkerNoWorkerIDFallsBack(t *testing.T) {
	stub := &stubWorkerRunner{}
	s := newWorkerTestService(t, t.TempDir(), stub)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "remote-w1",
		Cmd: []string{"echo", "hi"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("expected done (D4 fallback), got %s (err=%s)", final.Status, final.Error)
	}
	if stub.gotForward == nil || stub.gotForward.WorkerID != "" {
		t.Fatalf("Forward.WorkerID should be empty for the D4 fallback, got %+v", stub.gotForward)
	}
	if final.WorkerID != "" {
		t.Fatalf("JobResult.WorkerID = %q, want empty (resolved by runner default)", final.WorkerID)
	}
}

func TestSubmitUnknownWorkerID(t *testing.T) {
	s := newWorkerTestService(t, t.TempDir(), &stubWorkerRunner{})
	_, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "remote-w1", WorkerID: "ghost",
		Cmd: []string{"echo", "hi"}, Cwd: ".",
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("expected ErrInvalidRequest for unknown worker_id, got %v", err)
	}
}

// TestSubmitWorkerSetsForward proves the worker job takes the remote branch:
// Forward is populated (PeerRunner=local) and local cwd/command resolution is
// skipped (Cwd round-trips opaquely to the worker).
func TestSubmitWorkerSetsForward(t *testing.T) {
	stub := &stubWorkerRunner{}
	s := newWorkerTestService(t, t.TempDir(), stub)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "remote-w1", WorkerID: "w1",
		Cmd: []string{"echo", "hi"}, Cwd: "sub/dir", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("expected done, got %s (err=%s)", final.Status, final.Error)
	}
	if stub.gotForward == nil {
		t.Fatal("worker runner got nil Forward (remote branch not taken)")
	}
	if stub.gotForward.PeerRunner != builtinLocalRunner {
		t.Fatalf("Forward.PeerRunner = %q, want local", stub.gotForward.PeerRunner)
	}
	if stub.gotForward.Cwd != "sub/dir" {
		t.Fatalf("Forward.Cwd = %q, want opaque sub/dir (not SafeJoin'd)", stub.gotForward.Cwd)
	}
	if stub.gotForward.WorkerID != "w1" {
		t.Fatalf("Forward.WorkerID = %q, want w1 (explicit routing)", stub.gotForward.WorkerID)
	}
	if final.WorkerID != "w1" {
		t.Fatalf("final.WorkerID = %q, want w1", final.WorkerID)
	}
}

func TestSubmitInteractiveRunnerSelectionLocalVsWorker(t *testing.T) {
	t.Run("worker remote keeps worker runner", func(t *testing.T) {
		stub := &stubWorkerRunner{}
		pty := &recordingRunner{name: builtinPtyRunner}
		s := newWorkerTestService(t, t.TempDir(), stub)
		s.runners[builtinPtyRunner] = pty

		final := submitAndWait(t, s, JobRequest{
			ProjectKey: "self", Agent: "term", Runner: "remote-w1", WorkerID: "w1",
			Interactive: true,
			Cols:        120,
			Rows:        40,
			Prompt:      "hi", Cwd: ".", TimeoutSec: 30,
		})
		if final.Status != StatusDone {
			t.Fatalf("expected done, got %s (err=%s)", final.Status, final.Error)
		}
		if stub.gotForward == nil {
			t.Fatal("worker runner got nil Forward; interactive remote should stay on worker runner")
		}
		if !stub.gotForward.Interactive || stub.gotForward.Cols != 120 || stub.gotForward.Rows != 40 {
			t.Fatalf("Forward interactive size = (%v,%d,%d), want (true,120,40)", stub.gotForward.Interactive, stub.gotForward.Cols, stub.gotForward.Rows)
		}
		if len(pty.reqs) != 0 {
			t.Fatalf("pty runner was called for remote interactive job: %+v", pty.reqs)
		}
	})

	t.Run("local uses pty runner", func(t *testing.T) {
		pty := &recordingRunner{name: builtinPtyRunner}
		s := newWorkerTestService(t, t.TempDir(), &stubWorkerRunner{})
		s.runners[builtinPtyRunner] = pty

		final := submitAndWait(t, s, JobRequest{
			ProjectKey: "self", Agent: "term", Runner: "local",
			Interactive: true,
			Cols:        132,
			Rows:        43,
			Prompt:      "hi", Cwd: ".", TimeoutSec: 30,
		})
		if final.Status != StatusDone {
			t.Fatalf("expected done, got %s (err=%s)", final.Status, final.Error)
		}
		if len(pty.reqs) != 1 {
			t.Fatalf("pty runner calls = %d, want 1", len(pty.reqs))
		}
		if pty.reqs[0].Forward != nil {
			t.Fatalf("local pty runner got Forward: %+v", pty.reqs[0].Forward)
		}
		if !pty.reqs[0].Interactive || pty.reqs[0].Cols != 132 || pty.reqs[0].Rows != 43 {
			t.Fatalf("runner.Request interactive size = (%v,%d,%d), want (true,132,43)", pty.reqs[0].Interactive, pty.reqs[0].Cols, pty.reqs[0].Rows)
		}
	})
}

// TestWorkerIDRoundTrip proves WorkerID survives Submit → persist (UpsertJob) →
// fromRecord: a fresh Service reading the same DB sees worker_id.
func TestWorkerIDRoundTrip(t *testing.T) {
	root := t.TempDir()
	stub := &stubWorkerRunner{}
	s := newWorkerTestService(t, root, stub)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "remote-w1", WorkerID: "w1",
		Cmd: []string{"echo", "hi"}, Cwd: ".", TimeoutSec: 30,
	})
	// Read back from the metadata store (the persisted terminal row).
	rec, ok, err := s.meta.GetJob(final.ID)
	if err != nil || !ok {
		t.Fatalf("GetJob(%s): ok=%v err=%v", final.ID, ok, err)
	}
	if rec.WorkerID != "w1" {
		t.Fatalf("persisted worker_id = %q, want w1", rec.WorkerID)
	}
	if got := fromRecord(rec).WorkerID; got != "w1" {
		t.Fatalf("fromRecord worker_id = %q, want w1", got)
	}
}

// TestSubmitWorkerLabelsAutoSelect (P2 D3): a worker job with only worker_labels
// auto-selects a connected worker via the WorkerSelector, injecting its id into
// both the Forward and the persisted JobResult.worker_id.
func TestSubmitWorkerLabelsAutoSelect(t *testing.T) {
	stub := &stubWorkerRunner{}
	workers := map[string]config.WorkerAuthConfig{
		"w1": {Token: "tok-w1"},
		"w2": {Token: "tok-w2"},
	}
	sel := fakeSelector{cands: []WorkerCandidate{
		{WorkerID: "w1", Labels: []string{"cpu"}, InFlight: 3, HeartbeatAge: time.Second},
		{WorkerID: "w2", Labels: []string{"gpu"}, InFlight: 0, HeartbeatAge: time.Second},
	}}
	s := newWorkerTestServiceSel(t, t.TempDir(), stub, workers, sel)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "remote-w1",
		WorkerLabels: []string{"gpu"},
		Cmd:          []string{"echo", "hi"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("expected done, got %s (err=%s)", final.Status, final.Error)
	}
	if final.WorkerID != "w2" {
		t.Fatalf("JobResult.WorkerID = %q, want w2 (label gpu auto-select)", final.WorkerID)
	}
	if stub.gotForward == nil || stub.gotForward.WorkerID != "w2" {
		t.Fatalf("Forward.WorkerID = %+v, want w2", stub.gotForward)
	}
}

// TestSubmitWorkerLabelsNoEligible (P2): worker_labels with no eligible candidate
// is rejected with ErrNoEligibleWorker (HTTP 503).
func TestSubmitWorkerLabelsNoEligible(t *testing.T) {
	stub := &stubWorkerRunner{}
	workers := map[string]config.WorkerAuthConfig{"w1": {Token: "tok-w1"}}
	sel := fakeSelector{cands: []WorkerCandidate{
		{WorkerID: "w1", Labels: []string{"cpu"}, HeartbeatAge: time.Second},
	}}
	s := newWorkerTestServiceSel(t, t.TempDir(), stub, workers, sel)
	_, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "remote-w1",
		WorkerLabels: []string{"gpu"}, // no candidate has gpu
		Cmd:          []string{"echo", "hi"}, Cwd: ".",
	})
	if !errors.Is(err, ErrNoEligibleWorker) {
		t.Fatalf("expected ErrNoEligibleWorker, got %v", err)
	}
}

// TestSubmitWorkerIDWinsOverLabels (P2): when both worker_id and worker_labels are
// given, the explicit worker_id wins and labels are ignored (the selector is not
// even consulted — proven by a selector that would otherwise pick a different id).
func TestSubmitWorkerIDWinsOverLabels(t *testing.T) {
	stub := &stubWorkerRunner{}
	workers := map[string]config.WorkerAuthConfig{
		"w1": {Token: "tok-w1"},
		"w2": {Token: "tok-w2"},
	}
	// The selector would pick w2 for "gpu"; the explicit worker_id w1 must override.
	sel := fakeSelector{cands: []WorkerCandidate{
		{WorkerID: "w2", Labels: []string{"gpu"}, InFlight: 0, HeartbeatAge: time.Second},
	}}
	s := newWorkerTestServiceSel(t, t.TempDir(), stub, workers, sel)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "remote-w1",
		WorkerID: "w1", WorkerLabels: []string{"gpu"},
		Cmd: []string{"echo", "hi"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.WorkerID != "w1" {
		t.Fatalf("JobResult.WorkerID = %q, want w1 (explicit wins over labels)", final.WorkerID)
	}
	if stub.gotForward == nil || stub.gotForward.WorkerID != "w1" {
		t.Fatalf("Forward.WorkerID = %+v, want w1", stub.gotForward)
	}
}

func TestIsRemoteRunner(t *testing.T) {
	cfg := &config.Config{
		Runners: map[string]config.RunnerConfig{
			"remote-w1": {Type: "worker", WorkerID: "w1"},
			"peer":      {Type: "peer-http", BaseURL: "http://x"},
		},
	}
	if !isWorkerRunner(cfg, "remote-w1") {
		t.Fatal("remote-w1 should be a worker runner")
	}
	if !IsRemoteRunner(cfg, "remote-w1") || !IsRemoteRunner(cfg, "peer") {
		t.Fatal("worker and peer-http should both be remote runners")
	}
	if IsRemoteRunner(cfg, "local") {
		t.Fatal("local should not be a remote runner")
	}
}
