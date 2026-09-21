package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
	"github.com/inhere/gofer/internal/xfer"
)

// newXferServer wires a Server with a transfer manager over a temp storage root
// and one project "demo" whose execution root is a sibling temp dir. The manager
// has NO runner wired, so a staged transfer stays staged (deterministic asserts).
func newXferServer(t *testing.T, sc config.ServerConfig, lim xfer.Limits) (*Server, *xfer.Manager, string) {
	t.Helper()
	root := t.TempDir()
	projRoot := t.TempDir()
	cfg := &config.Config{
		Server:  sc,
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"demo": {
				HostPath:       projRoot,
				AllowedAgents:  []string{"exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	st, err := jobstore.Open(filepath.Join(root, "gofer.db"))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	jobs := drainOnCleanup(t, job.NewService(cfg, projects, agents, runners, st, nil))
	eng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(eng)
	s := New(&cfg.Server, sc.Token, sc.AllowEmptyToken, jobs, eng, projects, agents, nil, nil, nil, nil)
	// Events is the job service, exactly as core.Build wires it: a transfer's audit
	// event then travels the notification pipeline too (XFER-01 X2).
	mgr, err := xfer.NewManager(xfer.Options{Root: root, Limits: lim, Repo: st, Events: jobs})
	if err != nil {
		t.Fatalf("new xfer manager: %v", err)
	}
	s.SetXfer(mgr)
	return s, mgr, projRoot
}

// xferPush posts a multipart push (meta field first, then the file) and returns
// the response plus the decoded body.
func xferPush(t *testing.T, s *Server, token string, meta map[string]any, filename string, data []byte) (*http.Response, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	raw, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := mw.WriteField("meta", string(raw)); err != nil {
		t.Fatal(err)
	}
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/xfer", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	resp := rec.Result()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		body = map[string]any{"_decode_error": err.Error()}
	}
	resp.Body.Close()
	return resp, body
}

// pushMeta builds a put meta object for the demo project.
func pushMeta(path string, data []byte, sha string, force bool) map[string]any {
	return map[string]any{
		"op": "put", "runner": "server", "project": "demo", "path": path,
		"sha256": sha, "size": len(data), "force": force,
	}
}

// TestXferPutStagesAndVerifiesSha: a verified upload is staged byte-for-byte; a
// declared digest that does not match the streamed bytes answers 400 AND deletes
// the staging directory (a corrupted upload must never be dispatchable).
func TestXferPutStagesAndVerifiesSha(t *testing.T) {
	s, mgr, _ := newXferServer(t, config.ServerConfig{Token: testToken}, xfer.Limits{})
	data := []byte("hello xfer world")
	sum := sha256.Sum256(data)
	good := hex.EncodeToString(sum[:])

	resp, body := xferPush(t, s, testToken, pushMeta("tmp/a.bin", data, good, true), "a.bin", data)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%v, want 200", resp.StatusCode, body)
	}
	id, _ := body["id"].(string)
	if id == "" || body["state"] != "staged" {
		t.Fatalf("body=%v, want an id and state=staged", body)
	}
	got, err := os.ReadFile(mgr.Store().Path(id))
	if err != nil {
		t.Fatalf("read staged payload: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("staged payload = %q, want %q", got, data)
	}
	rec, ok, err := mgr.Get(id)
	if err != nil || !ok {
		t.Fatalf("get record: ok=%v err=%v", ok, err)
	}
	if rec.Size != int64(len(data)) || rec.SHA256 != good || rec.State != string(xfer.StateStaged) {
		t.Fatalf("record = %+v, want size/%s and state staged", rec, good)
	}

	// Wrong digest: rejected, and nothing is left behind.
	bad := pushMeta("tmp/b.bin", data, "deadbeef", true)
	resp, body = xferPush(t, s, testToken, bad, "b.bin", data)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad sha status=%d body=%v, want 400", resp.StatusCode, body)
	}
	badID, _ := body["id"].(string)
	if badID == "" {
		// The id is not echoed on failure; find the failed row by listing.
		rows, lerr := mgr.List(jobstore.XferFilter{State: string(xfer.StateFailed)})
		if lerr != nil || len(rows) != 1 {
			t.Fatalf("want exactly one failed row, got %d (err=%v)", len(rows), lerr)
		}
		badID = rows[0].ID
	}
	if _, err := os.Stat(mgr.Store().Path(badID)); !os.IsNotExist(err) {
		t.Fatalf("staging payload for the rejected upload still exists (err=%v)", err)
	}
}

// TestXferGetContentWorkerScope: a worker token may only reach a transfer
// assigned to itself, and only in the direction it is meant to serve.
func TestXferGetContentWorkerScope(t *testing.T) {
	sc := config.ServerConfig{
		Token: testToken,
		Workers: map[string]config.WorkerAuthConfig{
			"w-a": {Token: "tok-a"},
			"w-b": {Token: "tok-b"},
		},
	}
	s, mgr, _ := newXferServer(t, sc, xfer.Limits{})
	data := []byte("worker payload")
	sum := sha256.Sum256(data)

	// A put assigned to w-a, staged directly through the manager.
	rec, err := mgr.StagePut("alice", "w-a", "demo", "tmp/in.bin", int64(len(data)), hex.EncodeToString(sum[:]), true)
	if err != nil {
		t.Fatalf("stage put: %v", err)
	}
	w, err := mgr.Store().Writer(rec.ID)
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatalf("write: %v", err)
	}
	w.Close()
	if err := mgr.CommitPut(rec.ID, int64(len(data)), hex.EncodeToString(sum[:])); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Another worker asking for that id is refused.
	resp := do(t, s, http.MethodGet, "/v1/xfer/"+rec.ID+"/content", "tok-b", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-worker status=%d, want 403", resp.StatusCode)
	}
	resp.Body.Close()

	// The assigned worker gets the bytes.
	resp = do(t, s, http.MethodGet, "/v1/xfer/"+rec.ID+"/content", "tok-a", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("assigned worker status=%d, want 200", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(got, data) {
		t.Fatalf("assigned worker payload = %q, want %q", got, data)
	}

	// A user downloading a PUT has nothing to fetch (their own file), and a get
	// result is only served once it is done.
	resp = do(t, s, http.MethodGet, "/v1/xfer/"+rec.ID+"/content", testToken, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("user download of a put status=%d, want 409", resp.StatusCode)
	}
	resp.Body.Close()

	getRec, err := mgr.StageGet("alice", "w-a", "demo", "tmp/out.bin")
	if err != nil {
		t.Fatalf("stage get: %v", err)
	}
	resp = do(t, s, http.MethodGet, "/v1/xfer/"+getRec.ID+"/content", testToken, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("user download before done status=%d, want 409", resp.StatusCode)
	}
	resp.Body.Close()

	// The assigned worker uploads the get's result: verified, settled done.
	req := httptest.NewRequest(http.MethodPut, "/v1/xfer/"+getRec.ID+"/content", bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer tok-b")
	req.Header.Set("X-Gofer-Sha256", hex.EncodeToString(sum[:]))
	req.Header.Set("X-Gofer-Size", "14")
	rec2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec2, req)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("cross-worker upload status=%d, want 403", rec2.Code)
	}

	req = httptest.NewRequest(http.MethodPut, "/v1/xfer/"+getRec.ID+"/content", bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer tok-a")
	req.Header.Set("X-Gofer-Sha256", hex.EncodeToString(sum[:]))
	rec2 = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("assigned worker upload status=%d body=%s, want 200", rec2.Code, rec2.Body.String())
	}
	settled, ok, _ := mgr.Get(getRec.ID)
	if !ok || settled.State != string(xfer.StateDone) {
		t.Fatalf("get record = %+v, want done", settled)
	}
	resp = do(t, s, http.MethodGet, "/v1/xfer/"+getRec.ID+"/content", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("user download after done status=%d, want 200", resp.StatusCode)
	}
	if h := resp.Header.Get("X-Gofer-Sha256"); h != hex.EncodeToString(sum[:]) {
		t.Fatalf("X-Gofer-Sha256=%q, want %s", h, hex.EncodeToString(sum[:]))
	}
	got, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(got, data) {
		t.Fatalf("downloaded payload = %q, want %q", got, data)
	}
}

// TestXferPushDispatchesAfterResponse: a staged push is executed by the background
// dispatch that the create REQUEST started, i.e. after the response was written —
// the delivery must not inherit the request's cancellation (regression: passing
// c.Req.Context() made every transfer settle `failed: context canceled` within a
// millisecond of a 200, because net/http cancels that context on handler return).
//
// This one drives a REAL http.Server on purpose: httptest.NewRequest hands the
// handler a context nobody cancels, so the bug is invisible through the
// in-process recorder — only a served request reproduces it.
func TestXferPushDispatchesAfterResponse(t *testing.T) {
	s, mgr, projRoot := newXferServer(t, config.ServerConfig{Token: testToken}, xfer.Limits{})
	mgr.SetRunner(&xfer.Router{
		Local: &xfer.LocalRunner{
			Config:       func() *config.Config { return s.projects.Config() },
			Store:        mgr.Store(),
			OnGetContent: mgr.CommitGet,
		},
	})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	data := []byte("server-direct payload")
	sum := sha256.Sum256(data)
	meta, err := json.Marshal(pushMeta("tmp/out/a.bin", data, hex.EncodeToString(sum[:]), true))
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("meta", string(meta)); err != nil {
		t.Fatal(err)
	}
	fw, err := mw.CreateFormFile("file", "a.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/xfer", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var created map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%v, want 200", resp.StatusCode, created)
	}
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("body=%v, want an id", created)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		rec, ok, err := mgr.Get(id)
		if err != nil || !ok {
			t.Fatalf("get record: ok=%v err=%v", ok, err)
		}
		if rec.State == string(xfer.StateFailed) {
			t.Fatalf("transfer failed: %s", rec.Error)
		}
		if rec.State == string(xfer.StateDone) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("transfer stuck in state %s", rec.State)
		}
		time.Sleep(10 * time.Millisecond)
	}

	got, err := os.ReadFile(filepath.Join(projRoot, "tmp", "out", "a.bin"))
	if err != nil {
		t.Fatalf("read the delivered file: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("delivered payload = %q, want %q", got, data)
	}
}

