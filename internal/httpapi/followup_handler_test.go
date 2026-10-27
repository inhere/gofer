package httpapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

func TestFollowUpUnknownAndNotLiveStatuses(t *testing.T) {
	s := newTestServer(t, "tok", false)
	resp := do(t, s, http.MethodGet, "/v1/jobs/missing/follow-ups", "tok", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown follow-up list = %d, want 404", resp.StatusCode)
	}
	_ = resp.Body.Close()
	now := time.Now().Unix()
	if err := s.jobs.Meta().UpsertJob(jobstore.JobRecord{ID: "done-follow", ProjectKey: "self", Agent: "exec", Runner: "local", Status: job.StatusDone, ResultDir: t.TempDir(), StartedAt: now, EndedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	resp = do(t, s, http.MethodPost, "/v1/jobs/done-follow/follow-ups", "tok", map[string]any{"prompt": "too late"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("terminal follow-up queue = %d, want 409", resp.StatusCode)
	}
	_ = resp.Body.Close()
}
