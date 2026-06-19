package job

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

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

// newWorkerTestService builds a service with a server.workers entry (w1) and a
// type=worker runner (remote-w1) pointing at it, plus a stub runner instance.
func newWorkerTestService(t *testing.T, root string, stub runner.Runner) *Service {
	t.Helper()
	cfg := &config.Config{
		Server: config.ServerConfig{
			Workers: map[string]config.WorkerAuthConfig{
				"w1": {Token: "tok-w1"},
			},
		},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"exec"},
				AllowedRunners: []string{"local", "remote-w1"},
				AllowExec:      true,
			},
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
	return NewService(cfg, projReg, agentReg, runners, meta)
}

func TestSubmitWorkerRequiresWorkerID(t *testing.T) {
	s := newWorkerTestService(t, t.TempDir(), &stubWorkerRunner{})
	_, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "remote-w1",
		Cmd: []string{"echo", "hi"}, Cwd: ".",
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("expected ErrInvalidRequest for missing worker_id, got %v", err)
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
	if final.WorkerID != "w1" {
		t.Fatalf("final.WorkerID = %q, want w1", final.WorkerID)
	}
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
	if !isRemoteRunner(cfg, "remote-w1") || !isRemoteRunner(cfg, "peer") {
		t.Fatal("worker and peer-http should both be remote runners")
	}
	if isRemoteRunner(cfg, "local") {
		t.Fatal("local should not be a remote runner")
	}
}
