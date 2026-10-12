package httpapi

import (
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/job"
)

// POST /v1/jobs/{id}/deliver: 404 for an unknown job, 409 for a job without a
// managed worktree (GIT-02).
func TestDeliverJobEndpoint(t *testing.T) {
	t.Parallel()
	e := newTrackerE2E(t)
	if resp := do(t, e.server, http.MethodPost, "/v1/jobs/nope/deliver", "tok", map[string]any{}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown job: %d", resp.StatusCode)
	}
	resp := do(t, e.server, http.MethodPost, "/v1/jobs", "tok", job.JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 10})
	var jr job.JobResult
	decode(t, resp, &jr)
	if resp := do(t, e.server, http.MethodPost, "/v1/jobs/"+jr.ID+"/deliver", "tok", map[string]any{"mode": "pr"}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("job without worktree: %d", resp.StatusCode)
	}
}
