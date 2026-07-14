package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
)

// fakeReloader stands in for the serve-side hub adapter: the handler is a mapping
// from an outcome onto a status code, so the tests drive the outcomes directly and
// never need a live hub (the hub's own error taxonomy is covered in wshub).
type fakeReloader struct {
	out       WorkerReloadOutcome
	gotID     string
	gotReason string
	gotWait   time.Duration // deadline the handler imposed, rounded to seconds
	called    int
}

func (f *fakeReloader) ReloadWorker(ctx context.Context, workerID, reason string) WorkerReloadOutcome {
	f.called++
	f.gotID, f.gotReason = workerID, reason
	if dl, ok := ctx.Deadline(); ok {
		f.gotWait = time.Until(dl).Round(time.Second)
	}
	return f.out
}

// fakeHub satisfies workerHub so the WS routes get mounted alongside the reload
// route: /v1/workers/connect (static) and /v1/workers/{id}/reload (param) share a
// path segment, and this is what proves the router accepts both.
type fakeHub struct{}

func (fakeHub) Accept(http.ResponseWriter, *http.Request, string) {}
func (fakeHub) LiveInstance(string) (string, bool)                { return "", false }

// newReloadServer builds a server with one REGISTERED worker (w1) and an injected
// reloader, so the tests can separate "unknown worker" from "known but unreachable".
func newReloadServer(t *testing.T, hub workerHub) (*Server, *fakeReloader) {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Server: config.ServerConfig{
			Token:   testToken,
			Workers: map[string]config.WorkerAuthConfig{"w1": {}},
		},
		Storage: config.StorageConfig{Root: root},
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	jobs := job.NewService(cfg, projects, agents, runners, openTestStore(t, root), nil)
	eng := workflow.NewEngine(jobs)
	s := New(&cfg.Server, testToken, false, jobs, eng, projects, agents, hub, nil, nil, nil)
	fr := &fakeReloader{}
	s.SetWorkerReloader(fr)
	return s, fr
}

