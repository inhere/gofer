package job

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/testutil/wait"
	"github.com/inhere/gofer/internal/wsproto"
)

// restartingRunner plays a worker whose process restarts under a session: the first run
// reports the session (id, turn) and then ends with ErrWorkerRestarted; later runs are the
// re-dispatches, handed to onRedispatch, which decides how the reloaded session goes on.
type restartingRunner struct {
	mu           sync.Mutex
	forwards     []runner.Forward
	status       wsproto.Status
	onRedispatch func(req runner.Request) runner.Result
}

func (r *restartingRunner) Name() string { return "remote-w1" }

func (r *restartingRunner) Run(_ context.Context, req runner.Request) runner.Result {
	r.mu.Lock()
	r.forwards = append(r.forwards, *req.Forward)
	n := len(r.forwards)
	r.mu.Unlock()
	if n == 1 {
		if req.OnStartedAt != nil {
			req.OnStartedAt(time.Now().Unix())
		}
		if req.OnSessionStatus != nil {
			req.OnSessionStatus(r.status)
		}
		return runner.Result{ExitCode: -1, Err: runner.NewWorkerRestartedError(errString("worker lost: worker process restarted"))}
	}
	return r.onRedispatch(req)
}

func (r *restartingRunner) forwardAt(i int) runner.Forward {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.forwards[i]
}

type errString string

func (e errString) Error() string { return string(e) }

func submitHostedWorkerSession(t *testing.T, s *Service) JobResult {
	t.Helper()
	res, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "acpw", Runner: "remote-w1", Prompt: "hi", Cwd: ".", TimeoutSec: 30,
		Session: true, Hosted: true,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	return res
}

// TestWorkerSessionReloadedAfterWorkerProcessRestart: a worker session whose process
// restarted is not failed. It waits as `recovering`, is dispatched again under the SAME id with
// the session to load (no new prompt, hosted kept), goes back to the waiting-for-input state
// the new process reports, and the turn counter the new process restarts from zero keeps
// counting on from where the host was.
func TestWorkerSessionReloadedAfterWorkerProcessRestart(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	var started func()
	run := &restartingRunner{
		status: wsproto.Status{Status: StatusAwaitingInput, SessionStatus: StatusAwaitingInput, TurnNo: 3, SessionID: "agent-sess-1"},
	}
	run.onRedispatch = func(req runner.Request) runner.Result {
		started = req.OnStarted
		<-release
		return runner.Result{ExitCode: 0}
	}
	s := newWorkerTestService(t, t.TempDir(), run)
	res := submitHostedWorkerSession(t, s)

	wait.For(t, 10*time.Second, "session waiting as recovering for the new worker process", func() (bool, any) {
		cur, _ := s.Get(res.ID)
		return cur.Status == StatusRecovering, cur.Status
	})
	f := run.forwardAt(1)
	if f.SessionID != "agent-sess-1" || f.ResumedFrom != res.ID || f.Prompt != "" || !f.Hosted || !f.Session || f.WaitWorkerOnline <= 0 {
		t.Fatalf("re-dispatch forward = %+v, want the same session to load, no prompt, hosted kept, a wait for the worker", f)
	}
	wait.Until(t, 5*time.Second, "redispatch started callback wired", func() bool { return started != nil })
	started()
	wait.For(t, 5*time.Second, "job back out of recovering after the worker reports started", func() (bool, any) {
		cur, _ := s.Get(res.ID)
		return cur.Status == StatusRunning && cur.RecoveringSince == 0 && cur.Error == "", cur
	})
	entry := s.entry(res.ID)
	s.applyRemoteSessionState(entry, WorkerInflightJob{JobID: res.ID, Status: StatusAwaitingInput, SessionStatus: StatusAwaitingInput, TurnNo: 1}, false)
	if cur, _ := s.Get(res.ID); cur.TurnNo != 4 || cur.Status != StatusAwaitingInput {
		t.Fatalf("after the first turn on the new process: turn=%d status=%s, want 4 / awaiting_input", cur.TurnNo, cur.Status)
	}
	close(release)
	if final, _ := s.Wait(res.ID); final.Status != StatusDone || final.ID != res.ID {
		t.Fatalf("final = %+v, want done under the same id", final)
	}
}

