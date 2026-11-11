package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// TestPrintJobFallbackSkipped: `job show` lists each job.fallback_skipped event as one line
// (agent and reason), ignores other events, and prints nothing when the events read fails.
func TestPrintJobFallbackSkipped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/jobs/job-1/events" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"events": []jobstore.JobEvent{
			{JobID: "job-1", Type: job.EventJobSubmitted, Detail: `{}`},
			{JobID: "job-1", Type: job.EventJobFallbackSkipped, Detail: `{"agent":"claude","reason":"agent not allowed in project"}`},
		}})
	}))
	defer srv.Close()
	c := bindCmd(NewJobCmd().Subs[0])
	cli := client.New(srv.URL, "")

	out := captureOutput(t, func() { printJobFallbackSkipped(c, cli, "job-1") })
	if !strings.Contains(out, "fallback_skipped: claude (agent not allowed in project)") || strings.Count(out, "fallback_skipped") != 1 {
		t.Fatalf("output = %q, want exactly the claude line", out)
	}
	if out := captureOutput(t, func() { printJobFallbackSkipped(c, cli, "job-missing") }); out != "" {
		t.Fatalf("a failed events read printed %q, want nothing", out)
	}
}
