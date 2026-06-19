package worker_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

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

const (
	e2eToken    = "tok-w1"
	e2eWorkerID = "w1"
)

// hubSide bundles the in-process serve (hub) side of the e2e: the HTTP server,
// its job service (whose persisted rows we assert) and the test http URL.
type hubSide struct {
	ts    *httptest.Server
	jobs  *job.Service
	store *jobstore.Store
	hub   *wshub.Hub
}

// buildHubSide stands up the serve side: a real Core (job service + hub) with a
// server.workers.w1 binding + a remote-w1 worker runner + a project allowing it.
func buildHubSide(t *testing.T) *hubSide {
	t.Helper()
	host := t.TempDir()
	root := t.TempDir()

	cfg := &config.Config{
		Server: config.ServerConfig{
			Token:   "server-default-token",
			Workers: map[string]config.WorkerAuthConfig{e2eWorkerID: {Token: e2eToken}},
		},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"alpha": {
				HostPath: host,
				// wrapper is allowed for the WP2 interaction e2e (the hub validates the
				// agent allowlist before dispatch; the worker resolves/executes it).
				AllowedAgents:  []string{"exec", "wrapper"},
				AllowedRunners: []string{"remote-w1"},
				AllowExec:      true,
			},
		},
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
	jobs := job.NewService(cfg, projReg, agentReg, runners, st)

	srv := httpapi.New(&cfg.Server, "server-default-token", false, jobs, projReg, agentReg, hub)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &hubSide{ts: ts, jobs: jobs, store: st, hub: hub}
}

