package job

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
)

// The federation admission matrix (config-federation P3):
//
//	runner            | project                          | agent
//	------------------+----------------------------------+---------------------------
//	local             | must be in the host's config     | host allowlists (unchanged)
//	worker (resolved) | in the worker's reported set     | in the worker's reported set
//	worker (labels)   | filtered by selectWorker         | filtered by selectWorker
//	worker (offline)  | not checked (no capability view) | not checked
//
// The tests below drive one branch each through the REAL Submit path with a stub
// WorkerSelector standing in for the hub registry.

// wonlyCaps is a candidate for a worker that carries a project the HOST does not
// define ("wonly") — the G1 subject.
func wonlyCaps(id string) WorkerCandidate {
	return WorkerCandidate{
		WorkerID: id, HeartbeatAge: time.Second,
		Projects: []string{"wonly"},
		Agents:   []string{"exec", "codex"},
	}
}

// --- branch 1: local runner, project missing from the host config (regression) ---

func TestFedLocalUnknownProjectStillRejected(t *testing.T) {
	s := newWorkerTestService(t, t.TempDir(), &stubWorkerRunner{})
	_, err := s.Submit(JobRequest{
		ProjectKey: "wonly", Agent: "exec", Runner: "local",
		Cmd: []string{"echo", "hi"}, Cwd: ".",
	})
	if !errors.Is(err, ErrUnknownProject) {
		t.Fatalf("local runner + unknown project: got %v, want ErrUnknownProject", err)
	}
}

// --- branch 2: worker-only project, explicit worker_id → allowed (G1) ---

func TestFedWorkerOnlyProjectAllowed(t *testing.T) {
	stub := &stubWorkerRunner{}
	workers := map[string]config.WorkerAuthConfig{"w1": {Token: "tok-w1"}}
	sel := fakeSelector{cands: []WorkerCandidate{wonlyCaps("w1")}}
	s := newWorkerTestServiceSel(t, t.TempDir(), stub, workers, sel)

	final := submitAndWait(t, s, JobRequest{
		// "wonly" is NOT in the host's cfg.Projects — only the worker reports it.
		ProjectKey: "wonly", Agent: "exec", Runner: "remote-w1", WorkerID: "w1",
		Cmd: []string{"echo", "hi"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("worker-only project job: status = %s (err=%s), want done", final.Status, final.Error)
	}
	if stub.gotForward == nil || stub.gotForward.ProjectKey != "wonly" {
		t.Fatalf("Forward = %+v, want the worker-only project key forwarded verbatim", stub.gotForward)
	}
	if final.WorkerID != "w1" {
		t.Fatalf("worker_id = %q, want w1", final.WorkerID)
	}
}

// --- branch 3: agent not on the target worker → ErrAgentNotOnRunner ---

func TestFedAgentNotOnWorker(t *testing.T) {
	stub := &stubWorkerRunner{}
	workers := map[string]config.WorkerAuthConfig{"w1": {Token: "tok-w1"}}
	// The worker carries the host project "self" but only the exec agent.
	sel := fakeSelector{cands: []WorkerCandidate{
		{WorkerID: "w1", HeartbeatAge: time.Second, Projects: []string{"self"}, Agents: []string{"exec"}},
	}}
	s := newWorkerTestServiceSel(t, t.TempDir(), stub, workers, sel)

	_, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "term", Runner: "remote-w1", WorkerID: "w1",
		Prompt: "hi", Cwd: ".",
	})
	if !errors.Is(err, ErrAgentNotOnRunner) {
		t.Fatalf("got %v, want ErrAgentNotOnRunner", err)
	}
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("ErrAgentNotOnRunner must stay an ErrInvalidRequest (HTTP 400): %v", err)
	}
	if !strings.Contains(err.Error(), `"term"`) {
		t.Fatalf("error should name the missing agent: %v", err)
	}
	if stub.gotForward != nil {
		t.Fatalf("rejected job must not be dispatched: %+v", stub.gotForward)
	}
}

