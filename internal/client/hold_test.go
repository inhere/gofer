package client

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/inhere/gofer/internal/job"
)

// TestApproveJobAndApproveURL: ApproveJob posts the note to /approve, and a held
// submit surfaces the server's approve_url (falling back to the client's own address
// when the server reports none).
func TestApproveJobAndApproveURL(t *testing.T) {
	var approveBody string
	reported := ""
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/jobs":
			w.Header().Set("X-Gofer-Async", "1")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "job-h", "status": job.StatusAwaitingApproval, "approve_url": reported})
		case "/v1/jobs/job-h/approve":
			b, _ := io.ReadAll(r.Body)
			approveBody = string(b)
			_ = json.NewEncoder(w).Encode(job.JobResult{ID: "job-h", Status: job.StatusQueued})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	defer ts.Close()
	c := New(ts.URL, "")

	sub, err := c.SubmitJobSync(job.JobRequest{ProjectKey: "p", Agent: "exec", Hold: true})
	if err != nil || !sub.Async || sub.ApproveURL != "" {
		t.Fatalf("submit = %+v, %v", sub, err)
	}
	if got := c.ApprovalURL("job-h", sub.ApproveURL); got != ts.URL+"/jobs/job-h" {
		t.Fatalf("fallback approval url = %q", got)
	}
	reported = "https://gofer.example/jobs/job-h"
	sub, err = c.SubmitJobSync(job.JobRequest{ProjectKey: "p", Agent: "exec", Hold: true})
	if err != nil || sub.ApproveURL != reported || c.ApprovalURL("job-h", sub.ApproveURL) != reported {
		t.Fatalf("submit approve_url = %+v, %v", sub, err)
	}

	res, err := c.ApproveJob("job-h", "ship it")
	if err != nil || res.Status != job.StatusQueued {
		t.Fatalf("ApproveJob = %+v, %v", res, err)
	}
	if approveBody != `{"note":"ship it"}` {
		t.Fatalf("approve body = %s", approveBody)
	}
}
