package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/tunnel"
)

// 隧道领域的旁路关闭用例、存量 autostart 的启动者规则与非用户类分支（设计
// docs/design/2026-10-14-db-capabilities-and-session-grants-design.md §1 的三条目录行、
// §3「有意不保持现状的三处」#2、§6；计划 T1.5f）。

// deploymentWithoutGates is the deployment every test here starts from: no caller holds a
// capability bit and every governance switch is OPEN, so the configuration derives no permission
// at all and the database grants installed through setDBGrants are the whole of a caller's
// authority (the shape the reverse-case harness uses).
func deploymentWithoutGates() config.ServerConfig {
	return config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "alice", Token: "tok-alice"},
			{ID: "bob", Token: "tok-bob"},
		},
		Governance: config.GovernanceConfig{
			RequireAnswerCapability: true, RequireAdminCapability: true,
			RequireAttachCapability: true, RequireTunnelCapability: true,
		},
	}
}

// TestHostedStartNeedsTunnelOpen closes the design's hosted bypass (「有意不保持现状的三处」#2:
// 「tunnel.hosted 另需 tunnel.open：开关打开时，托管隧道不再能绕过隧道权限」): a caller that holds
// tunnel.hosted but NOT tunnel.open is refused when it starts a hosted forwarder, and the 403
// names tunnel.open. The same route with both permissions really starts the forwarder, and the
// registration records the STARTER — not the server — which is what the stop rule and the
// traffic roll-up read.
func TestHostedStartNeedsTunnelOpen(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, deploymentWithoutGates())
	st := openTestStore(t, t.TempDir())
	s.SetTunnelPresets(st)
	if err := st.UpsertTunnelPreset(jobstore.TunnelPresetRecord{
		Name: "demo", Worker: "w-case", SpecsJSON: `["0:127.0.0.1:1"]`, UpdatedAt: time.Now().Unix(), UpdatedBy: "alice",
	}); err != nil {
		t.Fatalf("seed preset: %v", err)
	}
	t.Cleanup(s.StopHostedForwarders)

	// tunnel.hosted only: the gate must refuse the missing tunnel.open.
	s.setDBGrants(grantsFor([]string{authz.PermTunnelHosted}))
	resp := do(t, s, http.MethodPost, "/v1/tunnels/hosted/demo", "tok-alice", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("hosted start with tunnel.hosted but no tunnel.open = %d, want 403 (body %s)",
			resp.StatusCode, readBody(t, resp))
	}
	if body := readBody(t, resp); !strings.Contains(body, authz.PermTunnelOpen) {
		t.Fatalf("the refusal must name %s: %s", authz.PermTunnelOpen, body)
	}
	if got := listForwarders(t, s, "tok-alice"); len(got) != 0 {
		t.Fatalf("a refused start must not register anything: %+v", got)
	}

	// Both permissions: the start succeeds and the entry belongs to alice.
	s.setDBGrants(grantsFor([]string{authz.PermTunnelHosted, authz.PermTunnelOpen}))
	resp = do(t, s, http.MethodPost, "/v1/tunnels/hosted/demo", "tok-alice", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("hosted start with both permissions = %d, want 200 (body %s)", resp.StatusCode, readBody(t, resp))
	}
	_ = resp.Body.Close()
	got := listForwarders(t, s, "tok-alice")
	if len(got) != 1 || !got[0].Hosted || got[0].HostedName != "demo" {
		t.Fatalf("hosted registrations = %+v, want one hosted demo", got)
	}
	if got[0].CallerID != "alice" {
		t.Fatalf("the hosted registration is recorded as %q, want the STARTER alice", got[0].CallerID)
	}
}