// A resume submission is gated on its SOURCE agent (the carrier is mechanically
// exec): the worker must carry the source agent, not just exec.
func TestFedResumeGatesOnSourceAgent(t *testing.T) {
	stub := &stubWorkerRunner{}
	workers := map[string]config.WorkerAuthConfig{"w1": {Token: "tok-w1"}}
	sel := fakeSelector{cands: []WorkerCandidate{
		{WorkerID: "w1", HeartbeatAge: time.Second, Projects: []string{"self"}, Agents: []string{"exec"}},
	}}
	s := newWorkerTestServiceSel(t, t.TempDir(), stub, workers, sel)

	_, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "remote-w1", WorkerID: "w1",
		ResumeSourceAgent: "term", // the worker does not carry "term"
		Cmd:               []string{"term", "--resume", "sess"}, Cwd: ".",
	})
	if !errors.Is(err, ErrAgentNotOnRunner) {
		t.Fatalf("resume must be gated on the source agent: got %v, want ErrAgentNotOnRunner", err)
	}
	if !strings.Contains(err.Error(), `"term"`) {
		t.Fatalf("error should name the resume SOURCE agent, not the exec carrier: %v", err)
	}
}

// --- branch 4: project not on the target worker → ErrUnknownProjectOnRunner ---

func TestFedProjectNotOnWorker(t *testing.T) {
	stub := &stubWorkerRunner{}
	workers := map[string]config.WorkerAuthConfig{"w1": {Token: "tok-w1"}}
	// The worker is online but carries a different project.
	sel := fakeSelector{cands: []WorkerCandidate{wonlyCaps("w1")}}
	s := newWorkerTestServiceSel(t, t.TempDir(), stub, workers, sel)

	_, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "remote-w1", WorkerID: "w1",
		Cmd: []string{"echo", "hi"}, Cwd: ".",
	})
	if !errors.Is(err, ErrUnknownProjectOnRunner) {
		t.Fatalf("got %v, want ErrUnknownProjectOnRunner", err)
	}
	// It is NOT the plain ErrUnknownProject (the project IS defined on the host —
	// it just is not on that worker); the HTTP layer must keep it a 400, not a 404.
	if errors.Is(err, ErrUnknownProject) {
		t.Fatalf("must not collapse into ErrUnknownProject (404): %v", err)
	}
	if !strings.Contains(err.Error(), `"self"`) {
		t.Fatalf("error should name the project: %v", err)
	}
	if stub.gotForward != nil {
		t.Fatalf("rejected job must not be dispatched: %+v", stub.gotForward)
	}
}

// --- branch 5: label auto-select with no capable worker → ErrNoCapableWorker ---

func TestFedAutoSelectNoCapableWorker(t *testing.T) {
	stub := &stubWorkerRunner{}
	workers := map[string]config.WorkerAuthConfig{
		"w1": {Token: "tok-w1"},
		"w2": {Token: "tok-w2"},
	}
	// Both carry the label but neither can run project self + agent term:
	// w1 has the project but not the agent, w2 the agent but not the project.
	sel := fakeSelector{cands: []WorkerCandidate{
		{WorkerID: "w1", Labels: []string{"gpu"}, HeartbeatAge: time.Second, Projects: []string{"self"}, Agents: []string{"exec"}},
		{WorkerID: "w2", Labels: []string{"gpu"}, HeartbeatAge: time.Second, Projects: []string{"other"}, Agents: []string{"exec", "term"}},
	}}
	s := newWorkerTestServiceSel(t, t.TempDir(), stub, workers, sel)

	_, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "term", Runner: "pool-w",
		WorkerLabels: []string{"gpu"},
		Prompt:       "hi", Cwd: ".",
	})
	if !errors.Is(err, ErrNoCapableWorker) {
		t.Fatalf("got %v, want ErrNoCapableWorker", err)
	}
	// It keeps satisfying ErrNoEligibleWorker so the HTTP mapping stays 503.
	if !errors.Is(err, ErrNoEligibleWorker) {
		t.Fatalf("ErrNoCapableWorker must wrap ErrNoEligibleWorker (HTTP 503): %v", err)
	}
	if !strings.Contains(err.Error(), `project="self"`) || !strings.Contains(err.Error(), `agent="term"`) {
		t.Fatalf("error must name the required project+agent: %v", err)
	}
	if stub.gotForward != nil {
		t.Fatalf("rejected job must not be dispatched: %+v", stub.gotForward)
	}
}

