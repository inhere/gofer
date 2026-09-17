package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/sessionrelay"
)

// fakeInjector stands in for the job service behind path A (§9.1): the HTTP
// contract is about routing and status codes, not about running tmux.
type fakeInjector struct {
	reqs []sessionrelay.InjectRequest
	res  sessionrelay.InjectResult
	err  error
}

func (f *fakeInjector) InjectSession(_ context.Context, req sessionrelay.InjectRequest) (sessionrelay.InjectResult, error) {
	f.reqs = append(f.reqs, req)
	return f.res, f.err
}

// TestSessionDeliverEndpoint walks POST /v1/sessions/{sid}/deliver: the turn path
// answers as `say` does (path=turn), an idle session in tmux goes to path A
// (path=tmux + the job id), a session without tmux is a 409 naming no_tmux, an
// over-long text is a 400, and a worker token may not drive a terminal at all.
func TestSessionDeliverEndpoint(t *testing.T) {
	s := newTestServer(t, testToken, false)
	inj := &fakeInjector{res: sessionrelay.InjectResult{JobID: "job-http-inject", ExitCode: 0}}
	s.relay.SetInjector(inj)

	// A session that is idle in tmux: path A.
	resp := do(t, s, http.MethodPost, "/v1/sessions", testToken, map[string]any{
		"session_id": "sid-deliver-tmux", "agent": "claude", "runner": "server",
		"cwd": s.projects.Config().Projects["self"].HostPath, "tmux_pane": "%7",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register status=%d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-deliver-tmux/deliver", testToken, map[string]any{"text": "carry on"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("deliver to tmux status=%d, want 200", resp.StatusCode)
	}
	var out struct {
		Path       string `json:"path"`
		JobID      string `json:"job_id"`
		DecisionID string `json:"decision_id"`
	}
	decode(t, resp, &out)
	if out.Path != "tmux" || out.JobID != "job-http-inject" || out.DecisionID == "" {
		t.Fatalf("tmux deliver result=%+v, want path=tmux with the job and audit ids", out)
	}
	// The manager maps the session's own label to the server's local runner.
	if len(inj.reqs) != 1 || inj.reqs[0].Runner != "server" {
		t.Fatalf("injection request=%+v, want one request for runner \"server\"", inj.reqs)
	}
	// State moved to running and the audit row is queryable.
	resp = do(t, s, http.MethodGet, "/v1/sessions/sid-deliver-tmux", testToken, nil)
	var detail struct {
		Session sessionView    `json:"session"`
		Turns   []decisionView `json:"turns"`
	}
	decode(t, resp, &detail)
	if detail.Session.State != "running" {
		t.Fatalf("state after the injection=%s, want running", detail.Session.State)
	}
	if len(detail.Turns) != 1 || detail.Turns[0].Detail == "" {
		t.Fatalf("audit turns=%+v, want one row carrying the tmux detail", detail.Turns)
	}

	// With a turn OPEN the same endpoint answers it instead (phase 1).
	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-deliver-tmux/relay", testToken, map[string]any{"mode": "on"})
	resp.Body.Close()
	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-deliver-tmux/turns", testToken, map[string]any{"body": "which?", "timeout_sec": 120})
	var turn decisionView
	decode(t, resp, &turn)
	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-deliver-tmux/deliver", testToken, map[string]any{"text": "the second one"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("deliver with an open turn status=%d, want 200", resp.StatusCode)
	}
	decode(t, resp, &out)
	if out.Path != "turn" || out.DecisionID != turn.ID || out.JobID != "" {
		t.Fatalf("turn deliver result=%+v, want path=turn answering %s", out, turn.ID)
	}
	if len(inj.reqs) != 1 {
		t.Fatalf("an OPEN turn must not dispatch an injection: %+v", inj.reqs)
	}

	// No tmux pane: 409 with the reason in the envelope's error field.
	resp = do(t, s, http.MethodPost, "/v1/sessions", testToken, map[string]any{
		"session_id": "sid-deliver-nopane", "agent": "claude", "runner": "server", "cwd": "/w",
	})
	resp.Body.Close()
	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-deliver-nopane/deliver", testToken, map[string]any{"text": "hello"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("deliver without tmux status=%d, want 409", resp.StatusCode)
	}
	var env struct {
		Error string `json:"error"`
	}
	decode(t, resp, &env)
	if env.Error != "deliver failed: no_tmux" {
		t.Fatalf("error field=%q, want the no_tmux reason code", env.Error)
	}

	// Over-long reply: 400 (nothing dispatched).
	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-deliver-tmux/deliver", testToken,
		map[string]any{"text": string(make([]byte, sessionrelay.MaxDeliverText+1))})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("over-long deliver status=%d, want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// A failed injection is the server's side of the wire: 502.
	inj.res = sessionrelay.InjectResult{JobID: "job-busy", ExitCode: 4, Output: "pane_busy:vim\n"}
	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-deliver-tmux/deliver", testToken, map[string]any{"text": "hello"})
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("failed injection status=%d, want 502", resp.StatusCode)
	}
	decode(t, resp, &env)
	if env.Error != "deliver failed: inject_failed:pane_busy:vim" {
		t.Fatalf("error field=%q, want the pane_busy reason", env.Error)
	}
}

// TestSessionDeliverWorkerForbidden pins the caller rule: a worker token runs
// jobs, it does not answer for a person, so it may not type into a terminal.
func TestSessionDeliverWorkerForbidden(t *testing.T) {
	s := newTestServerCfg(t, config.ServerConfig{
		Token:   testToken,
		Workers: map[string]config.WorkerAuthConfig{"w1": {Token: "worker-token"}},
	})
	resp := do(t, s, http.MethodPost, "/v1/sessions", testToken, map[string]any{
		"session_id": "sid-worker", "agent": "claude", "runner": "server", "tmux_pane": "%1",
	})
	resp.Body.Close()

	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-worker/deliver", "worker-token", map[string]any{"text": "hi"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("worker deliver status=%d, want 403", resp.StatusCode)
	}
	resp.Body.Close()

	// The same request with a human token is accepted (403 is about the caller).
	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-worker/deliver", testToken, map[string]any{"text": "hi"})
	if resp.StatusCode == http.StatusForbidden {
		t.Fatal("a human caller must not be refused")
	}
	resp.Body.Close()
}
