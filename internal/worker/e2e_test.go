package worker_test

import (
	"context"
	"encoding/json"
	"fmt"
	mathrand "math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/core"
	"github.com/inhere/gofer/internal/httpapi"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
	workerrunner "github.com/inhere/gofer/internal/runner/worker"
	"github.com/inhere/gofer/internal/testutil/testcmd"
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
	root  string
}

// buildHubSide stands up the serve side: a real Core (job service + hub) with a
// server.workers.w1 binding + a remote-w1 worker runner + a project allowing it.
func buildHubSide(t *testing.T) *hubSide {
	t.Helper()
	return buildHubSideAt(t, t.TempDir(), t.TempDir())
}

// buildHubSideAt is buildHubSide on an EXPLICIT project host dir + storage root, so a
// test can stand up the "next serve process" of a restart over the SAME jobstore and
// result dirs (RECOV-01 R4 adoption).
func buildHubSideAt(t *testing.T, host, root string) *hubSide {
	t.Helper()

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
				// agent allowlist before dispatch; the worker resolves/executes it);
				// acpbot for the GATE-01 permission-interaction e2e (same rule).
				AllowedAgents:  []string{"exec", "wrapper", "acpbot"},
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
	jobs := job.NewService(cfg, projReg, agentReg, runners, st, nil)
	// RECOV-01 R4: the same adoption seam production wires (core.Build), so a hub that
	// starts over a jobstore holding `recovering` rows adopts the worker's jobs back.
	hub.SetAdopter(core.NewJobAdopter(hub, jobs))

	jobsEng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(jobsEng)
	srv := httpapi.New(&cfg.Server, "server-default-token", false, jobs, jobsEng, projReg, agentReg, hub, nil, nil, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &hubSide{ts: ts, jobs: jobs, store: st, hub: hub, root: root}
}

// buildWorkerSide builds the worker's own local job service (project alpha with
// the exec agent, local runner) and a worker.Client dialing the hub.
func buildWorkerSide(t *testing.T, hubURL string) *worker.Client {
	cl, _ := buildWorkerSideJobs(t, hubURL)
	return cl
}

// buildWorkerSideJobs is buildWorkerSide that also returns the worker's local job
// service, so a P4 outcome test can find the dispatched local job's result_dir and
// seed产出 (result.json / artifacts) into it before the job finishes.
func buildWorkerSideJobs(t *testing.T, hubURL string) (*worker.Client, *job.Service) {
	t.Helper()
	return buildWorkerSideJobsOpts(t, hubURL, workerSideOpts{})
}

// workerSideOpts tunes the worker client an e2e test stands up. The zero value is the
// production wiring (default backoff/heartbeat, time-seeded jitter).
type workerSideOpts struct {
	// InitialBackoff/MaxBackoff override the reconnect backoff (0 = package default).
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	// Rng pins the reconnect jitter source, so a test can decide deterministically how
	// long the worker stays off-line after a blip (see pinSlowReconnect).
	Rng *mathrand.Rand
}

// buildWorkerSideJobsOpts is buildWorkerSideJobs with an explicit client wiring.
func buildWorkerSideJobsOpts(t *testing.T, hubURL string, opts workerSideOpts) (*worker.Client, *job.Service) {
	t.Helper()
	return buildWorkerSideURLs(t, []string{hubURL}, opts)
}

// buildWorkerSideURLs is buildWorkerSideJobsOpts with several hub addresses: the
// client's C7 failover rotates through them, which is how a restart test hands the
// worker from the dying serve process to its replacement.
func buildWorkerSideURLs(t *testing.T, hubURLs []string, opts workerSideOpts) (*worker.Client, *job.Service) {
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
	localJobs := job.NewService(cfg, projReg, agentReg, runners, st, nil)

	urls := make([]string, 0, len(hubURLs))
	for _, u := range hubURLs {
		urls = append(urls, "ws"+strings.TrimPrefix(u, "http")+"/v1/workers/connect")
	}
	cl := worker.New(worker.Config{
		WorkerID:       e2eWorkerID,
		URLs:           urls,
		Token:          e2eToken,
		Projects:       []string{"alpha"},
		Agents:         []string{"exec"},
		InitialBackoff: opts.InitialBackoff,
		MaxBackoff:     opts.MaxBackoff,
		Rng:            opts.Rng,
	}, localJobs)
	return cl, localJobs
}

// pinSlowReconnect returns a seeded jitter source whose FIRST backoff draw is a long
// wait. The worker's reconnect policy is full jitter — rand[0, cap) — so an
// un-pinned source can come back within microseconds and a test would have no chance
// to observe the host job in `recovering`. The seed is searched deterministically
// (the worker draws exactly one value per reconnect), and this returns a fresh rand
// with that seed so the client reproduces the same draw.
//
// (Legacy math/rand, not math/rand/v2: worker.Config.Rng is a *mathrand.Rand — the
// production client's own backoff source — so the fixed-seed classic API is what the
// caller depends on here.)
func pinSlowReconnect(t *testing.T, cap time.Duration) *mathrand.Rand {
	t.Helper()
	for seed := int64(1); seed < 10_000; seed++ {
		if time.Duration(mathrand.New(mathrand.NewSource(seed)).Int63n(int64(cap))) >= cap/2 {
			return mathrand.New(mathrand.NewSource(seed))
		}
	}
	t.Fatalf("no seed produced a first backoff >= %s", cap/2)
	return nil
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

// TestE2EWorkerOutcomeCaptured (P4-a full stack): a runner=worker job whose
// local execution writes a result.json + an artifact has those产出 captured on
// the WORKER (its shared job.Service runs captureOutcomes at finish), then回传 to
// the host via the Outcome WS frame. The host job's Get(id) must then carry the
// rendered command, the result.json, the artifacts清单 and source=worker:<id>.
func TestE2EWorkerOutcomeCaptured(t *testing.T) {
	hub := buildHubSide(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	cl, localJobs := buildWorkerSideJobs(t, hub.ts.URL)
	clientErr := make(chan error, 1)
	go func() { clientErr <- cl.Run(ctx) }()
	waitWorkerOnline(t, hub.hub)

	// A long-enough sleep so the test can seed产出 into the worker's local result
	// dir before the job finishes (mirrors the local outcomes_test seeding pattern).
	created := createJob(t, hub.ts, job.JobRequest{
		ProjectKey: "alpha", Agent: "exec", Runner: "remote-w1", WorkerID: e2eWorkerID,
		Cmd: testcmd.Cmd(t, "sleep", "1500ms"), Cwd: ".", TimeoutSec: 60,
	})
	if created.ID == "" {
		t.Fatal("created job has no id")
	}

	// Find the worker's LOCAL job (its id differs from the host id) and seed
	// result.json + an artifact into its result dir while it is still running.
	localDir := waitWorkerLocalResultDir(t, localJobs)
	resultJSON := `{"ok":true,"summary":"worker outcome"}`
	if err := os.WriteFile(filepath.Join(localDir, "result.json"), []byte(resultJSON), 0o600); err != nil {
		t.Fatalf("seed result.json: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(localDir, "artifacts"), 0o700); err != nil {
		t.Fatalf("mkdir artifacts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "artifacts", "out.bin"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("seed artifact: %v", err)
	}

	// Host job reaches terminal; the worker回传 the Outcome frame before the result.
	final, ok := hub.jobs.Wait(created.ID)
	if !ok {
		t.Fatalf("hub job %s not found", created.ID)
	}
	if final.Status != job.StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}

	// The host-side Get must now carry the worker-captured产出 + source.
	got, ok := hub.jobs.Get(created.ID)
	if !ok {
		t.Fatalf("host Get(%s) not found", created.ID)
	}
	if got.Source != "worker:"+e2eWorkerID {
		t.Fatalf("source = %q, want worker:%s", got.Source, e2eWorkerID)
	}
	if got.ResultJSON != resultJSON {
		t.Fatalf("result_json = %q, want %q", got.ResultJSON, resultJSON)
	}
	if got.RenderedCommand == "" || !strings.Contains(got.RenderedCommand, "sleep") {
		t.Fatalf("rendered_command = %q, want it to carry the worker-resolved sleep argv", got.RenderedCommand)
	}
	if got.ArtifactsJSON == "" || !strings.Contains(got.ArtifactsJSON, "out.bin") {
		t.Fatalf("artifacts_json = %q, want it to list out.bin", got.ArtifactsJSON)
	}

	// And the same产出 round-trips through the persisted DB row (host job evicted).
	rec, ok, err := hub.store.GetJob(created.ID)
	if err != nil || !ok {
		t.Fatalf("GetJob persisted: ok=%v err=%v", ok, err)
	}
	if rec.Source != "worker:"+e2eWorkerID || rec.ResultJSON != resultJSON || !strings.Contains(rec.ArtifactsJSON, "out.bin") {
		t.Fatalf("persisted outcome mismatch: source=%q result_json=%q artifacts=%q", rec.Source, rec.ResultJSON, rec.ArtifactsJSON)
	}

	cancel()
	select {
	case <-clientErr:
	case <-time.After(3 * time.Second):
		t.Log("worker client did not exit promptly after cancel (non-fatal)")
	}
}

// waitWorkerLocalResultDir polls the worker's local job service for the single
// dispatched job and returns its result_dir (so the test can seed产出 into it
// before it finishes). The worker has exactly one local job in flight here.
func waitWorkerLocalResultDir(t *testing.T, localJobs *job.Service) string {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		list, err := localJobs.ListJobs(job.ListOpts{Limit: 10})
		if err == nil {
			for _, j := range list {
				if j.ResultDir != "" {
					return j.ResultDir
				}
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("worker local job never appeared")
	return ""
}

// TestE2EWorkerDisconnectMidJobFailsJob (WP3 acceptance #4, full stack): a
// runner=worker job is running on the worker when the worker connection drops;
// the hub must mark the in-flight job `failed` with "worker disconnected" and the
// terminal row must be queryable from the metadata store.
func TestE2EWorkerDisconnectMidJobFailsJob(t *testing.T) {
	hub := buildHubSide(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Worker runs with a tiny reconnect backoff; we cancel its ctx mid-job to drop
	// the connection (a clean going-away close = a disconnect from the hub's view).
	workerCtx, workerCancel := context.WithCancel(ctx)
	cl := buildWorkerSide(t, hub.ts.URL)
	go func() { _ = cl.Run(workerCtx) }()
	waitWorkerOnline(t, hub.hub)

	// Submit a long-running job so it is still in flight when we drop the worker.
	created := createJob(t, hub.ts, job.JobRequest{
		ProjectKey: "alpha", Agent: "exec", Runner: "remote-w1", WorkerID: e2eWorkerID,
		Cmd: testcmd.Cmd(t, "sleep", "30s"), Cwd: ".", TimeoutSec: 60,
	})
	if created.ID == "" {
		t.Fatal("created job has no id")
	}

	// Wait until the hub-side job is running, then let the dispatch reach the worker
	// and its local `sleep` actually start (the runner registers the sink + sends
	// the dispatch synchronously once execute flips the job to running; a short
	// settle ensures we drop the worker AFTER the job is genuinely in flight, not in
	// the queued→dispatch window where RegisterSink would see the worker already
	// offline).
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if r, ok := hub.jobs.Get(created.ID); ok && r.Status == job.StatusRunning {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)

	// Drop the worker connection mid-job.
	workerCancel()

	// The hub must finish the in-flight job failed with the disconnect error.
	final, ok := hub.jobs.Wait(created.ID)
	if !ok {
		t.Fatalf("hub job %s not found", created.ID)
	}
	if final.Status != job.StatusFailed {
		t.Fatalf("status = %s, want failed (err=%s)", final.Status, final.Error)
	}
	if !strings.Contains(final.Error, "worker disconnected") {
		t.Fatalf("error = %q, want it to contain 'worker disconnected'", final.Error)
	}

	// Terminal row persisted + queryable.
	rec, ok, err := hub.store.GetJob(created.ID)
	if err != nil || !ok {
		t.Fatalf("GetJob persisted: ok=%v err=%v", ok, err)
	}
	if rec.Status != job.StatusFailed {
		t.Fatalf("persisted status = %s, want failed", rec.Status)
	}

	// Wait for the dropped worker's dispatch goroutine (which owns the child
	// process + its stderr.log handle under t.TempDir) to fully unwind before the
	// test returns and TempDir cleanup runs. Without this, RemoveAll races the
	// still-open stderr.log handle and fails on Windows (h-aii-4vqw).
	idleCtx, idleCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer idleCancel()
	if !cl.WaitIdle(idleCtx) {
		t.Fatal("worker dispatch did not exit promptly after disconnect")
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
// bound to this token) → registered{accepted:false}. With the WP3 reconnect
// supervisor, Run no longer returns on a rejection (it backs off and retries —
// the config may be fixed; §5.2). So the assertion is that w2 never becomes
// online (the binding rejection persistently blocks registration) and Run
// returns promptly only when ctx is cancelled.
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
	localJobs := job.NewService(cfg, projReg, agentReg, map[string]runner.Runner{localrunner.Name: localrunner.New()}, st, nil)

	wsURL := "ws" + strings.TrimPrefix(hub.ts.URL, "http") + "/v1/workers/connect"
	cl := worker.New(worker.Config{
		WorkerID:       "w2", // mismatched: not the worker w1's token is bound to
		URLs:           []string{wsURL},
		Token:          e2eToken,
		InitialBackoff: 20 * time.Millisecond,
		MaxBackoff:     40 * time.Millisecond,
	}, localJobs)

	runCtx, runCancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- cl.Run(runCtx) }()

	// The worker keeps retrying (rejected each time); w2 must never come online.
	if hub.hub.IsOnline("w2") {
		t.Fatal("mismatched worker should never be registered")
	}
	time.Sleep(300 * time.Millisecond)
	if hub.hub.IsOnline("w2") {
		t.Fatal("mismatched worker became online despite binding rejection")
	}

	// Cancelling the ctx must make Run return promptly (no permanent hang).
	runCancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run after cancel returned err: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
}

// TestE2EJobSurvivesHubBlip is the RECOV-01 full-stack acceptance: the WORKER
// PROCESS is never touched — only its connection dies and comes back — and the job
// that was in flight must be held as `recovering`, resume as `running`, finish
// `done`, and leave a host-side stdout log that is the command's output EXACTLY once
// (no gap from the dead connection, no duplicated chunk from the replay).
func TestE2EJobSurvivesHubBlip(t *testing.T) {
	hub := buildHubSide(t)
	// RECOV-01 is opt-in on the server side; the other e2e tests pin the pre-RECOV-01
	// behaviour (a disconnect fails the job at once), so it is enabled here only.
	hub.hub.SetRecoverWindow(30 * time.Second)
	// The blip: when stop closes, the hub closes every LIVE connection (going-away)
	// while it keeps accepting new ones. The worker process, its local job service and
	// the running job are untouched — which is the whole point of RECOV-01.
	stop := make(chan struct{})
	hub.hub.SetStop(stop)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// A slow, PINNED reconnect (~1.5-2s) keeps the job off-line long enough for the
	// test to observe `recovering` before it resumes.
	const reconnectCap = 2 * time.Second
	cl, _ := buildWorkerSideJobsOpts(t, hub.ts.URL, workerSideOpts{
		InitialBackoff: reconnectCap,
		MaxBackoff:     reconnectCap,
		Rng:            pinSlowReconnect(t, reconnectCap),
	})
	clientErr := make(chan error, 1)
	go func() { clientErr <- cl.Run(ctx) }()
	waitWorkerOnline(t, hub.hub)

	// 12 lines at 400ms ≈ 4.8s of output: the blip lands mid-stream, so the rest of
	// the output can only arrive after the reconnect.
	created := createJob(t, hub.ts, job.JobRequest{
		ProjectKey: "alpha", Agent: "exec", Runner: "remote-w1", WorkerID: e2eWorkerID,
		Cmd: testcmd.Cmd(t, "stdout-lines", "LINE", "12", "400ms"), Cwd: ".", TimeoutSec: 120,
	})
	if created.ID == "" {
		t.Fatal("created job has no id")
	}
	// Wait until the mirror is genuinely streaming before cutting the connection.
	waitForLogContains(t, hub.ts, created.ID, "LINE2", 15*time.Second)

	// --- blip the connection (NOT the worker) ---
	close(stop)

	// The host job is HELD in recovering while the same worker process is away...
	waitHostStatus(t, hub.jobs, created.ID, job.StatusRecovering, 10*time.Second)
	// ...and returns to running when it re-registers with its inflight list.
	waitHostStatus(t, hub.jobs, created.ID, job.StatusRunning, 20*time.Second)

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

	var want strings.Builder
	for i := 1; i <= 12; i++ {
		fmt.Fprintf(&want, "LINE%d\n", i)
	}
	if got := getLogs(t, hub.ts, created.ID, "stdout"); got != want.String() {
		t.Fatalf("host stdout log across the blip =\n%q\nwant exactly\n%q", got, want.String())
	}

	cancel()
	select {
	case <-clientErr:
	case <-time.After(3 * time.Second):
		t.Log("worker client did not exit promptly after cancel (non-fatal)")
	}
}

// waitHostStatus polls the hub-side job until it reports status. The RECOV-01
// recovering → running transition is short-lived, so the poll is tight.
func waitHostStatus(t *testing.T, jobs *job.Service, id, status string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if r, ok := jobs.Get(id); ok && r.Status == status {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	got, _ := jobs.Get(id)
	t.Fatalf("hub job %s never reached %q within %s (last status %q, err %q)", id, status, timeout, got.Status, got.Error)
}

// waitForLogContains polls the hub-side log endpoint until it contains substr, so a
// blip lands while the mirror is genuinely streaming.
func waitForLogContains(t *testing.T, ts *httptest.Server, id, substr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(getLogs(t, ts, id, "stdout"), substr) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("host log for %s never contained %q", id, substr)
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