// --- branch 6: label auto-select picks the capable worker ---

func TestFedAutoSelectPicksCapableWorker(t *testing.T) {
	stub := &stubWorkerRunner{}
	workers := map[string]config.WorkerAuthConfig{
		"w1": {Token: "tok-w1"},
		"w2": {Token: "tok-w2"},
	}
	// w1 is idle (would win on load) but cannot run the agent; w2 is busier yet
	// capable → capability beats load.
	sel := fakeSelector{cands: []WorkerCandidate{
		{WorkerID: "w1", Labels: []string{"gpu"}, InFlight: 0, HeartbeatAge: time.Second, Projects: []string{"self"}, Agents: []string{"exec"}},
		{WorkerID: "w2", Labels: []string{"gpu"}, InFlight: 7, HeartbeatAge: time.Second, Projects: []string{"self"}, Agents: []string{"exec", "term"}},
	}}
	s := newWorkerTestServiceSel(t, t.TempDir(), stub, workers, sel)

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "term", Runner: "pool-w",
		WorkerLabels: []string{"gpu"},
		Prompt:       "hi", Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}
	if final.WorkerID != "w2" {
		t.Fatalf("worker_id = %q, want w2 (the only worker carrying project+agent)", final.WorkerID)
	}
	if stub.gotForward == nil || stub.gotForward.WorkerID != "w2" {
		t.Fatalf("Forward = %+v, want w2", stub.gotForward)
	}
}

// --- branch 7: the worker is not resolvable (online=false) → no capability gate ---

func TestFedUnresolvableWorkerFallsThrough(t *testing.T) {
	// The selector knows nobody: capabilitiesFor returns online=false, so admission
	// must fall through to the pre-federation behaviour (the job is accepted here and
	// fails later at dispatch if the worker never shows up) — NOT rejected at submit.
	t.Run("explicit worker_id, worker offline", func(t *testing.T) {
		stub := &stubWorkerRunner{}
		workers := map[string]config.WorkerAuthConfig{"w1": {Token: "tok-w1"}}
		s := newWorkerTestServiceSel(t, t.TempDir(), stub, workers, fakeSelector{})
		final := submitAndWait(t, s, JobRequest{
			ProjectKey: "self", Agent: "exec", Runner: "remote-w1", WorkerID: "w1",
			Cmd: []string{"echo", "hi"}, Cwd: ".", TimeoutSec: 30,
		})
		if final.Status != StatusDone {
			t.Fatalf("offline worker must not be rejected at submit: status=%s err=%s", final.Status, final.Error)
		}
	})

	t.Run("no selector wired at all", func(t *testing.T) {
		stub := &stubWorkerRunner{}
		s := newWorkerTestService(t, t.TempDir(), stub) // nil selector
		final := submitAndWait(t, s, JobRequest{
			ProjectKey: "self", Agent: "exec", Runner: "remote-w1", WorkerID: "w1",
			Cmd: []string{"echo", "hi"}, Cwd: ".", TimeoutSec: 30,
		})
		if final.Status != StatusDone {
			t.Fatalf("nil selector must not reject: status=%s err=%s", final.Status, final.Error)
		}
	})

	t.Run("worker-only project with an offline worker is still accepted", func(t *testing.T) {
		// No capability view ⇒ nothing to validate the project against; the job is
		// accepted and the worker (or its absence) decides. Proves the G1 branch does
		// not depend on the capability gate having run.
		stub := &stubWorkerRunner{}
		workers := map[string]config.WorkerAuthConfig{"w1": {Token: "tok-w1"}}
		s := newWorkerTestServiceSel(t, t.TempDir(), stub, workers, fakeSelector{})
		final := submitAndWait(t, s, JobRequest{
			ProjectKey: "wonly", Agent: "exec", Runner: "remote-w1", WorkerID: "w1",
			Cmd: []string{"echo", "hi"}, Cwd: ".", TimeoutSec: 30,
		})
		if final.Status != StatusDone {
			t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
		}
	})
}

