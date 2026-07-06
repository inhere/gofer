package httpapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/ptyrelay"
)

func getJobDetailMap(t *testing.T, s *Server, id, token string) map[string]any {
	t.Helper()
	resp := do(t, s, http.MethodGet, "/v1/jobs/"+id, token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get job status=%d, want 200", resp.StatusCode)
	}
	var body map[string]any
	decode(t, resp, &body)
	return body
}

func TestJobDetailCanAttachComputedOnlyOnDetail(t *testing.T) {
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "alice", Token: "tok-alice"},
			{ID: "bob", Token: "tok-bob"},
		},
	})
	addAttachTicketLiveJob(t, s, "job-own", "alice")

	body := getJobDetailMap(t, s, "job-own", "tok-alice")
	if body["can_attach"] != true {
		t.Fatalf("can_attach=%v, want true (body=%#v)", body["can_attach"], body)
	}
	if body["interactive"] != true || body["caller_id"] != "alice" {
		t.Fatalf("embedded JobResult fields missing: %#v", body)
	}

	body = getJobDetailMap(t, s, "job-own", "tok-bob")
	if body["can_attach"] != false {
		t.Fatalf("non-owner can_attach=%v, want false", body["can_attach"])
	}

	resp := do(t, s, http.MethodGet, "/v1/jobs", "tok-alice", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status=%d, want 200", resp.StatusCode)
	}
	var list map[string]any
	decode(t, resp, &list)
	if jobs, ok := list["jobs"].([]any); !ok || len(jobs) == 0 {
		t.Fatalf("unexpected list body: %#v", list)
	} else if _, exists := jobs[0].(map[string]any)["can_attach"]; exists {
		t.Fatalf("list job unexpectedly contains can_attach: %#v", jobs[0])
	}
}

func TestJobDetailCanAttachFalseForTerminalPendingAndNonInteractive(t *testing.T) {
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{{ID: "alice", Token: "tok-alice"}},
	})
	now := time.Now().Unix()
	if err := s.jobs.Meta().UpsertJob(jobstore.JobRecord{
		ID: "job-done", ProjectKey: "self", Agent: "exec", Runner: "local", Interactive: true,
		Status: "done", Cwd: ".", ResultDir: t.TempDir(), StartedAt: now, EndedAt: now, UpdatedAt: now, CallerID: "alice",
	}); err != nil {
		t.Fatalf("upsert done: %v", err)
	}
	if got := getJobDetailMap(t, s, "job-done", "tok-alice")["can_attach"]; got != false {
		t.Fatalf("terminal can_attach=%v, want false", got)
	}

	addAttachTicketJob(t, s, "job-pending", "alice")
	s.SetPtyRelay(ptyrelay.NewNonceStore(), ptyrelay.NewRegistry())
	s.ptyRelays.Prepare(ptyrelay.RelayBinding{
		JobID: "job-pending", PtySessionID: "pty-job-pending", Nonce: "nonce-pending",
		Expiry: time.Now().Add(time.Minute).Unix(),
	})
	if got := getJobDetailMap(t, s, "job-pending", "tok-alice")["can_attach"]; got != false {
		t.Fatalf("pending relay can_attach=%v, want false", got)
	}

	if err := s.jobs.Meta().UpsertJob(jobstore.JobRecord{
		ID: "job-plain", ProjectKey: "self", Agent: "exec", Runner: "local",
		Status: "running", Cwd: ".", ResultDir: t.TempDir(), StartedAt: now, UpdatedAt: now, CallerID: "alice",
	}); err != nil {
		t.Fatalf("upsert plain: %v", err)
	}
	if got := getJobDetailMap(t, s, "job-plain", "tok-alice")["can_attach"]; got != false {
		t.Fatalf("non-interactive can_attach=%v, want false", got)
	}
}
