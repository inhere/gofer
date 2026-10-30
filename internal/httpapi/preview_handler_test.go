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

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/client"
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
		ID         string `json:"id"`
		URL        string `json:"url"`
		Port       int    `json:"port"`
		TargetPort int    `json:"target_port"`
	}
	decode(t, resp, &created)
	if created.ID == "" || created.URL == "" {
		t.Fatalf("created=%+v", created)
	}
	// port is the preview port the browser opens (it is in the URL), target_port the job's.
	if u, _ := url.Parse(created.URL); u == nil || u.Port() != strconv.Itoa(created.Port) || created.TargetPort != appPort || created.Port == appPort {
		t.Fatalf("created=%+v, want port = preview port in the URL and target_port = %d", created, appPort)
	}

	// The list uses the snake_case names the CLI client decodes.
	var listed struct {
		Previews []struct {
			ID        string `json:"id"`
			JobID     string `json:"job_id"`
			CallerID  string `json:"caller_id"`
			Port      int    `json:"port"`
			State     string `json:"state"`
			CreatedAt int64  `json:"created_at"`
			ExpiresAt int64  `json:"expires_at"`
		} `json:"previews"`
	}
	decode(t, do(t, s, http.MethodGet, "/v1/jobs/job-bob/previews", "tok-bob", nil), &listed)
	if len(listed.Previews) != 1 {
		t.Fatalf("listed=%+v", listed)
	}
	if p := listed.Previews[0]; p.ID != created.ID || p.JobID != "job-bob" || p.CallerID != "bob" || p.Port != created.Port || p.State != "active" || p.CreatedAt == 0 || p.ExpiresAt == 0 {
		t.Fatalf("listed preview=%+v, want the lease under snake_case names", p)
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

func TestPreviewListDecodesInTheClient(t *testing.T) {
	s, appPort := previewServer(t)
	runningJob(t, s, "job-bob", "bob")
	lease, _, err := s.preview.Create(t.Context(), "job-bob", "bob", "127.0.0.1", appPort, 60, true)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Previews []client.JobPreview `json:"previews"`
	}
	decode(t, do(t, s, http.MethodGet, "/v1/jobs/job-bob/previews", "tok-bob", nil), &out)
	if len(out.Previews) != 1 || out.Previews[0].ID != lease.ID || out.Previews[0].Port != lease.Port || out.Previews[0].State != "active" || out.Previews[0].ExpiresAt != lease.ExpiresAt {
		t.Fatalf("client view=%+v, want lease %+v", out.Previews, lease)
	}
}

