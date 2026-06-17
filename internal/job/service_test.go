package job

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dev-agent-bridge/internal/agent"
	"dev-agent-bridge/internal/config"
	"dev-agent-bridge/internal/project"
	"dev-agent-bridge/internal/runner"
	localrunner "dev-agent-bridge/internal/runner/local"
	"dev-agent-bridge/internal/store"
)

// newTestService builds a Service whose result base dir lives under a temp dir.
// It registers two projects: "self" (allow_exec=true) and "noexec"
// (allow_exec=false). storage.root points at root so result dirs are isolated.
func newTestService(t *testing.T, root string) *Service {
	t.Helper()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root, // any existing dir; cwd "." resolves here
				AllowedAgents:  []string{"exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
			"noexec": {
				HostPath:       root,
				AllowedAgents:  []string{"exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      false,
			},
		},
	}
	projReg := project.NewRegistry(cfg, "")
	agentReg := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	return NewService(cfg, projReg, agentReg, runners)
}

func submitAndWait(t *testing.T, s *Service, req JobRequest) JobResult {
	t.Helper()
	res, err := s.Submit(req)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	final, ok := s.Wait(res.ID)
	if !ok {
		t.Fatalf("Wait: job %s not found", res.ID)
	}
	return final
}

func TestSubmitExecDone(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("expected done, got %s (err=%s)", final.Status, final.Error)
	}
	if final.ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d", final.ExitCode)
	}
	out, err := store.NewFileStore(filepath.Join(root, "self")).ReadLogTail(final.ID, store.StreamStdout, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "go version") {
		t.Fatalf("stdout.log missing output: %q", out)
	}
}

func TestSubmitExecFailed(t *testing.T) {
	s := newTestService(t, t.TempDir())
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sh", "-c", "exit 3"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusFailed {
		t.Fatalf("expected failed, got %s", final.Status)
	}
	if final.ExitCode != 3 {
		t.Fatalf("expected exit 3, got %d", final.ExitCode)
	}
}

func TestSubmitTimeout(t *testing.T) {
	s := newTestService(t, t.TempDir())
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sleep", "5"}, Cwd: ".", TimeoutSec: 1,
	})
	if final.Status != StatusTimeout {
		t.Fatalf("expected timeout, got %s (err=%s)", final.Status, final.Error)
	}
}

func TestSubmitCancel(t *testing.T) {
	s := newTestService(t, t.TempDir())
	res, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sleep", "5"}, Cwd: ".", TimeoutSec: 30,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	// Give the process a moment to start running, then cancel.
	waitForStatus(t, s, res.ID, StatusRunning, 2*time.Second)
	if err := s.Cancel(res.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	final, _ := s.Wait(res.ID)
	if final.Status != StatusCancelled {
		t.Fatalf("expected cancelled, got %s", final.Status)
	}
}

func TestCancelCompletedIsNoOp(t *testing.T) {
	s := newTestService(t, t.TempDir())
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("setup: expected done")
	}
	// Cancelling a terminal job is a deterministic no-op (nil error), and does
	// not change the recorded status.
	if err := s.Cancel(final.ID); err != nil {
		t.Fatalf("cancel of completed job should be no-op, got %v", err)
	}
	again, _ := s.Get(final.ID)
	if again.Status != StatusDone {
		t.Fatalf("status changed after no-op cancel: %s", again.Status)
	}
}

func TestCancelUnknownJob(t *testing.T) {
	s := newTestService(t, t.TempDir())
	if err := s.Cancel("does-not-exist"); err == nil {
		t.Fatalf("expected error for unknown job id")
	}
}

func TestExecSecurityGate(t *testing.T) {
	s := newTestService(t, t.TempDir())
	_, err := s.Submit(JobRequest{
		ProjectKey: "noexec", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if err == nil {
		t.Fatalf("expected exec to be rejected when allow_exec=false")
	}
	if !strings.Contains(err.Error(), "allow_exec") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUnknownProjectRejected(t *testing.T) {
	s := newTestService(t, t.TempDir())
	if _, err := s.Submit(JobRequest{ProjectKey: "ghost", Agent: "exec", Runner: "local", Cmd: []string{"go"}}); err == nil {
		t.Fatalf("expected unknown project error")
	}
}

func TestAgentNotAllowedRejected(t *testing.T) {
	s := newTestService(t, t.TempDir())
	if _, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "claude", Runner: "local", Prompt: "hi"}); err == nil {
		t.Fatalf("expected agent-not-allowed error")
	}
}

func TestRunnerNotAllowedRejected(t *testing.T) {
	s := newTestService(t, t.TempDir())
	if _, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "exec", Runner: "docker-peer", Cmd: []string{"go"}}); err == nil {
		t.Fatalf("expected runner-not-allowed error")
	}
}