// TestWorkerSessionRestartFailsWhenItCannotBeReloaded: a session the host cannot reload
// (the agent declares load_session: false) or a job that is not a session still fails on a
// worker restart, with the restart message, and is never dispatched again.
func TestWorkerSessionRestartFailsWhenItCannotBeReloaded(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		agent  string
		sess   bool
		mutate func(*config.Config)
	}{
		{"agent without session/load", "acpw", true, func(cfg *config.Config) {
			no := false
			ac := cfg.Agents["acpw"]
			ac.ACP = &config.ACPConfig{LoadSession: &no}
			cfg.Agents["acpw"] = ac
		}},
		{"not a session", "exec", false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := &restartingRunner{status: wsproto.Status{Status: StatusAwaitingInput, SessionStatus: StatusAwaitingInput, TurnNo: 1, SessionID: "agent-sess-2"}}
			run.onRedispatch = func(runner.Request) runner.Result { return runner.Result{ExitCode: 0} }
			s := newWorkerTestService(t, t.TempDir(), run)
			if tc.mutate != nil {
				cfg := *s.config()
				cfg.Agents = map[string]config.AgentConfig{}
				for k, v := range s.config().Agents {
					cfg.Agents[k] = v
				}
				tc.mutate(&cfg)
				s.cfg.Store(&cfg)
			}
			req := JobRequest{ProjectKey: "self", Agent: tc.agent, Runner: "remote-w1", Cwd: ".", TimeoutSec: 30, Session: tc.sess, Prompt: "hi"}
			if tc.agent == "exec" {
				req.Cmd, req.Prompt = []string{"echo", "x"}, ""
			}
			res, err := s.Submit(req)
			if err != nil {
				t.Fatalf("Submit: %v", err)
			}
			final, _ := s.Wait(res.ID)
			if final.Status != StatusFailed || !strings.Contains(final.Error, "restart") {
				t.Fatalf("final = %s %q, want failed naming the worker restart", final.Status, final.Error)
			}
			run.mu.Lock()
			n := len(run.forwards)
			run.mu.Unlock()
			if n != 1 {
				t.Fatalf("dispatched %d times, want once (no re-dispatch)", n)
			}
		})
	}
}

