package httpapi

import (
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

func TestJobRedactOwnerOrAdminOnly(t *testing.T) {
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "alice", Token: "tok-alice"},
			{ID: "bob", Token: "tok-bob"},
			{ID: "admin", Token: "tok-admin", CanAdmin: true},
		},
	})
	created, status := createJob(t, s, "tok-alice")
	if status != http.StatusOK {
		t.Fatalf("create status=%d, want 200", status)
	}
	waitDoneTok(t, s, created.ID, "tok-alice")
	body := map[string]any{"literals": []string{"fake-secret"}}
	if resp := do(t, s, http.MethodPost, "/v1/jobs/"+created.ID+"/redact", "tok-bob", body); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-owner redact status=%d, want 403", resp.StatusCode)
	}
	if resp := do(t, s, http.MethodPost, "/v1/jobs/"+created.ID+"/redact", "tok-admin", body); resp.StatusCode != http.StatusOK {
		t.Fatalf("admin redact status=%d, want 200", resp.StatusCode)
	}
}