// TestXferPathEscapeRejected: a destination outside the project root is refused
// before anything is staged, in both the push and the pull direction.
func TestXferPathEscapeRejected(t *testing.T) {
	s, _, _ := newXferServer(t, config.ServerConfig{Token: testToken}, xfer.Limits{})
	data := []byte("x")
	sum := sha256.Sum256(data)
	resp, body := xferPush(t, s, testToken, pushMeta("../../evil.bin", data, hex.EncodeToString(sum[:]), true), "evil.bin", data)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("escape push status=%d body=%v, want 400", resp.StatusCode, body)
	}
	if body["error"] != "path escapes project" {
		t.Fatalf("error=%v, want path escapes project", body["error"])
	}

	resp = do(t, s, http.MethodPost, "/v1/xfer", testToken, map[string]any{
		"op": "get", "runner": "server", "project": "demo", "path": `C:\Windows\system32\config`,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("windows-drive pull path status=%d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, s, http.MethodPost, "/v1/xfer", testToken, map[string]any{
		"op": "get", "runner": "server", "project": "demo", "path": `\windows\config`,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("rooted pull path status=%d, want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// An unknown runner is a 404, an unknown project a 404.
	resp = do(t, s, http.MethodPost, "/v1/xfer", testToken, map[string]any{
		"op": "get", "runner": "nope", "project": "demo", "path": "tmp/x",
	})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown runner status=%d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, s, http.MethodPost, "/v1/xfer", testToken, map[string]any{
		"op": "get", "runner": "server", "project": "nope", "path": "tmp/x",
	})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown project status=%d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestXferOversizeRejected: both a declared and a streamed payload over the
// configured cap are refused with 413, and neither is staged.
func TestXferOversizeRejected(t *testing.T) {
	s, mgr, _ := newXferServer(t, config.ServerConfig{Token: testToken}, xfer.Limits{MaxBytes: 16})
	big := bytes.Repeat([]byte("z"), 64)
	sum := sha256.Sum256(big)

	// Declared size over the cap: refused before the body is even read.
	resp, body := xferPush(t, s, testToken, pushMeta("tmp/big.bin", big, hex.EncodeToString(sum[:]), true), "big.bin", big)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("declared oversize status=%d body=%v, want 413", resp.StatusCode, body)
	}

	// Declared size within the cap but the stream is over it: still 413, and the
	// record is failed with nothing staged.
	lying := pushMeta("tmp/liar.bin", big, hex.EncodeToString(sum[:]), true)
	lying["size"] = 4
	resp, _ = xferPush(t, s, testToken, lying, "liar.bin", big)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("streamed oversize status=%d, want 413", resp.StatusCode)
	}
	rows, err := mgr.List(jobstore.XferFilter{State: string(xfer.StateFailed)})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("failed rows = %d, want 1", len(rows))
	}
	if _, err := os.Stat(mgr.Store().Path(rows[0].ID)); !os.IsNotExist(err) {
		t.Fatalf("rejected oversize upload left a payload behind (err=%v)", err)
	}
}

