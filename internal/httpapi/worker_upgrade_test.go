package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/buildinfo"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
	"github.com/inhere/gofer/internal/workerupgrade"
	"github.com/inhere/gofer/internal/wsproto"
)

type fakeUpgrader struct {
	got     wsproto.Upgrade
	version string
	err     error
	called  int
}

func (f *fakeUpgrader) UpgradeWorker(_ context.Context, _ string, req wsproto.Upgrade) (string, error) {
	f.called++
	f.got = req
	return f.version, f.err
}

func newUpgradeServer(t *testing.T, ws map[string]WorkerStatus) (*Server, *fakeUpgrader, *workerupgrade.Manager) {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Server: config.ServerConfig{
			Token: testToken,
			Workers: map[string]config.WorkerAuthConfig{
				"w1": {Token: "tok-w1"},
				"w2": {Token: "tok-w2"},
			},
		},
		Storage: config.StorageConfig{Root: root},
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	jobs := drainOnCleanup(t, job.NewService(cfg, projects, agents, runners, openTestStore(t, root), nil))
	eng := workflow.NewEngine(jobs)
	s := New(&cfg.Server, testToken, false, jobs, eng, projects, agents, nil, nil, nil, fakeWorkers(ws))
	s.SetBuildInfo(buildinfo.Info{Version: "v9.9", GitCommit: "abcdef0123"})
	fu := &fakeUpgrader{version: "v9.9"}
	s.SetWorkerUpgrader(fu)
	mgr := workerupgrade.New(t.TempDir())
	s.SetWorkerUpgrades(mgr)
	return s, fu, mgr
}

func rawDo(t *testing.T, s *Server, method, path, token string, body []byte) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.Result()
}

func connectedWorker(osName, arch string, proto int) WorkerStatus {
	return WorkerStatus{Connected: true, OS: osName, Arch: arch, ProtocolVersion: proto, GoferVersion: "v1.0"}
}

func TestWorkerUpgradeFileDownloadAuth(t *testing.T) {
	t.Parallel()
	s, _, _ := newUpgradeServer(t, map[string]WorkerStatus{"w1": connectedWorker("linux", "amd64", 15)})
	payload := []byte("worker-binary-bytes")
	sum := sha256.Sum256(payload)

	// An admin stages it; the server computes the digest itself.
	resp := rawDo(t, s, http.MethodPut, "/v1/workers/w1/upgrade/file", testToken, payload)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stage status = %d", resp.StatusCode)
	}
	var staged struct {
		SHA256 string `json:"sha256"`
		Size   int64  `json:"size"`
	}
	decode(t, resp, &staged)
	if staged.SHA256 != hex.EncodeToString(sum[:]) || staged.Size != int64(len(payload)) {
		t.Fatalf("staged = %+v", staged)
	}

	// The worker's own token downloads it.
	resp = rawDo(t, s, http.MethodGet, "/v1/workers/w1/upgrade/file", "tok-w1", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("own token status = %d", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, payload) || resp.Header.Get("X-Gofer-Sha256") != staged.SHA256 {
		t.Fatalf("download body=%q sha=%q", got, resp.Header.Get("X-Gofer-Sha256"))
	}

	// Another worker's token, and an ordinary caller token, are refused.
	for name, tok := range map[string]string{"other worker": "tok-w2", "user caller": testToken} {
		if resp := rawDo(t, s, http.MethodGet, "/v1/workers/w1/upgrade/file", tok, nil); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403", name, resp.StatusCode)
		}
	}
	if resp := rawDo(t, s, http.MethodGet, "/v1/workers/w1/upgrade/file", "bogus", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("bad token status = %d, want 401", resp.StatusCode)
	}
	// A worker token may not stage or trigger upgrades.
	if resp := rawDo(t, s, http.MethodPut, "/v1/workers/w1/upgrade/file", "tok-w1", payload); resp.StatusCode != http.StatusForbidden {
		t.Errorf("worker staging status = %d, want 403", resp.StatusCode)
	}
	if resp := do(t, s, http.MethodPost, "/v1/workers/w1/upgrade", "tok-w1", map[string]any{}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("worker-triggered upgrade status = %d, want 403", resp.StatusCode)
	}
}

