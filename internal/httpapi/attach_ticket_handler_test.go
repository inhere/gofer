package httpapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/ptyrelay"
)

func addAttachTicketJob(t *testing.T, s *Server, id, caller string) {
	t.Helper()
	now := time.Now().Unix()
	if err := s.jobs.Meta().UpsertJob(jobstore.JobRecord{
		ID:          id,
		ProjectKey:  "self",
		Agent:       "exec",
		Runner:      "local",
		Interactive: true,
		Status:      "running",
		Cwd:         ".",
		ResultDir:   t.TempDir(),
		StartedAt:   now,
		UpdatedAt:   now,
		CallerID:    caller,
	}); err != nil {
		t.Fatalf("upsert job: %v", err)
	}
}

func addAttachTicketLiveJob(t *testing.T, s *Server, id, caller string) {
	t.Helper()
	addAttachTicketJob(t, s, id, caller)
	if s.ptyRelays == nil {
		s.SetPtyRelay(ptyrelay.NewNonceStore(), ptyrelay.NewRegistry())
	}
	openAttachRelay(t, s.ptyRelays, id, newAttachFakeSource())
}

func postAttachTicket(t *testing.T, s *Server, id, token, mode string) (*http.Response, map[string]any) {
	t.Helper()
	path := "/v1/jobs/" + id + "/attach-ticket"
	if mode != "" {
		path += "?mode=" + mode
	}
	resp := do(t, s, http.MethodPost, path, token, nil)
	var body map[string]any
	if resp.StatusCode == http.StatusOK {
		decode(t, resp, &body)
	} else {
		resp.Body.Close()
	}
	return resp, body
}

func TestAttachTicketCanAttachOwnJob(t *testing.T) {
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{{ID: "alice", Token: "tok-alice", CanAttach: true}},
	})
	addAttachTicketLiveJob(t, s, "job-own", "alice")

	resp, body := postAttachTicket(t, s, "job-own", "tok-alice", "read")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	if body["ticket"] == "" || int(body["expires_in"].(float64)) != 30 {
		t.Fatalf("unexpected response body: %#v", body)
	}
	b, ok := s.attachTickets.Consume(body["ticket"].(string), time.Now().Unix())
	if !ok || b.Caller != "alice" || b.JobID != "job-own" || b.Mode != "read" || b.PtySessionID != "pty-job-own" {
		t.Fatalf("ticket binding = (%+v,%v), want alice/job-own/pty-job-own/read true", b, ok)
	}
}

func TestAttachTicketRequireAttachCapabilityRejectsCallerWithoutCapability(t *testing.T) {
	s := newTestServerCfg(t, config.ServerConfig{
		Governance: config.GovernanceConfig{RequireAttachCapability: true},
		Callers: []config.CallerConfig{
			{ID: "alice", Token: "tok-alice"},
			{ID: "operator", Token: "tok-operator", CanAttach: true},
		},
	})
	addAttachTicketJob(t, s, "job-owned", "alice")

	resp, _ := postAttachTicket(t, s, "job-owned", "tok-alice", "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", resp.StatusCode)
	}
}

func TestAttachTicketRejectsOtherCallerJob(t *testing.T) {
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "alice", Token: "tok-alice"},
			{ID: "bob", Token: "tok-bob"},
		},
	})
	addAttachTicketLiveJob(t, s, "job-alice", "alice")

	resp, _ := postAttachTicket(t, s, "job-alice", "tok-bob", "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", resp.StatusCode)
	}
}

func TestAttachTicketAdminCanAttachOtherCallerJob(t *testing.T) {
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "alice", Token: "tok-alice"},
			{ID: "admin", Token: "tok-admin", CanAdmin: true},
		},
	})
	addAttachTicketLiveJob(t, s, "job-alice", "alice")

	resp, body := postAttachTicket(t, s, "job-alice", "tok-admin", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	if body["ticket"] == "" {
		t.Fatalf("missing ticket in body: %#v", body)
	}
}

