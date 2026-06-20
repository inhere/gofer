package worker_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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
	"github.com/inhere/gofer/internal/worker"
)

// interactionWrapper is a real cli-agent wrapper for the worker side: it raises a
// question interaction over the worker's LOCAL bridge HTTP API, polls until an
// answer appears, echoes it and exits 0. This is the worker analogue of the
// httpapi e2e wrapper — it drives the local job.Service the worker client
// observes, so the open propagates worker→hub.
const interactionWrapper = `#!/bin/sh
set -e
JOB_ID="$1"
curl -s -X POST -H "Authorization: Bearer $BRIDGE_TOKEN" -H "Content-Type: application/json" \
  -d '{"type":"question","prompt":"need input"}' \
  "$BRIDGE_BASE/v1/jobs/$JOB_ID/interactions" >/dev/null
i=0
while [ "$i" -lt 200 ]; do
  body=$(curl -s -H "Authorization: Bearer $BRIDGE_TOKEN" "$BRIDGE_BASE/v1/jobs/$JOB_ID/interactions")
  ans=$(printf '%s' "$body" | sed -n 's/.*"answer":"\([^"]*\)".*/\1/p')
  if [ -n "$ans" ]; then echo "ANSWER=$ans"; exit 0; fi
  i=$((i+1)); sleep 0.05
done
echo "no-answer"; exit 1
`

// workerWithBridge bundles the worker-side bridge server (so its cli-agent can
// raise interactions over HTTP) and the worker.Client dialing the hub.
type workerWithBridge struct {
	client *worker.Client
	bridge *httptest.Server
}