// buildWorkerSide builds the worker's own local job service (project alpha with
// the exec agent, local runner) and a worker.Client dialing the hub.
func buildWorkerSide(t *testing.T, hubURL string) *worker.Client {
	t.Helper()
	host := t.TempDir()
	root := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"alpha": {
				HostPath:       host,
				AllowedAgents:  []string{"exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
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
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	localJobs := job.NewService(cfg, projReg, agentReg, runners, st)

	wsURL := "ws" + strings.TrimPrefix(hubURL, "http") + "/v1/workers/connect"
	return worker.New(worker.Config{
		WorkerID: e2eWorkerID,
		URL:      wsURL,
		Token:    e2eToken,
		Projects: []string{"alpha"},
		Agents:   []string{"exec"},
	}, localJobs)
}

// createJob POSTs a job via the HTTP API and returns the created JobResult.
func createJob(t *testing.T, ts *httptest.Server, req job.JobRequest) job.JobResult {
	t.Helper()
	body, _ := json.Marshal(req)
	httpReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/jobs", strings.NewReader(string(body)))
	httpReq.Header.Set("Authorization", "Bearer server-default-token")
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("POST /v1/jobs: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		t.Fatalf("create job status = %d", resp.StatusCode)
	}
	var out job.JobResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode job: %v", err)
	}
	return out
}

// TestE2ERemoteExecution is the WP1 acceptance gate: a runner=worker job
// executes on the in-process worker, its logs mirror back to the hub, the
// result is correct and jobs.worker_id is persisted.
func TestE2ERemoteExecution(t *testing.T) {
	hub := buildHubSide(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Start the worker client in-process and wait for it to register.
	cl := buildWorkerSide(t, hub.ts.URL)
	clientErr := make(chan error, 1)
	go func() { clientErr <- cl.Run(ctx) }()
	waitWorkerOnline(t, hub.hub)

	// Submit a runner=worker job that echoes "hi" on the worker.
	created := createJob(t, hub.ts, job.JobRequest{
		ProjectKey: "alpha", Agent: "exec", Runner: "remote-w1", WorkerID: e2eWorkerID,
		Cmd: []string{"echo", "hi"}, Cwd: ".", TimeoutSec: 30,
	})
	if created.ID == "" {
		t.Fatal("created job has no id")
	}

	// Wait for the hub-side job to reach a terminal state.
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
	if final.WorkerID != e2eWorkerID {
		t.Fatalf("worker_id = %q, want %q", final.WorkerID, e2eWorkerID)
	}

	// Logs mirrored back to the hub: read via the HTTP /logs/stdout path (the
	// same store files the local runner uses — proves the mirror writes to the
	// host job's stdout.log unchanged).
	stdout := getLogs(t, hub.ts, created.ID, "stdout")
	if !strings.Contains(stdout, "hi") {
		t.Fatalf("hub stdout log missing mirrored output: %q", stdout)
	}

	// Logs also mirror through the SSE /stream path (C4): the stream replays the
	// mirrored log bytes and a terminal status + end (the job is already terminal
	// here, so the stream replays once and closes).
	sse := getStream(t, hub.ts, created.ID)
	if !strings.Contains(sse, "hi") {
		t.Fatalf("SSE stream missing mirrored log output: %q", sse)
	}
	if !strings.Contains(sse, "event: end") {
		t.Fatalf("SSE stream missing terminal end event: %q", sse)
	}

	// jobs.worker_id persisted + queryable from the metadata store.
	rec, ok, err := hub.store.GetJob(created.ID)
	if err != nil || !ok {
		t.Fatalf("GetJob persisted: ok=%v err=%v", ok, err)
	}
	if rec.WorkerID != e2eWorkerID {
		t.Fatalf("persisted worker_id = %q, want %q", rec.WorkerID, e2eWorkerID)
	}

	cancel()
	select {
	case <-clientErr:
	case <-time.After(3 * time.Second):
		t.Log("worker client did not exit promptly after cancel (non-fatal)")
	}
}

// TestE2EWrongTokenRejected: dialing with a bad token → handshake 401 (the WS
// route's bare 401 before upgrade).
func TestE2EWrongTokenRejected(t *testing.T) {
	hub := buildHubSide(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(hub.ts.URL, "http") + "/v1/workers/connect"
	_, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer wrong-token"}},
	})
	if err == nil {
		t.Fatal("dial with wrong token should fail the handshake")
	}
}

// TestE2EWorkerIDBindingMismatch: a valid token but register.worker_id=w2 (not
// bound to this token) → registered{accepted:false}, so the client's Run returns
// a "register rejected" error.
func TestE2EWorkerIDBindingMismatch(t *testing.T) {
	hub := buildHubSide(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	// Build a worker whose register announces w2 while authenticating with w1's
	// token: the hub binds w1's token to caller "w1", so register w2 mismatches.
	host := t.TempDir()
	root := t.TempDir()
	cfg := &config.Config{
		Storage:  config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{"alpha": {HostPath: host, AllowedAgents: []string{"exec"}, AllowExec: true}},
	}
	config.ApplyDefaults(cfg)
	projReg := project.NewRegistry(cfg, "")
	agentReg := agent.NewRegistry(cfg)
	st, err := jobstore.Open(root + "/worker.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	localJobs := job.NewService(cfg, projReg, agentReg, map[string]runner.Runner{localrunner.Name: localrunner.New()}, st)

	wsURL := "ws" + strings.TrimPrefix(hub.ts.URL, "http") + "/v1/workers/connect"
	cl := worker.New(worker.Config{
		WorkerID: "w2", // mismatched: not the worker w1's token is bound to
		URL:      wsURL,
		Token:    e2eToken,
	}, localJobs)

	err = cl.Run(ctx)
	if err == nil || !strings.Contains(err.Error(), "register rejected") {
		t.Fatalf("expected register rejected, got %v", err)
	}
}

// waitWorkerOnline polls the hub's registry until the worker has dialed +
// registered (deterministic: hub.IsOnline flips true once the registered ack is
// sent). This removes the dispatch-before-register race.
func waitWorkerOnline(t *testing.T, hub *wshub.Hub) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if hub.IsOnline(e2eWorkerID) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("worker did not come online within 5s")
}

// getLogs reads the job's stdout/stderr via the HTTP /logs endpoint.
func getLogs(t *testing.T, ts *httptest.Server, id, stream string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/jobs/"+id+"/logs/"+stream, nil)
	req.Header.Set("Authorization", "Bearer server-default-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET logs: %v", err)
	}
	defer resp.Body.Close()
	buf := new(strings.Builder)
	_, _ = copyTo(buf, resp.Body)
	return buf.String()
}

// getStream reads the job's SSE /stream to completion (the job is terminal so
// the server replays + closes). A short read deadline guards against a hang.
func getStream(t *testing.T, ts *httptest.Server, id string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/v1/jobs/"+id+"/stream", nil)
	req.Header.Set("Authorization", "Bearer server-default-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET stream: %v", err)
	}
	defer resp.Body.Close()
	buf := new(strings.Builder)
	_, _ = copyTo(buf, resp.Body)
	return buf.String()
}

func copyTo(dst *strings.Builder, src interface{ Read([]byte) (int, error) }) (int, error) {
	total := 0
	b := make([]byte, 4096)
	for {
		n, err := src.Read(b)
		if n > 0 {
			dst.Write(b[:n])
			total += n
		}
		if err != nil {
			return total, nil
		}
	}
}