func TestResultJSONAtomicAndComplete(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	dir := filepath.Join(root, "self", final.ID)
	if _, err := os.Stat(filepath.Join(dir, store.ResultFile+".tmp")); !os.IsNotExist(err) {
		t.Fatalf("result.json.tmp leaked")
	}
	data, err := os.ReadFile(filepath.Join(dir, store.ResultFile))
	if err != nil {
		t.Fatal(err)
	}
	var jr JobResult
	if err := json.Unmarshal(data, &jr); err != nil {
		t.Fatalf("result.json not parseable: %v", err)
	}
	if jr.ID != final.ID || jr.Status != StatusDone {
		t.Fatalf("result.json mismatch: %+v", jr)
	}
	// request.json must also exist.
	if _, err := os.Stat(filepath.Join(dir, store.RequestFile)); err != nil {
		t.Fatalf("request.json missing: %v", err)
	}
}

func TestJobIDUniquenessSameSecond(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	// Pin the clock to a single second so ids only differ by the random suffix;
	// this is the cross-restart collision case the plan calls out.
	fixed := time.Date(2026, 6, 16, 1, 2, 3, 0, time.UTC)
	s.nowFn = func() time.Time { return fixed }

	const n = 200
	seen := map[string]bool{}
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		res, err := s.Submit(JobRequest{
			ProjectKey: "self", Agent: "exec", Runner: "local",
			Cmd: []string{"sh", "-c", "exit 0"}, Cwd: ".", TimeoutSec: 30,
		})
		if err != nil {
			t.Fatalf("Submit %d: %v", i, err)
		}
		if seen[res.ID] {
			t.Fatalf("duplicate job id: %s", res.ID)
		}
		seen[res.ID] = true
		ids = append(ids, res.ID)
		// Each id's dir must have been created.
		if _, err := os.Stat(filepath.Join(root, "self", res.ID)); err != nil {
			t.Fatalf("job dir not created for %s: %v", res.ID, err)
		}
	}
	if len(seen) != n {
		t.Fatalf("expected %d unique ids, got %d", n, len(seen))
	}
	// Drain all background jobs so their goroutines stop writing into root
	// before t.TempDir() cleanup runs (avoids a RemoveAll-vs-write race).
	for _, id := range ids {
		s.Wait(id)
	}
}

func TestConcurrencyLimit(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	// Limit project "self" to 1 concurrent job.
	p := s.cfg.Projects["self"]
	p.MaxConcurrentJobs = 1
	s.cfg.Projects["self"] = p

	// Submit job1 (sleep) and wait until it is actually running and holding the
	// single slot BEFORE submitting job2, so the slot ownership is deterministic.
	r1, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"sleep", "1"}, Cwd: ".", TimeoutSec: 30})
	if err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, s, r1.ID, StatusRunning, 2*time.Second)
	r2, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"sh", "-c", "exit 0"}, Cwd: ".", TimeoutSec: 30})
	if err != nil {
		t.Fatal(err)
	}
	// Give job2's goroutine a moment to reach the (blocked) slot acquisition.
	time.Sleep(50 * time.Millisecond)
	// While job1 runs, job2 must still be queued (slot held by job1).
	if j2, _ := s.Get(r2.ID); j2.Status != StatusQueued {
		t.Fatalf("expected job2 queued while job1 runs, got %s", j2.Status)
	}
	// Both eventually complete.
	f1, _ := s.Wait(r1.ID)
	f2, _ := s.Wait(r2.ID)
	if f1.Status != StatusDone || f2.Status != StatusDone {
		t.Fatalf("expected both done, got %s/%s", f1.Status, f2.Status)
	}
}

// waitForStatus polls until the job reaches want or the deadline elapses.
func waitForStatus(t *testing.T, s *Service, id, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if r, ok := s.Get(id); ok && r.Status == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	r, _ := s.Get(id)
	t.Fatalf("job %s did not reach %q in time (status=%s)", id, want, r.Status)
}