func TestWorkerUpgradeAcceptedRecordsPending(t *testing.T) {
	t.Parallel()
	s, fu, mgr := newUpgradeServer(t, map[string]WorkerStatus{"w1": connectedWorker("linux", "amd64", 15)})
	rawDo(t, s, http.MethodPut, "/v1/workers/w1/upgrade/file", testToken, []byte("bin"))
	fu.version = "v2.0"
	resp := do(t, s, http.MethodPost, "/v1/workers/w1/upgrade", testToken, map[string]any{"source": "staged", "force": true, "drain_timeout_sec": 30})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	if fu.got.SHA256 == "" || fu.got.Size != 3 || !fu.got.Force || fu.got.DrainTimeoutSec != 30 || fu.got.URLPath != "/v1/workers/w1/upgrade/file" {
		t.Fatalf("frame = %+v", fu.got)
	}
	rec, ok := mgr.Latest("w1")
	if !ok || rec.State != workerupgrade.StatePending || rec.TargetVersion != "v2.0" || rec.UpgradeID != fu.got.RequestID || rec.FromVersion != "v1.0" {
		t.Fatalf("record = %+v ok=%v", rec, ok)
	}
	// GET /v1/workers/{id} shows it.
	var view struct {
		Upgrade *workerupgrade.Record `json:"upgrade"`
	}
	decode(t, do(t, s, http.MethodGet, "/v1/workers/w1", testToken, nil), &view)
	if view.Upgrade == nil || view.Upgrade.State != workerupgrade.StatePending {
		t.Fatalf("view upgrade = %+v", view.Upgrade)
	}
	// A second upgrade while one is pending is refused.
	if resp := do(t, s, http.MethodPost, "/v1/workers/w1/upgrade", testToken, map[string]any{"source": "staged"}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("second upgrade status = %d, want 409", resp.StatusCode)
	}
}

func TestWorkerUpgradeRejections(t *testing.T) {
	t.Parallel()
	s, fu, mgr := newUpgradeServer(t, map[string]WorkerStatus{
		"w1": connectedWorker("plan9", "mips", 15),
		"w2": connectedWorker(runtime.GOOS, runtime.GOARCH, 14),
	})
	// Platform mismatch for the server-binary source -> point at --file.
	resp := do(t, s, http.MethodPost, "/v1/workers/w1/upgrade", testToken, map[string]any{})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("mismatch status = %d, want 409", resp.StatusCode)
	}
	// Old protocol -> manual-upgrade hint.
	resp = do(t, s, http.MethodPost, "/v1/workers/w2/upgrade", testToken, map[string]any{})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("old protocol status = %d, want 409", resp.StatusCode)
	}
	// Staged source without a staged file.
	s2, _, _ := newUpgradeServer(t, map[string]WorkerStatus{"w1": connectedWorker("linux", "amd64", 15)})
	if resp := do(t, s2, http.MethodPost, "/v1/workers/w1/upgrade", testToken, map[string]any{"source": "staged"}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("not staged status = %d, want 400", resp.StatusCode)
	}
	// Unknown worker.
	if resp := do(t, s, http.MethodPost, "/v1/workers/nope/upgrade", testToken, map[string]any{}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown worker status = %d, want 404", resp.StatusCode)
	}
	if fu.called != 0 {
		t.Fatal("no request may reach the worker when the server refuses first")
	}
	if _, ok := mgr.Latest("w1"); ok {
		t.Fatal("a refused request must not create an upgrade record")
	}
}

func TestWorkerUpgradeWorkerRefusalMarksFailed(t *testing.T) {
	t.Parallel()
	s, fu, mgr := newUpgradeServer(t, map[string]WorkerStatus{"w1": connectedWorker(runtime.GOOS, runtime.GOARCH, 15)})
	fu.err = errors.New("upgrade checksum mismatch: got a want b")
	// Default source = the server's own executable (same os/arch).
	resp := do(t, s, http.MethodPost, "/v1/workers/w1/upgrade", testToken, map[string]any{})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	rec, ok := mgr.Latest("w1")
	if !ok || rec.State != workerupgrade.StateFailed || rec.Error == "" {
		t.Fatalf("record = %+v", rec)
	}
	if fu.got.Version != "v9.9 (abcdef0)" {
		t.Fatalf("target version = %q", fu.got.Version)
	}
}

func TestRunnersReportsServerPlatform(t *testing.T) {
	t.Parallel()
	s, _, _ := newUpgradeServer(t, nil)
	var body struct {
		Server struct {
			OS, Arch, Version string
		} `json:"server"`
	}
	decode(t, do(t, s, http.MethodGet, "/v1/runners", testToken, nil), &body)
	if body.Server.OS != runtime.GOOS || body.Server.Arch != runtime.GOARCH || body.Server.Version != "v9.9 (abcdef0)" {
		t.Fatalf("server = %+v", body.Server)
	}
}