func TestAttachTicketLegacyEmptyCallerJobRequiresAdmin(t *testing.T) {
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "alice", Token: "tok-alice"},
			{ID: "admin", Token: "tok-admin", CanAdmin: true},
		},
	})
	addAttachTicketLiveJob(t, s, "job-legacy", "")

	resp, _ := postAttachTicket(t, s, "job-legacy", "tok-alice", "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin status=%d, want 403", resp.StatusCode)
	}
	resp, body := postAttachTicket(t, s, "job-legacy", "tok-admin", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin status=%d, want 200", resp.StatusCode)
	}
	if body["ticket"] == "" {
		t.Fatalf("missing ticket in body: %#v", body)
	}
}

func TestAttachTicketRejectsNonInteractiveTerminalAndMissingRelay(t *testing.T) {
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{{ID: "alice", Token: "tok-alice", CanAttach: true}},
	})
	addAttachTicketJob(t, s, "job-no-relay", "alice")
	resp, _ := postAttachTicket(t, s, "job-no-relay", "tok-alice", "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("missing relay status=%d, want 409", resp.StatusCode)
	}

	now := time.Now().Unix()
	if err := s.jobs.Meta().UpsertJob(jobstore.JobRecord{
		ID: "job-noninteractive", ProjectKey: "self", Agent: "exec", Runner: "local",
		Status: "running", Cwd: ".", ResultDir: t.TempDir(), StartedAt: now, UpdatedAt: now, CallerID: "alice",
	}); err != nil {
		t.Fatalf("upsert noninteractive job: %v", err)
	}
	resp, _ = postAttachTicket(t, s, "job-noninteractive", "tok-alice", "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("noninteractive status=%d, want 409", resp.StatusCode)
	}

	if err := s.jobs.Meta().UpsertJob(jobstore.JobRecord{
		ID: "job-done", ProjectKey: "self", Agent: "exec", Runner: "local", Interactive: true,
		Status: "done", Cwd: ".", ResultDir: t.TempDir(), StartedAt: now, EndedAt: now, UpdatedAt: now, CallerID: "alice",
	}); err != nil {
		t.Fatalf("upsert done job: %v", err)
	}
	resp, _ = postAttachTicket(t, s, "job-done", "tok-alice", "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("terminal status=%d, want 409", resp.StatusCode)
	}
}

func TestAttachTicketRejectsWorkerToken(t *testing.T) {
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{{ID: "alice", Token: "tok-alice", CanAttach: true}},
		Workers: map[string]config.WorkerAuthConfig{
			"w1": {Token: "tok-worker"},
		},
	})
	addAttachTicketLiveJob(t, s, "job-own", "alice")

	resp, _ := postAttachTicket(t, s, "job-own", "tok-worker", "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", resp.StatusCode)
	}
}

func TestAttachTicketStoreConsumeOnceAndExpiry(t *testing.T) {
	store := NewAttachTicketStore()
	token := store.Issue(AttachTicketBinding{Caller: "alice", JobID: "job-1", Mode: "write", Expiry: 100})
	b, ok := store.Consume(token, 99)
	if !ok || b.Caller != "alice" || b.JobID != "job-1" || b.Mode != "write" {
		t.Fatalf("first consume = (%+v,%v), want binding true", b, ok)
	}
	if _, ok := store.Consume(token, 99); ok {
		t.Fatal("second consume ok, want false")
	}
	expired := store.Issue(AttachTicketBinding{Caller: "alice", JobID: "job-2", Expiry: 100})
	if _, ok := store.Consume(expired, 101); ok {
		t.Fatal("expired consume ok, want false")
	}
}

func TestAttachTicketStoreIssueSweepsExpired(t *testing.T) {
	store := NewAttachTicketStore()
	_ = store.Issue(AttachTicketBinding{Caller: "alice", JobID: "old", Expiry: time.Now().Add(-time.Minute).Unix()})
	_ = store.Issue(AttachTicketBinding{Caller: "alice", JobID: "new", Expiry: time.Now().Add(time.Minute).Unix()})
	if len(store.entries) != 1 {
		t.Fatalf("entries = %d, want 1 after opportunistic sweep", len(store.entries))
	}
}

func TestAttachTicketUnknownJobReturns404(t *testing.T) {
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{{ID: "alice", Token: "tok-alice", CanAttach: true}},
	})

	resp, _ := postAttachTicket(t, s, "missing", "tok-alice", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", resp.StatusCode)
	}
}
