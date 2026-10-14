package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/xfer"
)

// newWorkerScopedServer builds a server with two worker tokens (w1, w2) plus the
// legacy operator token (= caller "default"), over a temp project "self".
func newWorkerScopedServer(t *testing.T) *Server {
	t.Helper()
	return newTestServerCfg(t, config.ServerConfig{
		Token: testToken,
		Workers: map[string]config.WorkerAuthConfig{
			"w1": {Token: "tok-w1"},
			"w2": {Token: "tok-w2"},
		},
	})
}

// TestWorkerTokenConfinedToItsOwnRoutes: a worker token is a credential for the worker
// protocol, not a second operator token. Every route outside the worker whitelist of
// workercredential.go is refused with the same wording before any handler runs —
// including the writes a worker used to be able to make (submitting jobs, config,
// projects, worker registration) and the reads it never needed.
func TestWorkerTokenConfinedToItsOwnRoutes(t *testing.T) {
	t.Parallel()
	s := newWorkerScopedServer(t)

	cases := []struct {
		name         string
		method, path string
		body         any
	}{
		{name: "submit a job", method: http.MethodPost, path: "/v1/jobs", body: job.JobRequest{
			ProjectKey: "self", Agent: "exec", Runner: "local",
			Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
		}},
		{name: "write agent config", method: http.MethodPut, path: "/v1/config/agents/exec", body: map[string]any{}},
		{name: "delete agent config", method: http.MethodDelete, path: "/v1/config/agents/exec"},
		{name: "write server config", method: http.MethodPut, path: "/v1/config/server", body: map[string]any{}},
		{name: "create a project", method: http.MethodPost, path: "/v1/projects", body: map[string]any{}},
		{name: "write a project", method: http.MethodPut, path: "/v1/projects/self", body: map[string]any{}},
		{name: "register a worker", method: http.MethodPost, path: "/v1/workers", body: map[string]any{}},
		{name: "remove a worker", method: http.MethodDelete, path: "/v1/workers/w2"},
		{name: "reload a worker", method: http.MethodPost, path: "/v1/workers/w1/reload", body: map[string]any{}},
		{name: "upgrade a worker", method: http.MethodPost, path: "/v1/workers/w1/upgrade", body: map[string]any{}},
		{name: "import a skill", method: http.MethodPost, path: "/v1/skills/import", body: map[string]any{}},
		{name: "create a schedule", method: http.MethodPost, path: "/v1/schedules", body: map[string]any{}},
		{name: "run a schedule", method: http.MethodPost, path: "/v1/schedules/sch-1/run-now"},
		{name: "rotate a schedule token", method: http.MethodPost, path: "/v1/schedules/sch-1/rotate-token"},
		{name: "create a transfer", method: http.MethodPost, path: "/v1/xfer", body: map[string]any{}},
		{name: "list transfers", method: http.MethodGet, path: "/v1/xfer"},
		{name: "read a transfer", method: http.MethodGet, path: "/v1/xfer/xf-1234"},
		{name: "delete a transfer", method: http.MethodDelete, path: "/v1/xfer/xf-1234"},
		{name: "read the job list", method: http.MethodGet, path: "/v1/jobs"},
		{name: "read a job", method: http.MethodGet, path: "/v1/jobs/j-1"},
		{name: "read a session", method: http.MethodGet, path: "/v1/sessions"},
		{name: "read the config", method: http.MethodGet, path: "/v1/config"},
		{name: "read the meta form options", method: http.MethodGet, path: "/v1/meta"},
	}
	for _, tc := range cases {
		resp := do(t, s, tc.method, tc.path, "tok-w1", tc.body)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s: %s %s status=%d, want 403", tc.name, tc.method, tc.path, resp.StatusCode)
		}
		var body errorBody
		decode(t, resp, &body)
		if body.Error != "worker credentials cannot call this endpoint" {
			t.Fatalf("%s: error=%q, want the worker-confinement wording", tc.name, body.Error)
		}
	}

	// The same operator token still reaches the submit route: the gate is about the
	// credential KIND, not about the route being closed in general.
	created, status := createJob(t, s, testToken)
	if status != http.StatusOK {
		t.Fatalf("operator submit status=%d, want 200", status)
	}
	waitDoneTok(t, s, created.ID, testToken)
}