func TestWorkerReloadApplied(t *testing.T) {
	s, fr := newReloadServer(t, nil)
	fr.out = WorkerReloadOutcome{
		Status: WorkerReloadApplied,
		Caps: WorkerCaps{
			Agents:        []string{"exec", "tty-demo"},
			AgentCaps:     []AgentBrief{{Key: "tty-demo", Type: "pty", Interactive: true}},
			MaxConcurrent: 3,
		},
	}
	resp := do(t, s, http.MethodPost, "/v1/workers/w1/reload", testToken, map[string]any{"reason": "operator"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body workerReloadResp
	decode(t, resp, &body)
	if !body.Applied || body.WorkerID != "w1" {
		t.Fatalf("body = %+v, want applied w1", body)
	}
	if body.Caps == nil || len(body.Caps.Agents) != 2 || body.Caps.MaxConcurrent != 3 {
		t.Fatalf("caps = %+v, want the worker's new snapshot", body.Caps)
	}
	// Empty capability lists must serialise as [] (a client that renders `null` as
	// "unknown" would report a worker with no projects as a broken one).
	if body.Caps.Projects == nil || body.Caps.Labels == nil {
		t.Fatalf("caps slices must be non-nil: %+v", body.Caps)
	}
	if fr.gotReason != "operator" {
		t.Errorf("reason passed to the hub = %q, want operator", fr.gotReason)
	}
	if fr.gotWait != defaultWorkerReloadWait {
		t.Errorf("default wait = %s, want %s", fr.gotWait, defaultWorkerReloadWait)
	}
}

// TestWorkerReloadRejectedKeepsWorkerReason is the whole point of the synchronous
// endpoint: the worker's own explanation must arrive at the caller UNCHANGED.
func TestWorkerReloadRejectedKeepsWorkerReason(t *testing.T) {
	const workerReason = "load worker config: yaml: line 7: mapping values are not allowed in this context"
	s, fr := newReloadServer(t, nil)
	fr.out = WorkerReloadOutcome{Status: WorkerReloadRejected, Detail: workerReason}

	resp := do(t, s, http.MethodPost, "/v1/workers/w1/reload", testToken, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	var body workerReloadResp
	decode(t, resp, &body)
	if body.Applied {
		t.Error("applied must be false for a rejected reload")
	}
	if body.Error != workerReason {
		t.Fatalf("error = %q, want the worker's reason verbatim (%q)", body.Error, workerReason)
	}
}

func TestWorkerReloadOffline(t *testing.T) {
	s, fr := newReloadServer(t, nil)
	fr.out = WorkerReloadOutcome{Status: WorkerReloadOffline, Detail: "worker offline"}

	resp := do(t, s, http.MethodPost, "/v1/workers/w1/reload", testToken, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (offline is a state conflict, not a server fault)", resp.StatusCode)
	}
	var body workerReloadResp
	decode(t, resp, &body)
	if body.Applied || body.Error == "" {
		t.Fatalf("body = %+v, want applied=false with an error", body)
	}
}

func TestWorkerReloadTooOld(t *testing.T) {
	const hubMsg = "worker protocol too old to reload config: worker w1 speaks protocol v2, config reload needs v3 — upgrade and restart it"
	s, fr := newReloadServer(t, nil)
	fr.out = WorkerReloadOutcome{Status: WorkerReloadTooOld, Detail: hubMsg}

	resp := do(t, s, http.MethodPost, "/v1/workers/w1/reload", testToken, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	var body workerReloadResp
	decode(t, resp, &body)
	if body.Detail != hubMsg {
		t.Fatalf("detail = %q, want the version detail passed through", body.Detail)
	}
}

func TestWorkerReloadTimeout(t *testing.T) {
	s, fr := newReloadServer(t, nil)
	fr.out = WorkerReloadOutcome{Status: WorkerReloadTimedOut, Detail: "no receipt"}

	// timeout_sec also proves the wait budget is per-request configurable.
	resp := do(t, s, http.MethodPost, "/v1/workers/w1/reload", testToken, map[string]any{"timeout_sec": 3})
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504", resp.StatusCode)
	}
	var body workerReloadResp
	decode(t, resp, &body)
	if body.Applied {
		t.Error("a timed-out reload must not report applied=true (the worker may still apply it)")
	}
	if want := "worker w1 did not answer the reload request within 3s"; body.Error != want {
		t.Fatalf("error = %q, want %q", body.Error, want)
	}
	if fr.gotWait != 3*time.Second {
		t.Errorf("wait budget = %s, want the requested 3s", fr.gotWait)
	}
}

func TestWorkerReloadUnknownWorker(t *testing.T) {
	s, fr := newReloadServer(t, nil)
	resp := do(t, s, http.MethodPost, "/v1/workers/nope/reload", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if fr.called != 0 {
		t.Error("an unregistered worker id must not reach the hub")
	}
}

func TestWorkerReloadNoHubWired(t *testing.T) {
	s, _ := newReloadServer(t, nil)
	s.SetWorkerReloader(nil)
	resp := do(t, s, http.MethodPost, "/v1/workers/w1/reload", testToken, nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when no hub is wired", resp.StatusCode)
	}
}

func TestWorkerReloadRequiresAuth(t *testing.T) {
	s, fr := newReloadServer(t, nil)
	resp := do(t, s, http.MethodPost, "/v1/workers/w1/reload", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if fr.called != 0 {
		t.Error("an unauthenticated request must not reach the hub")
	}
}

// TestWorkerReloadRouteCoexistsWithWSRoutes guards the router shape: the reload path
// puts a {id} param where /v1/workers/connect has a static segment, and both must
// stay routable on a server that runs the hub.
func TestWorkerReloadRouteCoexistsWithWSRoutes(t *testing.T) {
	s, fr := newReloadServer(t, fakeHub{})
	fr.out = WorkerReloadOutcome{Status: WorkerReloadApplied}
	resp := do(t, s, http.MethodPost, "/v1/workers/w1/reload", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (reload must still route with the WS routes mounted)", resp.StatusCode)
	}
}
