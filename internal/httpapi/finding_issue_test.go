package httpapi

import (
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/tracker"
)

// A reviewer files a job's finding as an issue: it lands in the mirror of the job's
// tracker, asking again returns the same issue, and the repository receives it on its
// next sync. A job with no tracker gets 422 (the web falls back to the CLI command).
func TestFindingIssueCreatesInMirrorAndSyncsBack(t *testing.T) {
	t.Parallel()
	e := newTrackerE2E(t)
	syncTracker(t, e)
	trackerID := mustConfig(t, e.local).TrackerID

	submit := func(req job.JobRequest) string {
		t.Helper()
		resp := do(t, e.server, http.MethodPost, "/v1/jobs", "tok", req)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("submit status=%d", resp.StatusCode)
		}
		var jr job.JobResult
		decode(t, resp, &jr)
		return jr.ID
	}
	base := job.JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 10}
	withTracker := base
	withTracker.TrackerID = trackerID
	jobID := submit(withTracker)

	type out struct {
		TrackerID string        `json:"tracker_id"`
		Issue     tracker.Issue `json:"issue"`
		Created   bool          `json:"created"`
	}
	resp := do(t, e.server, http.MethodPost, "/v1/jobs/"+jobID+"/findings/issue", "tok", map[string]any{"text": "  flaky   retry in X  "})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status=%d", resp.StatusCode)
	}
	var first out
	decode(t, resp, &first)
	if !first.Created || first.Issue.Title != "flaky retry in X" || first.Issue.Priority != 2 ||
		len(first.Issue.Tags) != 1 || first.Issue.Tags[0] != "discovered" || first.Issue.Description != "discovered in job "+jobID {
		t.Fatalf("created issue: %+v", first)
	}
	resp = do(t, e.server, http.MethodPost, "/v1/jobs/"+jobID+"/findings/issue", "tok", map[string]any{"text": "flaky retry in X"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("repeat status=%d", resp.StatusCode)
	}
	var again out
	decode(t, resp, &again)
	if again.Created || again.Issue.ID != first.Issue.ID {
		t.Fatalf("repeat created a second issue: %+v", again)
	}

	syncTracker(t, e)
	got, err := e.local.Issue(first.Issue.ID)
	if err != nil || got.Title != "flaky retry in X" {
		t.Fatalf("issue did not reach the repository: err=%v issue=%+v", err, got)
	}

	orphan := submit(base) // no tracker_id, and the project has no tracker registered
	if resp := do(t, e.server, http.MethodPost, "/v1/jobs/"+orphan+"/findings/issue", "tok", map[string]any{"text": "x"}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("job without tracker: status=%d", resp.StatusCode)
	}
	if resp := do(t, e.server, http.MethodPost, "/v1/jobs/"+jobID+"/findings/issue", "tok", map[string]any{"text": " "}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty text: status=%d", resp.StatusCode)
	}
}
