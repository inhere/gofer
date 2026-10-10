package worker_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/testutil/wait"
	"github.com/inhere/gofer/internal/worker"
	"github.com/inhere/gofer/internal/wshub"
)

// acpStreamReader follows one job's /v1/jobs/{id}/acp/stream in the background and
// keeps every decoded `acp` event plus whether the terminal `end` event arrived.
type acpStreamReader struct {
	mu     sync.Mutex
	events []map[string]any
	ended  bool
}

// openACPStream opens the hub's structured stream for id. The reader goroutine is
// stopped and joined in t.Cleanup, which runs before the hub's httptest server
// closes (registered later, so it runs first) — an open SSE request would otherwise
// block that Close.
func openACPStream(t *testing.T, ts *httptest.Server, id string) *acpStreamReader {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/v1/jobs/"+id+"/acp/stream", nil)
	req.Header.Set("Authorization", "Bearer server-default-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("GET acp stream: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		cancel()
		t.Fatalf("acp stream status = %d", resp.StatusCode)
	}
	r := &acpStreamReader{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 64<<10), 4<<20)
		event := ""
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				r.mu.Lock()
				switch event {
				case "acp":
					var ev map[string]any
					if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev) == nil {
						r.events = append(r.events, ev)
					}
				case "end":
					r.ended = true
				}
				r.mu.Unlock()
			}
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return r
}

// has reports whether any received event matches kind and contains substr in text.
func (r *acpStreamReader) has(kind, substr string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ev := range r.events {
		text, _ := ev["text"].(string)
		if ev["kind"] == kind && strings.Contains(text, substr) {
			return true
		}
	}
	return false
}

func (r *acpStreamReader) snapshot() ([]map[string]any, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]map[string]any(nil), r.events...), r.ended
}

// postHub POSTs a JSON body to a hub path and requires 200.
func postHub(t *testing.T, ts *httptest.Server, path string, body any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer server-default-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST %s status = %d body=%s", path, resp.StatusCode, msg)
	}
}

func waitHostAwaitingTurn(t *testing.T, jobs *job.Service, id string, turn int) {
	t.Helper()
	wait.For(t, 20*time.Second, "hub session awaiting input", func() (bool, any) {
		res, ok := jobs.Get(id)
		if ok && job.IsTerminal(res.Status) {
			t.Fatalf("hub session ended early: status=%s err=%s", res.Status, res.Error)
		}
		return ok && res.Status == job.StatusAwaitingInput && res.TurnNo == turn, res.Status
	})
}

// sessionSelector is the hub-backed worker selector a remote ACP session needs on the
// hub's job service: exact-worker capability lookup for admission plus the session
// command transport for `say`/`end` (production wires core's equivalent).
type sessionSelector struct{ hub *wshub.Hub }

func (s sessionSelector) Candidates() []job.WorkerCandidate { return nil }

func (s sessionSelector) Candidate(workerID string) (job.WorkerCandidate, bool) {
	ws, ok := s.hub.WorkerSnapshot(workerID)
	if !ok {
		return job.WorkerCandidate{}, false
	}
	return job.WorkerCandidate{
		WorkerID: ws.WorkerID, Labels: ws.Labels, Projects: ws.Projects, Agents: ws.Agents,
		ProtocolVersion: ws.ProtocolVersion, ProtocolKnown: ws.ProtocolVersion > 0, InFlight: ws.InFlight,
	}, true
}

func (s sessionSelector) SendSessionCommand(workerID, jobID, cmdID, action, prompt string) error {
	return s.hub.SendSessionCommand(workerID, jobID, cmdID, action, prompt)
}

func (s sessionSelector) IsWorkerOnline(workerID string) bool { return s.hub.IsOnline(workerID) }

// TestE2EWorkerACPSessionStreamsStructuredEvents is the gofer-e2x7 regression: an
// ACP continuous session running ON a worker used to leave the hub's
// /v1/jobs/{id}/acp/stream empty (the acp.jsonl only existed on the worker), so the
// web workbench sat on "loading structured record" forever. The worker now mirrors
// the file over the "acp" log stream: each turn's prompt and reply reach the open
// stream live, a `say` turn shows up on the same stream, and stdout.log stays free of
// JSONL.
func TestE2EWorkerACPSessionStreamsStructuredEvents(t *testing.T) {
	hub := buildHubSideSel(t, t.TempDir(), t.TempDir(), func(h *wshub.Hub) job.WorkerSelector {
		return sessionSelector{hub: h}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cli, _ := buildWorkerWithACPAgent(t, hub.ts.URL, nil, acptest.Options{EchoPrompt: true})
	clientErr := worker.StartClient(t, ctx, cli)
	waitWorkerOnline(t, hub.hub)

	created := createJob(t, hub.ts, job.JobRequest{
		ProjectKey: "alpha", Agent: "acpbot", Runner: "remote-w1", WorkerID: e2eWorkerID,
		Prompt: "first turn", Cwd: ".", Session: true, TimeoutSec: 120, IdleTimeoutSec: 120,
	})
	if created.ID == "" {
		t.Fatal("created job has no id")
	}
	stream := openACPStream(t, hub.ts, created.ID)

	wait.For(t, 20*time.Second, "turn 1 prompt + reply on the hub acp stream", func() (bool, any) {
		events, _ := stream.snapshot()
		return stream.has("prompt", "first turn") && stream.has("message", "ECHO:first turn"), events
	})
	waitHostAwaitingTurn(t, hub.jobs, created.ID, 1)

	postHub(t, hub.ts, "/v1/jobs/"+created.ID+"/say", map[string]string{"message": "second turn"})
	wait.For(t, 20*time.Second, "say turn prompt + reply on the SAME open stream", func() (bool, any) {
		events, _ := stream.snapshot()
		return stream.has("prompt", "second turn") && stream.has("message", "ECHO:second turn"), events
	})
	waitHostAwaitingTurn(t, hub.jobs, created.ID, 2)

	postHub(t, hub.ts, "/v1/jobs/"+created.ID+"/end", map[string]string{})
	final, ok := hub.jobs.Wait(created.ID)
	if !ok || final.Status != job.StatusDone {
		t.Fatalf("hub session final: ok=%v status=%s err=%s", ok, final.Status, final.Error)
	}
	wait.For(t, 10*time.Second, "acp stream end event", func() (bool, any) {
		events, ended := stream.snapshot()
		return ended, events
	})
	events, _ := stream.snapshot()
	for _, ev := range events {
		if ev["kind"] == "notice" {
			t.Fatalf("a v%s worker must not get the mirror notice: %v", "22+", ev)
		}
	}

	// The mirror lands in the HOST job's artifacts/acp.jsonl, never in stdout.log.
	if b, err := os.ReadFile(runner.ACPArtifactPath(final.ResultDir)); err != nil || !bytes.Contains(b, []byte(`"second turn"`)) {
		t.Fatalf("hub acp.jsonl mirror: err=%v content=%q", err, b)
	}
	if stdout := getLogs(t, hub.ts, created.ID, "stdout"); strings.Contains(stdout, `"t":"prompt"`) {
		t.Fatalf("acp records leaked into the hub stdout log: %q", stdout)
	}

	stopWorkerClient(t, cli, cancel, clientErr)
}
