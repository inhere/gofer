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

// TestJobRunReadOnlyFlag: `job run --read-only` reaches the server as
// read_only:true in the request body (the flag is bound and mapped; the wire field is
// what actually enables the sandbox).
func TestJobRunReadOnlyFlag(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""
	jobRunOpts.readOnly = false
	t.Cleanup(func() { jobRunOpts.readOnly = false })

	var gotReq job.JobRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/jobs" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(job.JobResult{ID: "job-ro", Status: job.StatusQueued, ReadOnly: true})
	}))
	defer ts.Close()

	code := NewApp("test").Run([]string{
		"job", "run", "-p", "self", "-a", "codex", "--prompt", "audit this",
		"--read-only", "--server", ts.URL,
	})
	if code != 0 {
		t.Fatalf("app.Run exit code=%d", code)
	}
	if !gotReq.ReadOnly {
		t.Fatalf("the CLI must send read_only:true, got %+v", gotReq)
	}
}

// TestJobShowPrintsReadOnly: the read-only flag is part of a job's audit surface, so
// `job show` states it for a finished job (0/absent means the line is omitted).
func TestJobShowPrintsReadOnly(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""
	t.Cleanup(func() { jobConnOpts.server, jobConnOpts.token = "", "" })

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// `job show` also reads the job's wakeups (JOB-09); an empty list is the
		// normal answer here and prints no line.
		if r.URL.Path == "/v1/jobs/job-ro/wakeups" {
			_, _ = w.Write([]byte(`{"wakeups":[]}`))
			return
		}
		// ...and its retry chain (AUTO-03), which prints no line when nothing is
		// waiting either.
		if r.URL.Path == "/v1/jobs/job-ro/retries" {
			_, _ = w.Write([]byte(`{"job_id":"job-ro","retries":[]}`))
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/v1/jobs/job-ro" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(job.JobResult{
			ID: "job-ro", ProjectKey: "self", Status: job.StatusDone, ReadOnly: true,
		})
	}))
	defer ts.Close()
	jobConnOpts.server = ts.URL

	app := NewApp("test")
	out := captureOutput(t, func() {
		if code := app.Run([]string{"job", "show", "job-ro", "--server", ts.URL}); code != 0 {
			t.Fatalf("app.Run exit code=%d", code)
		}
	})
	if !strings.Contains(out, "read_only:  true") {
		t.Fatalf("job show output missing the read_only line:\n%s", out)
	}
}
