package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
)

// TestJobShowPrintsCheckContinuation: a job whose failed delivery check started a
// continuation shows that job's id (design §4.5).
func TestJobShowPrintsCheckContinuation(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""
	t.Cleanup(func() { jobConnOpts.server, jobConnOpts.token = "", "" })

	checkJSON, _ := json.Marshal(job.StoredCheck{
		CheckResult: job.CheckResult{Schema: "job-check-result/v1", Verdict: "fail", Summary: "one unmet",
			Items: []job.CheckItem{{ID: "c1", Requirement: "two commits", Passed: false, Action: "commit the rest"}}},
		Round: 1, Action: "resume",
	})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/jobs/job-chk/wakeups":
			_, _ = w.Write([]byte(`{"wakeups":[]}`))
			return
		case "/v1/jobs/job-chk/retries":
			_, _ = w.Write([]byte(`{"job_id":"job-chk","retries":[]}`))
			return
		case "/v1/jobs/job-chk":
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(job.JobResult{
			ID: "job-chk", ProjectKey: "self", Status: job.StatusFailed, CheckPrompt: "two commits",
			CheckJSON: string(checkJSON), CheckResumeCount: 1, AutoResumedBy: "job-next",
		})
	}))
	defer ts.Close()
	jobConnOpts.server = ts.URL

	app := NewApp("test")
	out := captureOutput(t, func() {
		if code := app.Run([]string{"job", "show", "job-chk", "--server", ts.URL}); code != 0 {
			t.Fatalf("app.Run exit code=%d", code)
		}
	})
	if !strings.Contains(out, "continued by: job-next") || !strings.Contains(out, "check_resumes: 1") {
		t.Fatalf("job show output missing the check continuation:\n%s", out)
	}
}