// infinitePushBody streams a multipart push whose file part NEVER ends, so a
// server that reads the payload before it validates the meta cannot answer at all
// (the old h-aii-gnm3 behaviour: the real machine reported a 400 after 34% of the
// bytes). Closing the returned reader unblocks the writer goroutine.
func infinitePushBody(t *testing.T, meta map[string]any) (*io.PipeReader, string) {
	t.Helper()
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		raw, err := json.Marshal(meta)
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		if err := mw.WriteField("meta", string(raw)); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		fw, err := mw.CreateFormFile("file", "huge.bin")
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		block := bytes.Repeat([]byte("x"), 32*1024)
		for {
			if _, err := fw.Write(block); err != nil {
				return // the server stopped reading / the test closed the pipe
			}
		}
	}()
	return pr, mw.FormDataContentType()
}

// TestXferRejectsBeforeReadingFile: a push whose meta is already refusable (the
// path escapes the project) is answered 400 while its file stream is still being
// produced — the payload is never consumed (bd h-aii-gnm3).
func TestXferRejectsBeforeReadingFile(t *testing.T) {
	s, mgr, _ := newXferServer(t, config.ServerConfig{Token: testToken}, xfer.Limits{})
	body, ctype := infinitePushBody(t, pushMeta("../escape.bin", []byte("x"), "", true))
	defer body.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/xfer", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()

	start := time.Now()
	done := make(chan struct{})
	go func() {
		s.Handler().ServeHTTP(rec, req)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("no answer while the file part was still streaming: the server read the payload before validating the meta")
	}
	elapsed := time.Since(start)
	resp := rec.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s, want 400", resp.StatusCode, raw)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("400 took %s; the meta must be judged before any payload is read", elapsed)
	}
	// Nothing was staged: no journal row exists for this attempt.
	rows, err := mgr.List(jobstore.XferFilter{})
	if err != nil {
		t.Fatalf("list xfers: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("a refused push left %d transfer row(s): %+v", len(rows), rows)
	}
}

