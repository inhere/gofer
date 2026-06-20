package httpapi

import (
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/job"
)

// createExecTags posts an exec job with tags and returns its created id.
func createExecTags(t *testing.T, s *Server, cmd []string, tags []string) string {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Tags: tags, Cmd: cmd, Cwd: ".", TimeoutSec: 30,
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

// TestListJobsEndpointTagAgentRunnerSince exercises the E5 query-param mapping
// (tag/agent/runner/since) on GET /v1/jobs, and asserts omitting the params is a
// regression-safe no-op (all jobs returned).
func TestListJobsEndpointTagAgentRunnerSince(t *testing.T) {
	s := newTestServer(t, testToken, false)

	idA := createExecTags(t, s, []string{"go", "version"}, []string{"alpha"})
	idB := createExecTags(t, s, []string{"go", "version"}, []string{"beta"})
	waitDone(t, s, idA)
	waitDone(t, s, idB)

	// No filter params -> both jobs (regression: behaves like before).
	all := listJobs(t, s, "")
	if len(all) != 2 {
		t.Fatalf("no-filter list expected 2 jobs, got %d: %+v", len(all), all)
	}

	// ?tag=alpha -> only idA.
	byTag := listJobs(t, s, "?tag=alpha")
	if len(byTag) != 1 || byTag[0].ID != idA {
		t.Fatalf("tag=alpha filter wrong: %+v", byTag)
	}
	// The returned job echoes its tags.
	if len(byTag[0].Tags) != 1 || byTag[0].Tags[0] != "alpha" {
		t.Fatalf("expected tags [alpha] echoed, got %+v", byTag[0].Tags)
	}

	// ?agent=exec -> both jobs (mapping reaches the agent filter).
	byAgent := listJobs(t, s, "?agent=exec")
	if len(byAgent) != 2 {
		t.Fatalf("agent=exec expected 2, got %d: %+v", len(byAgent), byAgent)
	}

	// ?agent=claude -> none (no such job; proves the param is actually applied).
	byAgentNone := listJobs(t, s, "?agent=claude")
	if len(byAgentNone) != 0 {
		t.Fatalf("agent=claude expected 0, got %d", len(byAgentNone))
	}

	// ?runner=worker -> none (both ran on local).
	byRunner := listJobs(t, s, "?runner=worker")
	if len(byRunner) != 0 {
		t.Fatalf("runner=worker expected 0, got %d", len(byRunner))
	}

	// ?since=<huge> -> none (started_at < since); non-numeric since -> 0 -> no filter.
	bySince := listJobs(t, s, "?since=99999999999")
	if len(bySince) != 0 {
		t.Fatalf("since=future expected 0, got %d", len(bySince))
	}
	bySinceBad := listJobs(t, s, "?since=notanumber")
	if len(bySinceBad) != 2 {
		t.Fatalf("non-numeric since should not filter, expected 2, got %d", len(bySinceBad))
	}
}
