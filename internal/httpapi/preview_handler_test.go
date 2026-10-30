package httpapi

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/testcmd"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// previewServer is a deployment where nobody holds job.read.others (no root admin, every
// governance gate open) and the preview port pool is a small block of free ports, plus a live
// loopback application to preview. The preview manager is stopped before the store closes.
func previewServer(t *testing.T) (*Server, int) {
	t.Helper()
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("app")) }))
	t.Cleanup(app.Close)
	u, _ := url.Parse(app.URL)
	appPort, _ := strconv.Atoi(u.Port())

	base := freePreviewPort(t)
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "alice", Token: "tok-alice"},
			{ID: "bob", Token: "tok-bob"},
		},
		Governance: config.GovernanceConfig{
			RequireAnswerCapability: true, RequireAdminCapability: true,
			RequireAttachCapability: true, RequireTunnelCapability: true,
		},
		Preview: config.PreviewConfig{Enabled: true, PortRange: strconv.Itoa(base) + "-" + strconv.Itoa(base+3)},
	})
	if s.preview == nil {
		t.Fatal("preview manager not wired")
	}
	t.Cleanup(s.preview.CloseAll)
	// Nothing granted in the database either: a caller only reaches its own jobs.
	s.setDBGrants(nil)
	return s, appPort
}

func runningJob(t *testing.T, s *Server, id, owner string) {
	t.Helper()
	now := time.Now().Unix()
	if err := s.jobs.Meta().UpsertJob(jobstore.JobRecord{
		ID: id, ProjectKey: "self", Agent: "exec", Runner: "local", Status: job.StatusRunning,
		Cwd: ".", ResultDir: t.TempDir(), StartedAt: now, UpdatedAt: now, CallerID: owner,
	}); err != nil {
		t.Fatalf("upsert job: %v", err)
	}
}

func TestPreviewRoutesRefuseCallerWhoCannotSeeTheJob(t *testing.T) {
	s, appPort := previewServer(t)
	runningJob(t, s, "job-bob", "bob")
	lease, _, err := s.preview.Create(t.Context(), "job-bob", "bob", "127.0.0.1", appPort, 60, true)
	if err != nil {
		t.Fatalf("seed lease: %v", err)
	}

	for _, tc := range []struct {
		name, method, path string
		body               any
	}{
		{"create", http.MethodPost, "/v1/jobs/job-bob/previews", map[string]any{"port": appPort}},
		{"list", http.MethodGet, "/v1/jobs/job-bob/previews", nil},
		{"close", http.MethodDelete, "/v1/jobs/job-bob/previews/" + lease.ID, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := do(t, s, tc.method, tc.path, "tok-alice", tc.body)
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("alice %s answered %d, want 403 (body %s)", tc.name, resp.StatusCode, readBody(t, resp))
			}
		})
	}

	// The refused calls changed nothing: no new lease, the seeded one still active.
	vs, err := s.preview.List("job-bob")
	if err != nil || len(vs) != 1 || vs[0].State != "active" {
		t.Fatalf("leases after refused calls = %+v err=%v, want the one active lease", vs, err)
	}
}

func TestPreviewOwnerLifecycle(t *testing.T) {
	s, appPort := previewServer(t)
	runningJob(t, s, "job-bob", "bob")
	runningJob(t, s, "job-bob2", "bob")

	resp := do(t, s, http.MethodPost, "/v1/jobs/job-bob/previews", "tok-bob", map[string]any{"port": appPort, "ttl_sec": 60})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create answered %d (body %s)", resp.StatusCode, readBody(t, resp))
	}
	var created struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	decode(t, resp, &created)
	if created.ID == "" || created.URL == "" {
		t.Fatalf("created=%+v", created)
	}

	var listed struct {
		Previews []struct {
			ID string `json:"ID"`
		} `json:"previews"`
	}
	decode(t, do(t, s, http.MethodGet, "/v1/jobs/job-bob/previews", "tok-bob", nil), &listed)
	if len(listed.Previews) != 1 {
		t.Fatalf("listed=%+v", listed)
	}
	// The list never carries the bearer URL or its token.
	raw := readBody(t, do(t, s, http.MethodGet, "/v1/jobs/job-bob/previews", "tok-bob", nil))
	if u, _ := url.Parse(created.URL); u != nil && u.Query().Get("token") != "" && strings.Contains(raw, u.Query().Get("token")) {
		t.Fatalf("list body leaks the preview token: %s", raw)
	}

	// A lease is closed through the job it belongs to, not through any other job's path.
	if r := do(t, s, http.MethodDelete, "/v1/jobs/job-bob2/previews/"+created.ID, "tok-bob", nil); r.StatusCode != http.StatusNotFound {
		t.Fatalf("close via another job's path answered %d, want 404", r.StatusCode)
	}
	if got, _, _ := s.preview.Get(created.ID); got.State != "active" {
		t.Fatalf("state=%q after a mismatched close", got.State)
	}
	if r := do(t, s, http.MethodDelete, "/v1/jobs/job-bob/previews/"+created.ID, "tok-bob", nil); r.StatusCode != http.StatusOK {
		t.Fatalf("close answered %d", r.StatusCode)
	}
	if got, _, _ := s.preview.Get(created.ID); got.State != "closed" || got.CloseReason != "revoked" {
		t.Fatalf("lease=%+v, want closed/revoked", got)
	}
}

