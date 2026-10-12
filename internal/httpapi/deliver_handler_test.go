package httpapi

import (
	"net/http"
	"strings"
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

func TestDeliverJobDryRunEndpoint(t *testing.T) {
	t.Parallel()
	s, _ := newWorktreeTestServer(t)
	resp := do(t, s, http.MethodPost, "/v1/jobs?wait=1", testToken, job.JobRequest{
		ProjectKey: "repo", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"},
		Cwd: ".", TimeoutSec: 60, Worktree: true, Title: "Preview HTTP",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("submit status=%d", resp.StatusCode)
	}
	var created job.JobResult
	decode(t, resp, &created)
	if _, ok := s.jobs.Wait(created.ID); !ok {
		t.Fatal("job missing after submit")
	}
	resp = do(t, s, http.MethodPost, "/v1/jobs/"+created.ID+"/deliver", testToken,
		map[string]any{"dry_run": true, "mode": "pr", "branch": "review/http", "draft": false})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("preview status=%d", resp.StatusCode)
	}
	var preview job.DeliveryPreview
	decode(t, resp, &preview)
	if preview.Mode != "pr" || preview.Branch != created.WorktreeBranch ||
		preview.RemoteBranch != "review/http" || preview.Base != "main" ||
		preview.Title != "Preview HTTP" || !strings.Contains(preview.Body, created.ID) {
		t.Fatalf("preview = %+v", preview)
	}
	if got, _ := s.jobs.Get(created.ID); got.Delivery != nil {
		t.Fatalf("preview recorded delivery: %+v", got.Delivery)
	}
	for _, tc := range []struct {
		mode string
		want int
	}{
		{"none", http.StatusConflict},
		{"invalid", http.StatusBadRequest},
	} {
		resp := do(t, s, http.MethodPost, "/v1/jobs/"+created.ID+"/deliver", testToken,
			map[string]any{"dry_run": true, "mode": tc.mode})
		if resp.StatusCode != tc.want {
			t.Errorf("mode %s: status=%d, want %d", tc.mode, resp.StatusCode, tc.want)
		}
		resp.Body.Close()
	}
}