// buildWorkerWithBridge stands up the worker side with a local bridge HTTP server
// backed by the worker's own job.Service + a wrapper cli-agent, plus the
// worker.Client dialing hubURL. The wrapper agent reaches the bridge via the
// BRIDGE_BASE/BRIDGE_TOKEN env the local runner inherits.
func buildWorkerWithBridge(t *testing.T, hubURL string) *workerWithBridge {
	t.Helper()
	host := t.TempDir()
	root := t.TempDir()
	scriptPath := filepath.Join(host, "wrapper.sh")
	if err := os.WriteFile(scriptPath, []byte(interactionWrapper), 0o755); err != nil {
		t.Fatalf("write wrapper: %v", err)
	}

	const workerBridgeToken = "worker-bridge-token"
	cfg := &config.Config{
		Server:  config.ServerConfig{Token: workerBridgeToken},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"alpha": {
				HostPath:       host,
				AllowedAgents:  []string{"wrapper"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
		Agents: map[string]config.AgentConfig{
			"wrapper": {Type: agent.TypeCLIAgent, Command: "sh", Args: []string{scriptPath, "{{job_id}}"}},
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

	bridge := httptest.NewServer(httpapi.New(&cfg.Server, workerBridgeToken, false, localJobs, projReg, agentReg, nil, nil, nil, nil).Handler())
	t.Cleanup(bridge.Close)
	// The wrapper subprocess inherits os.Environ via the local runner; the bridge
	// base/token reach it through the environment.
	t.Setenv("BRIDGE_BASE", bridge.URL)
	t.Setenv("BRIDGE_TOKEN", workerBridgeToken)

	wsURL := "ws" + strings.TrimPrefix(hubURL, "http") + "/v1/workers/connect"
	cl := worker.New(worker.Config{
		WorkerID: e2eWorkerID,
		URLs:     []string{wsURL},
		Token:    e2eToken,
		Projects: []string{"alpha"},
		Agents:   []string{"wrapper"},
	}, localJobs)
	return &workerWithBridge{client: cl, bridge: bridge}
}

// hubInteractions GETs the hub-side job interactions array.
func hubInteractions(t *testing.T, ts *httptest.Server, id string) []map[string]any {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/jobs/"+id+"/interactions", nil)
	req.Header.Set("Authorization", "Bearer server-default-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET interactions: %v", err)
	}
	defer resp.Body.Close()
	var obj map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&obj); err != nil {
		t.Fatalf("decode interactions: %v", err)
	}
	raw, _ := obj["interactions"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, e := range raw {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// answerHub POSTs an answer to a hub-side job interaction.
func answerHub(t *testing.T, ts *httptest.Server, id, iid, answer string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"answer": answer})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/jobs/"+id+"/interactions/"+iid+"/answer", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer server-default-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST answer: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("answer status = %d", resp.StatusCode)
	}
}

// TestE2EInteractionOverWS is the WP2 acceptance gate: a worker job that triggers
// an interaction shows as pending_interaction on the HUB (existing GET
// interactions API), answering on the hub flows back over WS, and the worker job
// continues to completion.
func TestE2EInteractionOverWS(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not available")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	// buildHubSide allows the wrapper agent (the hub validates the agent allowlist
	// before dispatch; the worker resolves/executes it).
	hub := buildHubSide(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	w := buildWorkerWithBridge(t, hub.ts.URL)
	clientErr := make(chan error, 1)
	go func() { clientErr <- w.client.Run(ctx) }()
	waitWorkerOnline(t, hub.hub)

	created := createJob(t, hub.ts, job.JobRequest{
		ProjectKey: "alpha", Agent: "wrapper", Runner: "remote-w1", WorkerID: e2eWorkerID,
		Prompt: "go", Cwd: ".", TimeoutSec: 30,
	})
	if created.ID == "" {
		t.Fatal("created job has no id")
	}

	// 1) the worker job's interaction propagates worker→hub: the hub job shows a
	//    pending interaction + status pending_interaction.
	overall := time.Now().Add(25 * time.Second)
	var iid string
	for time.Now().Before(overall) {
		for _, it := range hubInteractions(t, hub.ts, created.ID) {
			if it["status"] == "pending" {
				iid, _ = it["id"].(string)
				break
			}
		}
		if iid != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if iid == "" {
		snap, _ := hub.jobs.Get(created.ID)
		t.Fatalf("no pending interaction surfaced on the hub; hub job status=%s", snap.Status)
	}
	if snap, _ := hub.jobs.Get(created.ID); snap.Status != job.StatusPendingInteraction {
		t.Fatalf("hub job status = %s, want pending_interaction", snap.Status)
	}

	// 2) answer on the hub → flows back over WS → worker job resumes → completes.
	answerHub(t, hub.ts, created.ID, iid, "hi-from-hub")

	final, ok := hub.jobs.Wait(created.ID)
	if !ok {
		t.Fatalf("hub job %s not found", created.ID)
	}
	if final.Status != job.StatusDone {
		t.Fatalf("hub job status = %s (err=%s), want done", final.Status, final.Error)
	}

	// 3) the worker's wrapper echoed the answer it read → mirrored back to the hub
	//    stdout log.
	stdout := getLogs(t, hub.ts, created.ID, "stdout")
	if !strings.Contains(stdout, "ANSWER=hi-from-hub") {
		t.Fatalf("hub stdout missing wrapper answer echo: %q", stdout)
	}

	cancel()
	select {
	case <-clientErr:
	case <-time.After(3 * time.Second):
	}
}

// TestE2ECancelOverWS: cancelling an in-flight worker job from the hub cancels
// the worker's local job (status→cancelled), and cancelling an already-done
// worker job is a stable no-op; an unknown id is a 404.
func TestE2ECancelOverWS(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	hub := buildHubSide(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cl := buildWorkerSide(t, hub.ts.URL)
	clientErr := make(chan error, 1)
	go func() { clientErr <- cl.Run(ctx) }()
	waitWorkerOnline(t, hub.hub)

	// A long-running worker job: sleep well past the test window.
	created := createJob(t, hub.ts, job.JobRequest{
		ProjectKey: "alpha", Agent: "exec", Runner: "remote-w1", WorkerID: e2eWorkerID,
		Cmd: []string{"sh", "-c", "sleep 30"}, Cwd: ".", TimeoutSec: 60,
	})
	if created.ID == "" {
		t.Fatal("created job has no id")
	}

	// Wait until the hub job is actually running (the worker accepted + started it)
	// so the cancel hits an in-flight job, not a queued one.
	waitHubStatus(t, hub, created.ID, job.StatusRunning, 10*time.Second)

	// Cancel from the hub → cancel frame over WS → worker cancels its local job.
	cancelHubJob(t, hub.ts, created.ID, http.StatusOK)

	final, ok := hub.jobs.Wait(created.ID)
	if !ok {
		t.Fatalf("hub job %s not found", created.ID)
	}
	if final.Status != job.StatusCancelled {
		t.Fatalf("hub job status = %s (err=%s), want cancelled", final.Status, final.Error)
	}

	// Cancelling an already-terminal worker job is a stable no-op (200, status
	// unchanged).
	cancelHubJob(t, hub.ts, created.ID, http.StatusOK)
	if snap, _ := hub.jobs.Get(created.ID); snap.Status != job.StatusCancelled {
		t.Fatalf("re-cancel changed status to %s, want cancelled (no-op)", snap.Status)
	}

	// Unknown id → 404.
	cancelHubJob(t, hub.ts, "no-such-job", http.StatusNotFound)

	cancel()
	select {
	case <-clientErr:
	case <-time.After(3 * time.Second):
	}
}

// TestE2ETimeoutOverWS: a worker job exceeding timeout_sec yields status timeout
// (classified by the worker's local job.Service ctx deadline; the host ctx is the
// backstop), not failed or cancelled.
func TestE2ETimeoutOverWS(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	hub := buildHubSide(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cl := buildWorkerSide(t, hub.ts.URL)
	clientErr := make(chan error, 1)
	go func() { clientErr <- cl.Run(ctx) }()
	waitWorkerOnline(t, hub.hub)

	// timeout_sec=1 but the command sleeps 30s: the worker's local timeout fires.
	created := createJob(t, hub.ts, job.JobRequest{
		ProjectKey: "alpha", Agent: "exec", Runner: "remote-w1", WorkerID: e2eWorkerID,
		Cmd: []string{"sh", "-c", "sleep 30"}, Cwd: ".", TimeoutSec: 1,
	})
	if created.ID == "" {
		t.Fatal("created job has no id")
	}

	final, ok := hub.jobs.Wait(created.ID)
	if !ok {
		t.Fatalf("hub job %s not found", created.ID)
	}
	if final.Status != job.StatusTimeout {
		t.Fatalf("hub job status = %s (err=%s), want timeout", final.Status, final.Error)
	}

	cancel()
	select {
	case <-clientErr:
	case <-time.After(3 * time.Second):
	}
}

// waitHubStatus polls until the hub job reaches want (or fails after d).
func waitHubStatus(t *testing.T, hub *hubSide, id, want string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if snap, ok := hub.jobs.Get(id); ok && snap.Status == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	snap, _ := hub.jobs.Get(id)
	t.Fatalf("hub job %s did not reach %s within %s (status=%s)", id, want, d, snap.Status)
}

// cancelHubJob POSTs a cancel for a hub job and asserts the HTTP status.
func cancelHubJob(t *testing.T, ts *httptest.Server, id string, wantStatus int) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/jobs/"+id+"/cancel", nil)
	req.Header.Set("Authorization", "Bearer server-default-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST cancel: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		t.Fatalf("cancel %s status = %d, want %d", id, resp.StatusCode, wantStatus)
	}
}
