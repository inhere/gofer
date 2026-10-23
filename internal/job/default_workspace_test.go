package job

import (
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
)

// gofer-vin1: a session that belongs to no project is still delivered to, and the
// internal delivery job that carries the message runs in the execution machine's
// default workspace project (config.DefaultProjectKey). These pins are the admission
// half of that: the built-in default lists only the machine that owns it (`local`),
// and nobody wrote a policy for it — so gofer's own relay work gets the two gates
// waived, and NOTHING else does.

// defaultWorkspaceService is a worker-capable service whose config carries the built-in
// default workspace project next to a normal one, plus a worker reporting it.
func defaultWorkspaceService(t *testing.T, stub *stubWorkerRunner) *Service {
	t.Helper()
	s := newWorkerTestServiceSel(t, t.TempDir(), stub,
		map[string]config.WorkerAuthConfig{"w1": {Token: "tok-w1"}},
		fakeSelector{cands: []WorkerCandidate{{
			WorkerID: "w1", Projects: []string{"self", config.DefaultProjectKey},
			Agents: []string{"exec", "term"}, HeartbeatAge: time.Second,
		}}})
	cfg := s.config()
	cfg.Projects[config.DefaultProjectKey] = config.DefaultWorkspaceProject(t.TempDir(), []string{"term"})
	// A real project with a NARROW runner list, for the unchanged-behaviour pin below.
	cfg.Projects["narrow"] = config.ProjectConfig{HostPath: t.TempDir(), AllowedRunners: []string{"local"}, AllowExec: true}
	return s
}

// TestRelayDeliveryRunsInTheDefaultWorkspaceOnAWorker: the delivery job is admitted on
// the session's WORKER even though the built-in default lists only `local`, and it is
// dispatched with the default project and its root as the cwd.
func TestRelayDeliveryRunsInTheDefaultWorkspaceOnAWorker(t *testing.T) {
	t.Parallel()
	stub := &stubWorkerRunner{}
	s := defaultWorkspaceService(t, stub)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: config.DefaultProjectKey, Agent: "exec", Runner: "remote-w1",
		Cmd: []string{"gofer-send", "hi"}, Cwd: ".", TimeoutSec: 30,
		InternalExec: InternalExecRelay,
	})
	if final.Status != StatusDone {
		t.Fatalf("relay delivery in the default workspace = %s (%s), want done", final.Status, final.Error)
	}
	if stub.gotForward == nil {
		t.Fatal("the delivery was not dispatched")
	}
	if got := stub.gotForward.ProjectKey; got != config.DefaultProjectKey {
		t.Fatalf("dispatched project = %q, want %q", got, config.DefaultProjectKey)
	}
	if got := stub.gotForward.Cwd; got != "." {
		t.Fatalf("dispatched cwd = %q, want %q (the default workspace root)", got, ".")
	}
}

// TestRelayDeliveryWaivesTheExecGateInTheDefaultWorkspace: the built-in default has
// allow_exec=false and does not list the exec carrier, yet gofer's own delivery must run
// there — path A's tmux script and path C's deliver_command are exec carriers.
func TestRelayDeliveryWaivesTheExecGateInTheDefaultWorkspace(t *testing.T) {
	t.Parallel()
	s := defaultWorkspaceService(t, &stubWorkerRunner{})
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: config.DefaultProjectKey, Agent: "exec", Runner: "local",
		Cmd: []string{"echo", "typed"}, Cwd: ".", TimeoutSec: 30,
		InternalExec: InternalExecRelay,
	})
	if final.Status != StatusDone {
		t.Fatalf("local relay delivery in the default workspace = %s (%s), want done", final.Status, final.Error)
	}
}

// TestOrdinaryJobStillHonoursTheDefaultWorkspacesGates: the waiver belongs to
// InternalExec only. A caller's own exec job pointed at the default workspace is still
// refused by the runner list / allow_exec, so the relaxation cannot be used to run
// arbitrary commands on a worker through the machine's own project.
func TestOrdinaryJobStillHonoursTheDefaultWorkspacesGates(t *testing.T) {
	t.Parallel()
	s := defaultWorkspaceService(t, &stubWorkerRunner{})
	_, err := s.Submit(JobRequest{
		ProjectKey: config.DefaultProjectKey, Agent: "exec", Runner: "remote-w1",
		Cmd: []string{"echo", "hi"}, Cwd: ".", TimeoutSec: 30,
	})
	if err == nil || !strings.Contains(err.Error(), "is not allowed in project") {
		t.Fatalf("ordinary job in the default workspace: err = %v, want the runner refusal", err)
	}
	_, err = s.Submit(JobRequest{
		ProjectKey: config.DefaultProjectKey, Agent: "exec", Runner: "local",
		Cmd: []string{"echo", "hi"}, Cwd: ".", TimeoutSec: 30,
	})
	// The built-in default does not list the exec carrier either, so a caller's exec job
	// stops at the agent allowlist (before allow_exec): either refusal is the point.
	if err == nil || !strings.Contains(err.Error(), "not allowed in project") {
		t.Fatalf("ordinary exec job in the default workspace: err = %v, want the project gate's refusal", err)
	}
}

// TestRelayDeliveryKeepsProjectGatesForANormalProject: a session that DOES belong to a
// project keeps the old behaviour — the project's own runner list and allow_exec decide.
func TestRelayDeliveryKeepsProjectGatesForANormalProject(t *testing.T) {
	t.Parallel()
	stub := &stubWorkerRunner{}
	s := defaultWorkspaceService(t, stub)
	_, err := s.Submit(JobRequest{
		ProjectKey: "narrow", Agent: "exec", Runner: "remote-w1",
		Cmd: []string{"gofer-send", "hi"}, Cwd: ".", TimeoutSec: 30,
		InternalExec: InternalExecRelay,
	})
	// The waiver is about the built-in default workspace only: a project the operator
	// wrote keeps its own runner list.
	if err == nil || !strings.Contains(err.Error(), "is not allowed in project") {
		t.Fatalf("relay delivery into a project that does not list the runner: err = %v, want the runner refusal", err)
	}
}