// TestWorkerTokenReachesItsOwnRoutes: the whitelist rows a worker process actually
// uses stay reachable — the upgrade binary it downloads, the transfer content assigned
// to it (both directions) and its own assignable projects — and the target-level rules
// on top of them are unchanged (another worker's id is still refused).
func TestWorkerTokenReachesItsOwnRoutes(t *testing.T) {
	t.Parallel()
	sc := config.ServerConfig{
		Token: testToken,
		Workers: map[string]config.WorkerAuthConfig{
			"w1": {Token: "tok-w1"},
			"w2": {Token: "tok-w2"},
		},
	}
	s, mgr, _ := newXferServer(t, sc, xfer.Limits{})

	// GET /v1/workers/{id}/upgrade/file: past the gate (this server has no staging
	// area, so the handler answers 503 — a 403 would mean the gate refused it).
	resp := do(t, s, http.MethodGet, "/v1/workers/w1/upgrade/file", "tok-w1", nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("upgrade file status=%d, want 503 (past the worker gate)", resp.StatusCode)
	}
	if body := readError(t, resp); body.Error == "worker credentials cannot call this endpoint" {
		t.Fatalf("GET upgrade/file must be on the worker whitelist: %+v", body)
	}

	// GET /v1/workers/{id}/assignable: its own id answers, a peer's is refused by the
	// handler (the route itself is open to a worker).
	resp = do(t, s, http.MethodGet, "/v1/workers/w1/assignable", "tok-w1", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("own assignable status=%d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, s, http.MethodGet, "/v1/workers/w1/assignable", "tok-w2", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("peer assignable status=%d, want 403", resp.StatusCode)
	}
	if body := readError(t, resp); body.Error == "worker credentials cannot call this endpoint" {
		t.Fatalf("assignable must be on the worker whitelist: %+v", body)
	}

	// PUT /v1/xfer/{id}/content: the assigned worker uploads a get's result.
	payload := []byte("collected by w1")
	sum := sha256.Sum256(payload)
	getRec, err := mgr.StageGet("default", "w1", "demo", "tmp/out.bin")
	if err != nil {
		t.Fatalf("stage get: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, "/v1/xfer/"+getRec.ID+"/content", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer tok-w1")
	req.Header.Set("X-Gofer-Sha256", hex.EncodeToString(sum[:]))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("assigned worker upload status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}

	// GET /v1/xfer/{id}/content: the assigned worker fetches a put's source.
	putRec, err := mgr.StagePut("default", "w1", "demo", "tmp/in.bin", int64(len(payload)), hex.EncodeToString(sum[:]), true)
	if err != nil {
		t.Fatalf("stage put: %v", err)
	}
	w, err := mgr.Store().Writer(putRec.ID)
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := mgr.CommitPut(putRec.ID, int64(len(payload)), hex.EncodeToString(sum[:])); err != nil {
		t.Fatalf("commit put: %v", err)
	}
	resp = do(t, s, http.MethodGet, "/v1/xfer/"+putRec.ID+"/content", "tok-w1", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("assigned worker fetch status=%d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
	// Another worker's token is still refused by the handler's target rule.
	resp = do(t, s, http.MethodGet, "/v1/xfer/"+putRec.ID+"/content", "tok-w2", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("peer worker fetch status=%d, want 403", resp.StatusCode)
	}
	if body := readError(t, resp); body.Error == "worker credentials cannot call this endpoint" {
		t.Fatalf("xfer content must be on the worker whitelist: %+v", body)
	}
}

// TestCallerMayAdminRequiresAUserKind: the admin gate is a person's. It used to consult
// only the capability bit, which the default-off governance switch skips entirely — so
// any authenticated caller passed it. The caller kind is checked first now, which is
// what keeps a worker (or a job) credential from being treated as an operator even on a
// route that is reachable for it.
func TestCallerMayAdminRequiresAUserKind(t *testing.T) {
	t.Parallel()
	s := newWorkerScopedServer(t)

	admin := &rux.Context{Req: httptest.NewRequest(http.MethodGet, "/v1/config", nil), Resp: httptest.NewRecorder()}
	admin.Set(ctxCallerID, "default")
	admin.Set(ctxCallerKind, callerKindUser)
	if !s.callerMayAdmin(admin) {
		t.Fatal("a user caller must pass the admin gate when the capability switch is off")
	}

	worker := &rux.Context{Req: httptest.NewRequest(http.MethodGet, "/v1/config", nil), Resp: httptest.NewRecorder()}
	worker.Set(ctxCallerID, "w1")
	worker.Set(ctxCallerKind, callerKindWorker)
	if s.callerMayAdmin(worker) {
		t.Fatal("a worker credential must never pass the admin gate")
	}

	jc := &rux.Context{Req: httptest.NewRequest(http.MethodGet, "/v1/config", nil), Resp: httptest.NewRecorder()}
	jc.Set(ctxCallerID, "j-1")
	jc.Set(ctxCallerKind, callerKindJob)
	if s.callerMayAdmin(jc) {
		t.Fatal("a job credential must never pass the admin gate")
	}

	// With the capability switch ON, a worker id that somehow carries the bit is still
	// refused: the kind check comes first.
	s.cfg.Governance.RequireAdminCapability = true
	s.cfg.Callers = []config.CallerConfig{{ID: "w1", CanAdmin: true}}
	if s.callerMayAdmin(worker) {
		t.Fatal("the caller kind is checked before the capability bit")
	}
	if s.callerMayAdmin(admin) {
		t.Fatal("a caller without the can_admin bit must be refused when the switch is on")
	}
}

// readError decodes an errorBody and closes the body.
func readError(t *testing.T, resp *http.Response) errorBody {
	t.Helper()
	var body errorBody
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return body
}
