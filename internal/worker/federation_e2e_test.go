package worker_test

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/httpapi"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
	workerrunner "github.com/inhere/gofer/internal/runner/worker"
	"github.com/inhere/gofer/internal/worker"
	"github.com/inhere/gofer/internal/wshub"
)

// hubSelector adapts the live hub registry to job.WorkerSelector exactly like the
// production wiring (core.hubWorkerSelector): the host validates/filters against the
// capabilities the worker REPORTED on register. The e2e services in e2e_test.go pass
// a nil selector (pre-federation), so the federation gates need this one to be
// exercised against a real worker.
type hubSelector struct {
	hub     *wshub.Hub
	allowed []string
}

func (h hubSelector) Candidates() []job.WorkerCandidate {
	out := make([]job.WorkerCandidate, 0, len(h.allowed))
	for _, id := range h.allowed {
		if c, ok := h.Candidate(id); ok {
			out = append(out, c)
		}
	}
	return out
}

func (h hubSelector) Candidate(workerID string) (job.WorkerCandidate, bool) {
	ws, ok := h.hub.WorkerSnapshot(workerID)
	if !ok {
		return job.WorkerCandidate{}, false
	}
	return job.WorkerCandidate{
		WorkerID:     ws.WorkerID,
		Labels:       ws.Labels,
		Projects:     ws.Projects,
		Agents:       ws.Agents,
		InFlight:     ws.InFlight,
		PtyCapable:   ws.PtyCapable,
		HeartbeatAge: time.Duration(time.Now().Unix()-ws.LastHeartbeat) * time.Second,
	}, true
}