// TestWorkerSessionReloadedWhenServeAndWorkerBothRestarted: the serve that dispatched the
// session restarted too, so only the stored row is left; the worker process that registers
// under a new instance id gets the session reloaded under the same job id instead of the row
// being failed as lost.
func TestWorkerSessionReloadedWhenServeAndWorkerBothRestarted(t *testing.T) {
	t.Parallel()
	run := &restartingRunner{}
	got := make(chan runner.Forward, 1)
	run.forwards = []runner.Forward{{}} // the first dispatch belonged to the previous serve
	run.onRedispatch = func(req runner.Request) runner.Result {
		got <- *req.Forward
		return runner.Result{ExitCode: 0}
	}
	root := t.TempDir()
	s := newWorkerTestService(t, root, run)
	reqJSON := `{"project_key":"self","agent":"acpw","runner":"remote-w1","cwd":".","session":true,"hosted":true,"prompt":"hi"}`
	now := time.Now().Unix()
	resultDir := filepath.Join(root, "self", "both-restarted")
	if err := os.MkdirAll(filepath.Join(resultDir, "artifacts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.meta.UpsertJob(jobstore.JobRecord{
		ID: "both-restarted", ProjectKey: "self", Agent: "acpw", Runner: "remote-w1", WorkerID: "w1",
		WorkerInstanceID: "old-instance", Status: StatusRecovering, RecoveringSince: now, Cwd: ".", ResultDir: resultDir,
		RequestJSON: reqJSON, SessionID: "agent-sess-3", TimeoutSec: 30, StartedAt: now - 60, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.meta.UpsertJob(jobstore.JobRecord{
		ID: "plain-lost", ProjectKey: "self", Agent: "exec", Runner: "remote-w1", WorkerID: "w1",
		WorkerInstanceID: "old-instance", Status: StatusRecovering, RecoveringSince: now, Cwd: ".", ResultDir: t.TempDir(),
		RequestJSON: `{"project_key":"self","agent":"exec","runner":"remote-w1","cwd":".","cmd":["echo","x"]}`, StartedAt: now - 60, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	adopted, lost := s.ReconcileAdoption("w1", "new-instance", []WorkerInflightJob{}, AdoptBackend{})
	if len(adopted) != 0 || len(lost) != 1 || lost[0] != "plain-lost" {
		t.Fatalf("adopted=%v lost=%v, want only the non-session row lost", adopted, lost)
	}
	select {
	case f := <-got:
		if f.SessionID != "agent-sess-3" || f.ResumedFrom != "both-restarted" || f.Prompt != "" || !f.Hosted || f.WorkerID != "w1" {
			t.Fatalf("re-dispatch forward = %+v", f)
		}
	case <-time.After(wait.Timeout(t, 10*time.Second)):
		cur, _ := s.Get("both-restarted")
		t.Fatalf("the session was not dispatched again to the restarted worker: %s %q", cur.Status, cur.Error)
	}
	if final, _ := s.Wait("both-restarted"); final.Status != StatusDone {
		t.Fatalf("final = %+v", final)
	}
}

// TestAdoptedWorkerSessionReloadedWhenItsProcessRestartsLater: a session adopted after a serve
// restart (no execute goroutine behind it) is handed to a re-dispatch when its worker process
// then restarts, under the same id and entry, instead of being finished as lost.
func TestAdoptedWorkerSessionReloadedWhenItsProcessRestartsLater(t *testing.T) {
	t.Parallel()
	run := &restartingRunner{}
	run.forwards = []runner.Forward{{}}
	got := make(chan runner.Forward, 1)
	run.onRedispatch = func(req runner.Request) runner.Result {
		got <- *req.Forward
		return runner.Result{ExitCode: 0}
	}
	root := t.TempDir()
	s := newWorkerTestService(t, root, run)
	resultDir := filepath.Join(root, "self", "adopted-restart")
	if err := os.MkdirAll(filepath.Join(resultDir, "artifacts"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	if err := s.meta.UpsertJob(jobstore.JobRecord{
		ID: "adopted-restart", ProjectKey: "self", Agent: "acpw", Runner: "remote-w1", WorkerID: "w1",
		WorkerInstanceID: "inst-1", Status: StatusRecovering, RecoveringSince: now, Cwd: ".", ResultDir: resultDir,
		RequestJSON: `{"project_key":"self","agent":"acpw","runner":"remote-w1","cwd":".","session":true,"prompt":"hi"}`,
		SessionID:   "agent-sess-4", TimeoutSec: 30, StartedAt: now - 60, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	adopted, lost := s.ReconcileAdoption("w1", "inst-1", []WorkerInflightJob{{JobID: "adopted-restart", Status: StatusRunning}}, AdoptBackend{})
	aj := adopted["adopted-restart"]
	if aj == nil || len(lost) != 0 {
		t.Fatalf("adopted=%v lost=%v, want the session adopted", adopted, lost)
	}
	aj.OnDisconnect(runner.NewWorkerRestartedError(errString("worker lost: worker process restarted")))
	select {
	case f := <-got:
		if f.SessionID != "agent-sess-4" || f.ResumedFrom != "adopted-restart" || f.Prompt != "" {
			t.Fatalf("re-dispatch forward = %+v", f)
		}
	case <-time.After(wait.Timeout(t, 10*time.Second)):
		cur, _ := s.Get("adopted-restart")
		t.Fatalf("the adopted session was not dispatched again: %s %q", cur.Status, cur.Error)
	}
	if final, _ := s.Wait("adopted-restart"); final.Status != StatusDone {
		t.Fatalf("final = %+v, want done", final)
	}
}

// TestRecoveryWindowLeavesARedispatchingSessionAlone: the serve-start recovery window ends
// what nobody claimed, but a session whose execute is re-dispatching it onto the restarted
// worker process is claimed; failing its row would put a terminal row in front of a live
// entry. The window fails the unclaimed row and leaves the session to its re-dispatch.
func TestRecoveryWindowLeavesARedispatchingSessionAlone(t *testing.T) {
	t.Parallel()
	run := &restartingRunner{}
	run.forwards = []runner.Forward{{}}
	inRedispatch, release := make(chan struct{}), make(chan struct{})
	run.onRedispatch = func(req runner.Request) runner.Result {
		close(inRedispatch)
		<-release
		return runner.Result{ExitCode: 0}
	}
	root := t.TempDir()
	s := newWorkerTestService(t, root, run)
	now := time.Now().Unix()
	resultDir := filepath.Join(root, "self", "window-race")
	if err := os.MkdirAll(filepath.Join(resultDir, "artifacts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.meta.UpsertJob(jobstore.JobRecord{
		ID: "window-race", ProjectKey: "self", Agent: "acpw", Runner: "remote-w1", WorkerID: "w1",
		WorkerInstanceID: "old-instance", Status: StatusRecovering, RecoveringSince: now, Cwd: ".", ResultDir: resultDir,
		RequestJSON: `{"project_key":"self","agent":"acpw","runner":"remote-w1","cwd":".","session":true,"prompt":"hi"}`,
		SessionID:   "agent-sess-9", TimeoutSec: 30, StartedAt: now - 60, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	s.ReconcileAdoption("w1", "new-instance", []WorkerInflightJob{}, AdoptBackend{})
	select {
	case <-inRedispatch:
	case <-time.After(wait.Timeout(t, 10*time.Second)):
		t.Fatal("the session was not re-dispatched")
	}
	if err := s.meta.UpsertJob(jobstore.JobRecord{
		ID: "unclaimed", ProjectKey: "self", Agent: "exec", Runner: "remote-w1", WorkerID: "w1",
		Status: StatusRecovering, RecoveringSince: now, Cwd: ".", ResultDir: t.TempDir(),
		RequestJSON: `{"project_key":"self","agent":"exec","runner":"remote-w1","cwd":".","cmd":["echo","x"]}`, StartedAt: now - 60, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	n, err := s.FailRecoveringJobs("worker lost")
	if err != nil || n != 1 {
		t.Fatalf("FailRecoveringJobs = %d, %v; want only the unclaimed row", n, err)
	}
	if rec, ok, _ := s.meta.GetJob("window-race"); !ok || rec.Status == StatusFailed {
		t.Fatalf("re-dispatching session row = %+v, want it left to its re-dispatch", rec)
	}
	close(release)
	if final, _ := s.Wait("window-race"); final.Status != StatusDone {
		t.Fatalf("final = %+v", final)
	}
}