// TestXferPrecheckEndpoint: POST /v1/xfer/precheck validates exactly what a create
// validates — target, project, path and size — and stages NOTHING, so the CLI can
// fail before it hashes or streams a byte (bd h-aii-gnm3).
func TestXferPrecheckEndpoint(t *testing.T) {
	s, mgr, _ := newXferServer(t, config.ServerConfig{
		Token:   testToken,
		Workers: map[string]config.WorkerAuthConfig{"w-off": {}},
	}, xfer.Limits{MaxBytes: 1024})

	post := func(t *testing.T, meta map[string]any) (int, map[string]any) {
		t.Helper()
		raw, err := json.Marshal(meta)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/xfer/precheck", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+testToken)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		resp := rec.Result()
		defer resp.Body.Close()
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode, body
	}

	// The same meta a push would send: accepted.
	if status, body := post(t, pushMeta("tmp/ok.bin", []byte("1234"), "", false)); status != http.StatusOK {
		t.Fatalf("valid precheck status=%d body=%v, want 200", status, body)
	}
	if status, body := post(t, pushMeta("../escape.bin", []byte("1234"), "", false)); status != http.StatusBadRequest {
		t.Fatalf("escaping path precheck status=%d body=%v, want 400", status, body)
	}
	if status, body := post(t, map[string]any{"op": "put", "runner": "server", "project": "nope", "path": "a.bin"}); status != http.StatusNotFound {
		t.Fatalf("unknown project precheck status=%d body=%v, want 404", status, body)
	}
	if status, body := post(t, map[string]any{"op": "put", "runner": "ghost", "project": "demo", "path": "a.bin"}); status != http.StatusNotFound {
		t.Fatalf("unknown runner precheck status=%d body=%v, want 404", status, body)
	}
	// A registered worker that is NOT connected must be refused up front: the old
	// flow accepted the payload and failed later, at dispatch.
	if status, body := post(t, map[string]any{"op": "put", "runner": "w-off", "project": "demo", "path": "a.bin"}); status != http.StatusConflict {
		t.Fatalf("offline runner precheck status=%d body=%v, want 409", status, body)
	}
	// Over the cap (Limits.MaxBytes = 1024 above).
	if status, body := post(t, map[string]any{"op": "put", "runner": "server", "project": "demo", "path": "a.bin", "size": 4096}); status != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized precheck status=%d body=%v, want 413", status, body)
	}
	// Precheck is a pure check: no row, no staging directory.
	rows, err := mgr.List(jobstore.XferFilter{})
	if err != nil {
		t.Fatalf("list xfers: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("precheck staged %d transfer(s): %+v", len(rows), rows)
	}
}