// TestHostedAutostartLegacyStarter pins the migration rule of plan T1.5f: an autostart preset
// that predates the permission model still comes up, its starter is the preset's updated_by, and
// a starter that is not a live user-class caller is 按根管理员处理 — only a root administrator may
// stop that forwarder. A preset written by a live caller stays that caller's own.
func TestHostedAutostartLegacyStarter(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, config.ServerConfig{Callers: []config.CallerConfig{
		{ID: "alice", Token: "tok-alice"},
		{ID: "bob", Token: "tok-bob"},
		{ID: "root", Token: "tok-root", CanAdmin: true},
	}})
	st := openTestStore(t, t.TempDir())
	s.SetTunnelPresets(st)
	now := time.Now().Unix()
	for _, rec := range []jobstore.TunnelPresetRecord{
		{Name: "legacy", Worker: "w-case", SpecsJSON: `["0:127.0.0.1:1"]`, Autostart: true, UpdatedAt: now, UpdatedBy: "gone-since"},
		{Name: "bobs", Worker: "w-case", SpecsJSON: `["0:127.0.0.1:1"]`, Autostart: true, UpdatedAt: now, UpdatedBy: "bob"},
	} {
		if err := st.UpsertTunnelPreset(rec); err != nil {
			t.Fatalf("seed preset %s: %v", rec.Name, err)
		}
	}
	t.Cleanup(s.StopHostedForwarders)
	// The dial is never reached: a forwarder only dials when a local connection arrives.
	s.SetHostedForwarderDial(func(_ context.Context, _, _, _, _ string) (tunnel.DialResult, error) {
		return tunnel.DialResult{}, nil
	})

	// Upgrading must not stop the stored autostart forwarders.
	s.StartHostedAutostart()
	got := listForwarders(t, s, "tok-root")
	if len(got) != 2 {
		t.Fatalf("autostart forwarders = %+v, want both presets started", got)
	}

	// The legacy row's starter no longer exists: nobody but a root may stop it.
	resp := do(t, s, http.MethodDelete, "/v1/tunnels/hosted/legacy", "tok-alice", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("stopping a legacy hosted forwarder as a plain caller = %d, want 403 (body %s)",
			resp.StatusCode, readBody(t, resp))
	}
	if body := readBody(t, resp); !strings.Contains(body, authz.PermTunnelManageOthers) {
		t.Fatalf("the refusal must name %s: %s", authz.PermTunnelManageOthers, body)
	}
	resp = do(t, s, http.MethodDelete, "/v1/tunnels/hosted/legacy", "tok-root", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stopping a legacy hosted forwarder as a root = %d, want 200 (body %s)",
			resp.StatusCode, readBody(t, resp))
	}
	_ = resp.Body.Close()

	// A preset written by a live caller belongs to that caller: bob stops his own, and another
	// plain caller may not.
	resp = do(t, s, http.MethodDelete, "/v1/tunnels/hosted/bobs", "tok-alice", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("stopping another caller's hosted forwarder = %d, want 403 (body %s)",
			resp.StatusCode, readBody(t, resp))
	}
	_ = resp.Body.Close()
	resp = do(t, s, http.MethodDelete, "/v1/tunnels/hosted/bobs", "tok-bob", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stopping the caller's own hosted forwarder = %d, want 200 (body %s)",
			resp.StatusCode, readBody(t, resp))
	}
	_ = resp.Body.Close()
}

// TestHostedDialJudgesTheStarter pins the dial half of design §6「判定与登记都按启动者」: the
// in-process dial asks tunnel.open for the STARTER, and once it holds it, the dial drives the
// rendezvous itself — no HTTP call to the server's own tunnel endpoint, no server token. The
// worker never arrives here, so the rendezvous times out: which is exactly the proof that the
// permission rule let the request through and the in-process path was reached.
func TestHostedDialJudgesTheStarter(t *testing.T) {
	// Not parallel: the rendezvous timeout is a package-level knob.
	old := tunnelRendezvousTimeout
	tunnelRendezvousTimeout = 20 * time.Millisecond
	defer func() { tunnelRendezvousTimeout = old }()

	s := newTestServerCfg(t, deploymentWithoutGates())
	s.hub = &tunnelTestHub{live: true}

	s.setDBGrants(nil)
	_, err := s.dialHostedTunnel(context.Background(), "alice", "w1", "tcp", "127.0.0.1:80")
	if err == nil || !strings.Contains(err.Error(), authz.PermTunnelOpen) {
		t.Fatalf("dial without the starter's %s = %v, want a refusal naming it", authz.PermTunnelOpen, err)
	}

	s.setDBGrants(grantsFor([]string{authz.PermTunnelOpen}))
	_, err = s.dialHostedTunnel(context.Background(), "alice", "w1", "tcp", "127.0.0.1:80")
	if !errors.Is(err, tunnel.ErrRendezvousTimeout) {
		t.Fatalf("dial with %s = %v, want the in-process rendezvous to time out waiting for the worker", authz.PermTunnelOpen, err)
	}
}

// TestTunnelNonUserArmsAnswerAsBeforeTheMigration pins the arm the tunnel routes give a caller
// that is NOT governed by permissions (design §2「只对 user 类调用方计算」): the pre-migration
// answer, never a catalog lookup. No non-user caller reaches a tunnel WRITE — SEC-01
// (jobcredential.go) refuses every one of them to a job credential, and workercredential.go's
// whitelist holds no tunnel route for a worker token — while the tunnel lists stay open to a job
// credential exactly as before (SEC-01 admits every GET).
func TestTunnelNonUserArmsAnswerAsBeforeTheMigration(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{{ID: "alice", Token: "tok-alice"}},
		Workers: map[string]config.WorkerAuthConfig{"w1": {Token: "tok-worker"}},
	})
	s.SetTunnelPresets(openTestStore(t, t.TempDir()))

	jobTok := seedJobToken(t, s, "job-tunnel", jobstore.JobCredentialMember, "")
	for _, path := range []string{"/v1/tunnels", "/v1/tunnels/presets", "/v1/tunnels/forwarders"} {
		resp := do(t, s, http.MethodGet, path, jobTok, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("a job credential reading %s = %d, want 200 (body %s)", path, resp.StatusCode, readBody(t, resp))
		}
		_ = resp.Body.Close()
	}

	resp := do(t, s, http.MethodPost, "/v1/tunnels/hosted/demo", jobTok, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a job credential starting a hosted forwarder = %d, want 403 (body %s)", resp.StatusCode, readBody(t, resp))
	}
	if body := readBody(t, resp); !strings.Contains(body, "job credential") {
		t.Fatalf("the refusal must stay SEC-01's: %s", body)
	}

	// A worker token never reaches the tunnel surface at all.
	resp = do(t, s, http.MethodGet, "/v1/tunnels", "tok-worker", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a worker token listing tunnels = %d, want 403 (body %s)", resp.StatusCode, readBody(t, resp))
	}
	_ = resp.Body.Close()
}