// --- branch 8: local runner, normal project/agent (regression) ---

func TestFedLocalRunnerUnaffected(t *testing.T) {
	stub := &stubWorkerRunner{}
	workers := map[string]config.WorkerAuthConfig{"w1": {Token: "tok-w1"}}
	// A worker that carries NEITHER the project nor the agent: a local job must not
	// consult it at all (capabilitiesFor(local) = the host's own config).
	sel := fakeSelector{cands: []WorkerCandidate{wonlyCaps("w1")}}
	s := newWorkerTestServiceSel(t, t.TempDir(), stub, workers, sel)

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"echo", "hi"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("local job: status = %s (err=%s), want done", final.Status, final.Error)
	}
}

// --- worker-only project keys are request-supplied: they must stay path-safe ---

func TestFedWorkerOnlyProjectKeyMustBeSafe(t *testing.T) {
	workers := map[string]config.WorkerAuthConfig{"w1": {Token: "tok-w1"}}
	sel := fakeSelector{cands: []WorkerCandidate{wonlyCaps("w1")}}
	s := newWorkerTestServiceSel(t, t.TempDir(), &stubWorkerRunner{}, workers, sel)

	for _, key := range []string{"../escape", "a/b", `..\win`, "..", ".hidden", ""} {
		_, err := s.Submit(JobRequest{
			ProjectKey: key, Agent: "exec", Runner: "remote-w1", WorkerID: "w1",
			Cmd: []string{"echo", "hi"}, Cwd: ".",
		})
		if !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("project key %q: got %v, want ErrInvalidRequest (unsafe path segment)", key, err)
		}
	}
}

// --- R2: a worker-only project's results must still land + read back on the host ---

// TestFedWorkerOnlyProjectResultLandsGlobalStore (R2, storage.root SET): the host
// has no ProjectConfig for the project, yet the job's result dir is created under
// the key-driven global store (<storage.root>/<project_key>/<job_id>), the logs are
// written there and the terminal job reads back from the metadata store.
func TestFedWorkerOnlyProjectResultLandsGlobalStore(t *testing.T) {
	root := t.TempDir()
	stub := &stubWorkerRunner{}
	workers := map[string]config.WorkerAuthConfig{"w1": {Token: "tok-w1"}}
	sel := fakeSelector{cands: []WorkerCandidate{wonlyCaps("w1")}}
	s := newWorkerTestServiceSel(t, root, stub, workers, sel) // Storage.Root = root

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "wonly", Agent: "exec", Runner: "remote-w1", WorkerID: "w1",
		Cmd: []string{"echo", "hi"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}
	// <storage.root>/<project_key>/<date>/<job_id> (the store adds the date shard).
	wantBase := filepath.Join(root, "wonly")
	assertResultDirUnder(t, final, wantBase)
	if st, err := os.Stat(final.ResultDir); err != nil || !st.IsDir() {
		t.Fatalf("result dir not created on disk: %v", err)
	}
	// The host mirrors the remote job's logs into that dir — the writer must have
	// opened there (an empty placeholder proj would have scattered it elsewhere).
	if _, err := os.Stat(filepath.Join(final.ResultDir, "stdout.log")); err != nil {
		t.Fatalf("stdout.log not in the worker-only project result dir: %v", err)
	}
	// Read back: in-memory Get + the persisted DB row.
	if got, ok := s.Get(final.ID); !ok || got.ProjectKey != "wonly" || got.ResultDir != final.ResultDir {
		t.Fatalf("Get(%s) = %+v, ok=%v", final.ID, got, ok)
	}
	rec, ok, err := s.meta.GetJob(final.ID)
	if err != nil || !ok {
		t.Fatalf("GetJob(%s): ok=%v err=%v", final.ID, ok, err)
	}
	if rec.ProjectKey != "wonly" || rec.ResultDir != final.ResultDir || rec.Status != StatusDone {
		t.Fatalf("persisted row = %+v, want project wonly / dir %s / done", rec, final.ResultDir)
	}
}