// buildFedHubSide is buildHubSide with the federation wiring: a hub-backed
// WorkerSelector, and a host config that deliberately does NOT define the project
// the job will use ("wonly" lives only in the worker's config).
func buildFedHubSide(t *testing.T) *hubSide {
	t.Helper()
	root := t.TempDir()

	cfg := &config.Config{
		Server: config.ServerConfig{
			Token:   "server-default-token",
			Workers: map[string]config.WorkerAuthConfig{e2eWorkerID: {Token: e2eToken}},
		},
		Storage:  config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{}, // no projects at all on the host
		Runners: map[string]config.RunnerConfig{
			"remote-w1": {Type: "worker", WorkerID: e2eWorkerID},
		},
	}
	config.ApplyDefaults(cfg)

	hub := wshub.New(map[string]string{e2eWorkerID: e2eWorkerID})
	projReg := project.NewRegistry(cfg, "")
	agentReg := agent.NewRegistry(cfg)
	st, err := jobstore.Open(root + "/hub.db")
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	runners := map[string]runner.Runner{
		localrunner.Name: localrunner.New(),
		"remote-w1":      workerrunner.New("remote-w1", e2eWorkerID, hub),
	}
	jobs := job.NewService(cfg, projReg, agentReg, runners, st, hubSelector{hub: hub, allowed: []string{e2eWorkerID}})

	srv := httpapi.New(&cfg.Server, "server-default-token", false, jobs, nil, projReg, agentReg, hub, nil, nil, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &hubSide{ts: ts, jobs: jobs, store: st, hub: hub}
}

// buildFedWorkerSide builds a worker whose OWN config defines project "wonly" (the
// host has no such project) and reports it on register.
func buildFedWorkerSide(t *testing.T, hubURL string) *worker.Client {
	t.Helper()
	host := t.TempDir()
	root := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"wonly": {
				HostPath:       host,
				AllowedAgents:  []string{"exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true, // the sensitive gate stays LOCAL to the worker (decision #2)
			},
		},
	}
	config.ApplyDefaults(cfg)
	projReg := project.NewRegistry(cfg, "")
	agentReg := agent.NewRegistry(cfg)
	st, err := jobstore.Open(root + "/worker.db")
	if err != nil {
		t.Fatalf("open worker jobstore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	localJobs := job.NewService(cfg, projReg, agentReg, map[string]runner.Runner{localrunner.Name: localrunner.New()}, st, nil)

	wsURL := "ws" + strings.TrimPrefix(hubURL, "http") + "/v1/workers/connect"
	return worker.New(worker.Config{
		WorkerID: e2eWorkerID,
		URLs:     []string{wsURL},
		Token:    e2eToken,
		Projects: []string{"wonly"}, // reported capability set (P1)
		Agents:   []string{"exec"},
	}, localJobs)
}

// TestE2EWorkerOnlyProjectRoundTrip is the P3 R2 acceptance gate (G1, full stack):
// a project defined ONLY on the worker is submitted through the HTTP API against a
// host whose config has no projects at all. It must be accepted (no
// ErrUnknownProject), dispatched to the worker, executed there, and its result must
// land in the host's key-driven store and read back over /v1/jobs/<id>.
func TestE2EWorkerOnlyProjectRoundTrip(t *testing.T) {
	hub := buildFedHubSide(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cl := buildFedWorkerSide(t, hub.ts.URL)
	clientErr := make(chan error, 1)
	go func() { clientErr <- cl.Run(ctx) }()
	waitWorkerOnline(t, hub.hub)

	created := createJob(t, hub.ts, job.JobRequest{
		ProjectKey: "wonly", Agent: "exec", Runner: "remote-w1", WorkerID: e2eWorkerID,
		Cmd: []string{"echo", "worker-only-ok"}, Cwd: ".", TimeoutSec: 30,
	})
	if created.ID == "" {
		t.Fatal("created job has no id")
	}

	final, ok := hub.jobs.Wait(created.ID)
	if !ok {
		t.Fatalf("hub job %s not found", created.ID)
	}
	if final.Status != job.StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}
	if final.ExitCode != 0 {
		t.Fatalf("exit_code = %d, want 0", final.ExitCode)
	}

	// Result landing on the HOST: <storage.root>/wonly/<date>/<job_id> — the
	// worker-only project is key-driven, no host ProjectConfig needed (R2).
	if !strings.Contains(filepath.ToSlash(final.ResultDir), "/wonly/") {
		t.Fatalf("result_dir = %q, want it under the wonly project store", final.ResultDir)
	}
	if st, err := os.Stat(final.ResultDir); err != nil || !st.IsDir() {
		t.Fatalf("host result dir missing: %v", err)
	}

	// Logs mirrored back from the worker into that dir (read over HTTP).
	stdout := getLogs(t, hub.ts, created.ID, "stdout")
	if !strings.Contains(stdout, "worker-only-ok") {
		t.Fatalf("mirrored stdout = %q, want the worker's output", stdout)
	}

	// Read back through the persisted row (the /v1/jobs/<id> source of truth).
	rec, ok, err := hub.store.GetJob(created.ID)
	if err != nil || !ok {
		t.Fatalf("GetJob persisted: ok=%v err=%v", ok, err)
	}
	if rec.ProjectKey != "wonly" || rec.Status != job.StatusDone || rec.WorkerID != e2eWorkerID {
		t.Fatalf("persisted row = %+v, want wonly/done/%s", rec, e2eWorkerID)
	}

	cancel()
	select {
	case <-clientErr:
	case <-time.After(3 * time.Second):
		t.Log("worker client did not exit promptly after cancel (non-fatal)")
	}
}

// TestE2EMisconfiguredAgentRejectedOnHost is the G2 acceptance gate (full stack):
// the worker reports only the exec agent, so a job asking for a different agent is
// rejected by the HOST at submit — the dispatch never happens.
func TestE2EMisconfiguredAgentRejectedOnHost(t *testing.T) {
	hub := buildFedHubSide(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cl := buildFedWorkerSide(t, hub.ts.URL)
	go func() { _ = cl.Run(ctx) }()
	waitWorkerOnline(t, hub.hub)

	// Agent "claude" is not in the worker's reported capability set.
	_, err := hub.jobs.Submit(job.JobRequest{
		ProjectKey: "wonly", Agent: "claude", Runner: "remote-w1", WorkerID: e2eWorkerID,
		Prompt: "hi", Cwd: ".", TimeoutSec: 30,
	})
	if err == nil {
		t.Fatal("host must reject an agent the worker does not carry")
	}
	if !strings.Contains(err.Error(), "agent") || !strings.Contains(err.Error(), "claude") {
		t.Fatalf("rejection should name the missing agent: %v", err)
	}

	// And a project the worker does not carry is rejected too.
	if _, err := hub.jobs.Submit(job.JobRequest{
		ProjectKey: "ghost", Agent: "exec", Runner: "remote-w1", WorkerID: e2eWorkerID,
		Cmd: []string{"echo", "hi"}, Cwd: ".", TimeoutSec: 30,
	}); err == nil {
		t.Fatal("host must reject a project the worker does not carry")
	}
}
