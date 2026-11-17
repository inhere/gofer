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

// TestJobShowPrintsSelfReportedResult: `job show` prints the outcome the job's agent
// reported about itself, with what a person has to do and its findings.
func TestJobShowPrintsSelfReportedResult(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""
	t.Cleanup(func() { jobConnOpts.server, jobConnOpts.token = "", "" })

	report, _ := json.Marshal(job.SelfResultInput{Decision: "human", Reason: "needs a product call",
		ActionRequired: "choose option B or C", Findings: []string{"two designs fit"}})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/jobs/job-sr/wakeups":
			_, _ = w.Write([]byte(`{"wakeups":[]}`))
			return
		case "/v1/jobs/job-sr/events":
			_, _ = w.Write([]byte(`{"events":[]}`))
			return
		case "/v1/jobs/job-sr/retries":
			_, _ = w.Write([]byte(`{"job_id":"job-sr","retries":[]}`))
			return
		case "/v1/jobs/job-sr":
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(job.JobResult{
			ID: "job-sr", ProjectKey: "self", Status: job.StatusDone,
			ResultDecision: "human", ResultReason: "needs a product call", ResultReportJSON: string(report), ResultAttempt: 1,
		})
	}))
	defer ts.Close()
	jobConnOpts.server = ts.URL

	app := NewApp("test")
	out := captureOutput(t, func() {
		if code := app.Run([]string{"job", "show", "job-sr", "--server", ts.URL}); code != 0 {
			t.Fatalf("app.Run exit code=%d", code)
		}
	})
	for _, want := range []string{"reported:   human — needs a product call", "action required: choose option B or C", "- two designs fit"} {
		if !strings.Contains(out, want) {
			t.Fatalf("job show output missing %q:\n%s", want, out)
		}
	}
}