// assertResultDirUnder checks the job's result dir lives under base (the store adds
// a <date> shard between the base and the job id).
func assertResultDirUnder(t *testing.T, final JobResult, base string) {
	t.Helper()
	if !strings.HasPrefix(final.ResultDir, base+string(filepath.Separator)) {
		t.Fatalf("result_dir = %q, want it under %q", final.ResultDir, base)
	}
	if filepath.Base(final.ResultDir) != final.ID {
		t.Fatalf("result_dir = %q, want it to end in the job id %q", final.ResultDir, final.ID)
	}
}

// TestFedWorkerOnlyProjectResultLandsConfigDir (R2, storage.root UNSET): without a
// global store the host has NO path for a project it does not define — an empty
// placeholder ProjectConfig would make ResultBaseDir fall back to <host_path>/… with
// host_path="", i.e. the serve process CWD. The synthesized placeholder must instead
// keep the results under the config dir, keyed by project.
func TestFedWorkerOnlyProjectResultLandsConfigDir(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv(config.EnvConfigDir, cfgDir) // ConfigDir() → cfgDir (no ~/.config writes)

	stub := &stubWorkerRunner{}
	s := newRootlessWorkerService(t, stub, fakeSelector{cands: []WorkerCandidate{wonlyCaps("w1")}})

	cwdBefore, _ := os.Getwd()
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "wonly", Agent: "exec", Runner: "remote-w1", WorkerID: "w1",
		Cmd: []string{"echo", "hi"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}
	// <config-dir>/remote/<project_key>/<date>/<job_id>.
	assertResultDirUnder(t, final, filepath.Join(cfgDir, workerOnlyStoreSubdir, "wonly"))
	if _, err := os.Stat(filepath.Join(final.ResultDir, "stdout.log")); err != nil {
		t.Fatalf("stdout.log not in the worker-only project result dir: %v", err)
	}
	// Nothing was scattered into the process CWD (the empty-proj failure mode).
	if strings.HasPrefix(final.ResultDir, cwdBefore+string(filepath.Separator)) {
		t.Fatalf("result dir %q landed under the process cwd %q", final.ResultDir, cwdBefore)
	}
	if got, ok := s.Get(final.ID); !ok || got.ResultDir != final.ResultDir {
		t.Fatalf("Get(%s) = %+v, ok=%v", final.ID, got, ok)
	}
}

// newRootlessWorkerService mirrors newWorkerTestServiceSel but leaves storage.root
// EMPTY (the default deployment shape: results live under each project's exchange
// dir) so the worker-only project has no key-driven store to fall back on.
func newRootlessWorkerService(t *testing.T, stub runner.Runner, sel WorkerSelector) *Service {
	t.Helper()
	host := t.TempDir()
	cfg := &config.Config{
		Server: config.ServerConfig{
			Workers: map[string]config.WorkerAuthConfig{"w1": {Token: "tok-w1"}},
		},
		Projects: map[string]config.ProjectConfig{
			"self": {HostPath: host, AllowedRunners: []string{"local", "remote-w1"}, AllowExec: true},
		},
		Runners: map[string]config.RunnerConfig{
			"remote-w1": {Type: "worker", WorkerID: "w1"},
		},
	}
	config.ApplyDefaults(cfg)
	cfg.Storage.Root = "" // explicit: no global store.
	projReg := project.NewRegistry(cfg, "")
	agentReg := agent.NewRegistry(cfg)
	meta, err := jobstore.Open(filepath.Join(t.TempDir(), "gofer.db"))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	runners := map[string]runner.Runner{
		localrunner.Name: localrunner.New(),
		"remote-w1":      stub,
	}
	return NewService(cfg, projReg, agentReg, runners, meta, sel)
}
