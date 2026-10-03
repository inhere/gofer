package httpapi

import (
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

func TestJobDeleteHTTPRemovesRecordKeepsAudit(t *testing.T) {
	s := newTestServerCfg(t, config.ServerConfig{Callers: []config.CallerConfig{{ID: "alice", Token: "tok-alice"}}})
	created, status := createJob(t, s, "tok-alice")
	if status != http.StatusOK {
		t.Fatalf("create status=%d, want 200", status)
	}
	waitDoneTok(t, s, created.ID, "tok-alice")
	resp := do(t, s, http.MethodDelete, "/v1/jobs/"+created.ID, "tok-alice", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete status=%d, want 200", resp.StatusCode)
	}
	resp = do(t, s, http.MethodGet, "/v1/jobs/"+created.ID, "tok-alice", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get deleted status=%d, want 404", resp.StatusCode)
	}
}
