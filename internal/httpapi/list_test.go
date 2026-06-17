package httpapi

import (
	"net/http"
	"testing"

	"dev-agent-bridge/internal/job"
)

// createExec posts an exec job and returns its created id, failing the test on
// any non-200 response.
func createExec(t *testing.T, s *Server, cmd []string) string {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: cmd, Cwd: ".", TimeoutSec: 30,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d, want 200", resp.StatusCode)
	}
	var created job.JobResult
	decode(t, resp, &created)
	if created.ID == "" {
		t.Fatalf("created job has no id: %+v", created)
	}
	return created.ID
}

// listJobs GETs /v1/jobs with an optional raw query string (e.g. "?status=done")
// and returns the decoded jobs slice.
func listJobs(t *testing.T, s *Server, query string) []job.JobResult {
	t.Helper()
	resp := do(t, s, http.MethodGet, "/v1/jobs"+query, testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status=%d, want 200", resp.StatusCode)
	}
	var body struct {
		Jobs []job.JobResult `json:"jobs"`
	}
	decode(t, resp, &body)
	return body.Jobs
}

func TestListJobsEndpoint(t *testing.T) {
	s := newTestServer(t, testToken, false)

	id1 := createExec(t, s, []string{"go", "version"})
	id2 := createExec(t, s, []string{"sh", "-c", "exit 3"})
	if waitDone(t, s, id1).Status != job.StatusDone {
		t.Fatalf("setup: id1 should be done")
	}
	if f := waitDone(t, s, id2); f.Status != job.StatusFailed || f.ExitCode != 3 {
		t.Fatalf("setup: id2 should be failed exit 3, got %s/%d", f.Status, f.ExitCode)
	}

	jobs := listJobs(t, s, "")
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d: %+v", len(jobs), jobs)
	}
	// Sorted by started_at desc (non-increasing).
	if jobs[0].StartedAt < jobs[1].StartedAt {
		t.Fatalf("not sorted by started_at desc: %d before %d", jobs[0].StartedAt, jobs[1].StartedAt)
	}
	for _, j := range jobs {
		if j.ID == "" || j.ProjectKey != "self" || j.Agent != "exec" || j.Status == "" {
			t.Fatalf("incomplete job fields: %+v", j)
		}
	}
}

func TestListJobsEndpointFilters(t *testing.T) {
	s := newTestServer(t, testToken, false)

	id1 := createExec(t, s, []string{"go", "version"})
	id2 := createExec(t, s, []string{"sh", "-c", "exit 3"})
	waitDone(t, s, id1)
	waitDone(t, s, id2)

	// ?status=done -> only the done job.
	done := listJobs(t, s, "?status=done")
	if len(done) != 1 || done[0].ID != id1 {
		t.Fatalf("status=done filter wrong: %+v", done)
	}

	// ?project=self -> both.
	proj := listJobs(t, s, "?project=self")
	if len(proj) != 2 {
		t.Fatalf("project=self expected 2, got %d", len(proj))
	}

	// ?limit=1 -> truncated.
	limited := listJobs(t, s, "?limit=1")
	if len(limited) != 1 {
		t.Fatalf("limit=1 expected 1, got %d", len(limited))
	}
}

func TestListJobsEndpointRequiresAuth(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodGet, "/v1/jobs", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", resp.StatusCode)
	}
}