// TestXferPrecheckEndpointRefusesWorkerCaller: like the create, the precheck is a
// user-only surface (a worker must not be able to probe the server's transfer
// targets).
func TestXferPrecheckEndpointRefusesWorkerCaller(t *testing.T) {
	s, _, _ := newXferServer(t, config.ServerConfig{
		Token:   testToken,
		Workers: map[string]config.WorkerAuthConfig{"w1": {Token: "worker-token"}},
	}, xfer.Limits{})

	raw, _ := json.Marshal(map[string]any{"op": "put", "runner": "server", "project": "demo", "path": "a.bin"})
	req := httptest.NewRequest(http.MethodPost, "/v1/xfer/precheck", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer worker-token")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("worker precheck status=%d, want 403", rec.Code)
	}
}

// TestXferAcceptsLegacyLongID: transfer rows written before XFER-02 keep their
// 32-hex ids and stay readable — nothing parses an id's shape, so the short form
// is a write-side change only.
func TestXferAcceptsLegacyLongID(t *testing.T) {
	s, _, _ := newXferServer(t, config.ServerConfig{Token: testToken}, xfer.Limits{})
	legacy := "0123456789abcdef0123456789abcdef"
	if err := s.jobs.Meta().InsertXfer(jobstore.XferRecord{
		ID: legacy, Op: "put", Runner: "local", ProjectKey: "demo", Path: "tmp/old.bin",
		Size: 3, State: "done", CallerID: "alice", CreatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/xfer/"+legacy, nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	resp := rec.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("legacy id status=%d body=%s, want 200", resp.StatusCode, raw)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["id"] != legacy {
		t.Fatalf("body id = %v, want the legacy %s", body["id"], legacy)
	}
}

// TestXferCreateMintsShortID: a real create now mints `xf-<8hex>` (XFER-02), and
// the row it answers with is the one the journal holds.
func TestXferCreateMintsShortID(t *testing.T) {
	s, _, _ := newXferServer(t, config.ServerConfig{Token: testToken}, xfer.Limits{})
	data := []byte("short id payload")
	resp, body := xferPush(t, s, testToken, pushMeta("tmp/short.bin", data, "", true), "short.bin", data)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%v, want 200", resp.StatusCode, body)
	}
	id, _ := body["id"].(string)
	if !regexp.MustCompile(`^xf-[0-9a-f]{8}$`).MatchString(id) {
		t.Fatalf("created id = %q, want xf-<8 hex>", id)
	}
}