func TestPreviewCreateRejectsNonRunningAndDisabled(t *testing.T) {
	s, appPort := previewServer(t)
	now := time.Now().Unix()
	if err := s.jobs.Meta().UpsertJob(jobstore.JobRecord{
		ID: "job-done", ProjectKey: "self", Agent: "exec", Runner: "local", Status: job.StatusDone,
		Cwd: ".", ResultDir: t.TempDir(), StartedAt: now, EndedAt: now, UpdatedAt: now, CallerID: "bob",
	}); err != nil {
		t.Fatal(err)
	}
	if r := do(t, s, http.MethodPost, "/v1/jobs/job-done/previews", "tok-bob", map[string]any{"port": appPort}); r.StatusCode != http.StatusConflict {
		t.Fatalf("finished job answered %d, want 409", r.StatusCode)
	}
	runningJob(t, s, "job-run", "bob")
	for _, port := range []int{0, 70000} {
		if r := do(t, s, http.MethodPost, "/v1/jobs/job-run/previews", "tok-bob", map[string]any{"port": port}); r.StatusCode != http.StatusConflict {
			t.Fatalf("port %d answered %d, want 409", port, r.StatusCode)
		}
	}
	if r := do(t, s, http.MethodPost, "/v1/jobs/no-such-job/previews", "tok-bob", map[string]any{"port": appPort}); r.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown job answered %d, want 404", r.StatusCode)
	}

	s.preview = nil
	if r := do(t, s, http.MethodPost, "/v1/jobs/job-run/previews", "tok-bob", map[string]any{"port": appPort}); r.StatusCode != http.StatusNotFound {
		t.Fatalf("disabled preview answered %d, want 404", r.StatusCode)
	}
}

// A job reaching a terminal state closes its previews through the hook the server registers.
func TestPreviewClosedWhenJobEnds(t *testing.T) {
	s, appPort := previewServer(t)
	res, err := s.jobs.Submit(job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{testcmd.Path(t), "sleep", "60s"}, Cwd: ".", TimeoutSec: 120,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	lease, _, err := s.preview.Create(t.Context(), res.ID, "bob", "127.0.0.1", appPort, 300, true)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.jobs.Cancel(res.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	wait.For(t, 10*time.Second, "preview to be closed after the job ended", func() (bool, any) {
		got, _, _ := s.preview.Get(lease.ID)
		return got.State != "active", got.State
	})
	if got, _, _ := s.preview.Get(lease.ID); got.CloseReason != "job_ended" {
		t.Fatalf("reason=%q, want job_ended", got.CloseReason)
	}
}

// freePreviewPort returns the first of four consecutive ports that are free right now.
func freePreviewPort(t *testing.T) int {
	t.Helper()
	for base := 31000; base < 60000; base += 53 {
		var lns []net.Listener
		ok := true
		for p := base; p < base+4; p++ {
			ln, err := net.Listen("tcp", fmt.Sprintf(":%d", p))
			if err != nil {
				ok = false
				break
			}
			lns = append(lns, ln)
		}
		for _, ln := range lns {
			_ = ln.Close()
		}
		if ok {
			return base
		}
	}
	t.Fatal("no free port block for the preview pool")
	return 0
}