// Changing a lease: your own needs only read access to the job; someone else's needs the
// job's owner or job.manage.others.
func TestPreviewRevokeAndRenewPermissions(t *testing.T) {
	s, appPort := previewServer(t)
	s.setDBGrants(grantsFor([]string{authz.PermJobReadOthers}))
	runningJob(t, s, "job-bob", "bob")

	mk := func(caller string) string {
		t.Helper()
		l, _, err := s.preview.Create(t.Context(), "job-bob", caller, "127.0.0.1", appPort, 600, true)
		if err != nil {
			t.Fatal(err)
		}
		return l.ID
	}
	state := func(id string) string { g, _, _ := s.preview.Get(id); return g.State }
	renew := func(tok, id string) int {
		return do(t, s, http.MethodPost, "/v1/jobs/job-bob/previews/"+id+"/renew", tok, map[string]any{"ttl_sec": 120}).StatusCode
	}
	revoke := func(tok, id string) int {
		return do(t, s, http.MethodDelete, "/v1/jobs/job-bob/previews/"+id, tok, nil).StatusCode
	}

	// alice can read bob's job (granted) but has no manage right: bob's lease is off limits.
	bobLease := mk("bob")
	if got := revoke("tok-alice", bobLease); got != http.StatusForbidden {
		t.Fatalf("alice revoking bob's lease answered %d, want 403", got)
	}
	if got := renew("tok-alice", bobLease); got != http.StatusForbidden {
		t.Fatalf("alice renewing bob's lease answered %d, want 403", got)
	}
	if state(bobLease) != "active" {
		t.Fatal("a refused revoke changed the lease")
	}
	// ... her own lease on that job is hers to change.
	aliceLease := mk("alice")
	if got := renew("tok-alice", aliceLease); got != http.StatusOK {
		t.Fatalf("alice renewing her own lease answered %d, want 200", got)
	}
	// The job owner may change a lease somebody else made on the job.
	if got := renew("tok-bob", aliceLease); got != http.StatusOK {
		t.Fatalf("owner renewing alice's lease answered %d, want 200", got)
	}
	if got := revoke("tok-bob", aliceLease); got != http.StatusOK || state(aliceLease) != "closed" {
		t.Fatalf("owner revoking alice's lease answered %d state=%q", got, state(aliceLease))
	}
	// job.manage.others opens other people's leases too.
	s.setDBGrants(grantsFor([]string{authz.PermJobReadOthers, authz.PermJobManageOthers}))
	if got := revoke("tok-alice", bobLease); got != http.StatusOK || state(bobLease) != "closed" {
		t.Fatalf("manage.others revoke answered %d state=%q", got, state(bobLease))
	}
	// Renewing a lease that is no longer active is a conflict, not a revival.
	if got := renew("tok-bob", bobLease); got != http.StatusConflict {
		t.Fatalf("renew of a closed lease answered %d, want 409", got)
	}
}

func TestPreviewRevokeAndRenewRefuseCallerWhoCannotSeeTheJob(t *testing.T) {
	s, appPort := previewServer(t)
	runningJob(t, s, "job-bob", "bob")
	lease, _, err := s.preview.Create(t.Context(), "job-bob", "bob", "127.0.0.1", appPort, 600, true)
	if err != nil {
		t.Fatal(err)
	}
	before, _, _ := s.preview.Get(lease.ID)
	if got := do(t, s, http.MethodPost, "/v1/jobs/job-bob/previews/"+lease.ID+"/renew", "tok-alice", map[string]any{"ttl_sec": 7000}).StatusCode; got != http.StatusForbidden {
		t.Fatalf("renew by a stranger answered %d, want 403", got)
	}
	after, _, _ := s.preview.Get(lease.ID)
	if after.ExpiresAt != before.ExpiresAt || after.State != "active" {
		t.Fatalf("lease changed by a refused call: %+v -> %+v", before, after)
	}
}

func TestPreviewRenewMovesExpiry(t *testing.T) {
	s, appPort := previewServer(t)
	runningJob(t, s, "job-bob", "bob")
	var created struct {
		ID string `json:"id"`
	}
	decode(t, do(t, s, http.MethodPost, "/v1/jobs/job-bob/previews", "tok-bob", map[string]any{"port": appPort, "ttl_sec": 30}), &created)
	var renewed struct {
		ID        string `json:"id"`
		State     string `json:"state"`
		ExpiresAt int64  `json:"expires_at"`
	}
	resp := do(t, s, http.MethodPost, "/v1/jobs/job-bob/previews/"+created.ID+"/renew", "tok-bob", map[string]any{"ttl_sec": 900})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("renew answered %d (body %s)", resp.StatusCode, readBody(t, resp))
	}
	decode(t, resp, &renewed)
	if now := time.Now().Unix(); renewed.ID != created.ID || renewed.State != "active" || renewed.ExpiresAt < now+890 || renewed.ExpiresAt > now+910 {
		t.Fatalf("renewed=%+v, want active with expiry about now+900", renewed)
	}
	if got := do(t, s, http.MethodPost, "/v1/jobs/job-bob2/previews/"+created.ID+"/renew", "tok-bob", nil).StatusCode; got != http.StatusNotFound {
		t.Fatalf("renew via an unknown job answered %d, want 404", got)
	}
}
