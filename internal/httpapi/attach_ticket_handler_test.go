package httpapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

func addAttachTicketJob(t *testing.T, s *Server, id, caller string) {
	t.Helper()
	now := time.Now().Unix()
	if err := s.jobs.Meta().UpsertJob(jobstore.JobRecord{
		ID:         id,
		ProjectKey: "self",
		Agent:      "exec",
		Runner:     "local",
		Status:     "running",
		Cwd:        ".",
		ResultDir:  t.TempDir(),
		StartedAt:  now,
		UpdatedAt:  now,
		CallerID:   caller,
	}); err != nil {
		t.Fatalf("upsert job: %v", err)
	}
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
	addAttachTicketJob(t, s, "job-own", "alice")

	resp, body := postAttachTicket(t, s, "job-own", "tok-alice", "read")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	if body["ticket"] == "" || int(body["expires_in"].(float64)) != 30 {
		t.Fatalf("unexpected response body: %#v", body)
	}
	b, ok := s.attachTickets.Consume(body["ticket"].(string), time.Now().Unix())
	if !ok || b.Caller != "alice" || b.JobID != "job-own" || b.Mode != "read" {
		t.Fatalf("ticket binding = (%+v,%v), want alice/job-own/read true", b, ok)
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
	addAttachTicketJob(t, s, "job-alice", "alice")

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
	addAttachTicketJob(t, s, "job-alice", "alice")

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
	addAttachTicketJob(t, s, "job-legacy", "")

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

func TestAttachTicketUnknownJobReturns404(t *testing.T) {
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{{ID: "alice", Token: "tok-alice", CanAttach: true}},
	})

	resp, _ := postAttachTicket(t, s, "missing", "tok-alice", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", resp.StatusCode)
	}
}
