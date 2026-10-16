package httpapi

import (
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

func TestAuthAuditSessionGrantAndDeniedRequest(t *testing.T) {
	s := gatesOpenServer(t)
	registerSessionAs(t, s, "tok-alice", "sess-audit")
	grant := createSessionGrant(t, s, "sess-audit", authz.PermTunnelOpen, 3600)
	_ = startTunnel(t, s, "tok-alice", "sess-audit")

	row, ok, err := s.jobs.Meta().GetAuthGrant(grant.ID)
	if err != nil || !ok {
		t.Fatalf("read session grant: ok=%v err=%v", ok, err)
	}
	if row.UseCount != 1 || row.LastUsedAt == 0 {
		t.Fatalf("session grant usage = count %d last_used_at %d, want 1 and non-zero", row.UseCount, row.LastUsedAt)
	}
	entries, err := s.jobs.Meta().ListAuthAudit(jobstore.AuthAuditFilter{Subject: "session:sess-audit", GrantID: grant.ID})
	if err != nil {
		t.Fatal(err)
	}
	var sessionEntry *jobstore.AuthAuditEntry
	for i := range entries {
		if entries[i].Action == "request.session_grant" {
			sessionEntry = &entries[i]
		}
	}
	if sessionEntry == nil || sessionEntry.Permission != authz.PermTunnelOpen {
		t.Fatalf("session audit = %+v, want a session-grant entry", entries)
	}
	if sessionEntry.Actor != "alice" || sessionEntry.Method != http.MethodGet || sessionEntry.Status != http.StatusOK {
		t.Fatalf("session audit fields = %+v", *sessionEntry)
	}

	if resp := do(t, s, http.MethodGet, "/v1/auth/grants", "tok-alice", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unauthorized audit-side request = %d, want 403", resp.StatusCode)
	}
	denied, err := s.jobs.Meta().ListAuthAudit(jobstore.AuthAuditFilter{Subject: "caller:alice", Permission: authz.PermAuthManage})
	if err != nil {
		t.Fatal(err)
	}
	if len(denied) == 0 {
		t.Fatal("missing denied-request audit")
	}
	last := denied[0]
	if last.Action != "request.denied" || last.Method != http.MethodGet || last.Path != "/v1/auth/grants" || last.Status != http.StatusForbidden || last.Reason == "" {
		t.Fatalf("denied audit = %+v", last)
	}
}

func TestAuthAuditSessionGrantNotCountedWhenAnotherSourceHoldsPermission(t *testing.T) {
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "root", Token: "tok-root", CanAdmin: true},
			{ID: "alice", Token: "tok-alice", CanTunnel: true},
		},
		Governance: config.GovernanceConfig{RequireTunnelCapability: true},
	})
	registerSessionAs(t, s, "tok-alice", "sess-source")
	grant := createSessionGrant(t, s, "sess-source", authz.PermTunnelOpen, 3600)
	_ = startTunnel(t, s, "tok-alice", "sess-source")
	row, ok, err := s.jobs.Meta().GetAuthGrant(grant.ID)
	if err != nil || !ok {
		t.Fatalf("read session grant: ok=%v err=%v", ok, err)
	}
	if row.UseCount != 0 || row.LastUsedAt != 0 {
		t.Fatalf("session grant with another source used = %+v, want zero usage", row)
	}
	entries, err := s.jobs.Meta().ListAuthAudit(jobstore.AuthAuditFilter{GrantID: grant.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Action == "request.session_grant" {
			t.Fatalf("unexpected session-grant audit when config also grants permission: %+v", entry)
		}
	}
}

func TestAuthAuditGrantLifecycle(t *testing.T) {
	s := authTestServer(t)
	resp := do(t, s, http.MethodPost, "/v1/auth/grants", "tok-root", map[string]any{
		"subject": "caller:alice", "pattern": authz.PermTunnelOpen,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d, want 201", resp.StatusCode)
	}
	var created authGrantView
	decode(t, resp, &created)
	s.SetServerConfig(&config.ServerConfig{Callers: []config.CallerConfig{
		{ID: "root", Token: "tok-root", CanAdmin: true},
		{ID: "bob", Token: "tok-bob"},
	}})
	// Recreate the declared caller and activate the orphaned row.
	s.SetServerConfig(&config.ServerConfig{Callers: []config.CallerConfig{
		{ID: "root", Token: "tok-root", CanAdmin: true},
		{ID: "alice", Token: "tok-alice"},
	}})
	if resp := do(t, s, http.MethodPost, "/v1/auth/grants/"+created.ID+"/activate", "tok-root", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("activate = %d, want 200", resp.StatusCode)
	}
	if resp := do(t, s, http.MethodDelete, "/v1/auth/grants/"+created.ID, "tok-root", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke = %d, want 200", resp.StatusCode)
	}
	entries, err := s.jobs.Meta().ListAuthAudit(jobstore.AuthAuditFilter{GrantID: created.ID})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		seen[entry.Action] = true
	}
	for _, action := range []string{"grant.create", "grant.activate", "grant.revoke"} {
		if !seen[action] {
			t.Errorf("missing %s audit in %+v", action, entries)
		}
	}
}
