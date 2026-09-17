package commands

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
)

// TestJobAcceptRejectCommands: `job accept <id> [--note]` and `job reject <id> --note
// [--resume]` reach the review endpoints with the human's decision in the body (the
// note is the audit reason, and — with --resume — the continuation prompt).
func TestJobAcceptRejectCommands(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""
	t.Cleanup(func() { jobConnOpts.server, jobConnOpts.token = "", "" })
	// The review subcommand flags are package globals (like jobRunOpts) and gcli does
	// not reset them between Run calls, so each test starts from the zero literal.
	jobAcceptOpts.note, jobRejectOpts.note, jobRejectOpts.resume = "", "", false
	t.Cleanup(func() { jobAcceptOpts.note, jobRejectOpts.note, jobRejectOpts.resume = "", "", false })

	gotBody := map[string]string{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		gotBody[r.URL.Path] = string(b)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/jobs/job-1/accept":
			_ = json.NewEncoder(w).Encode(job.ReviewOutcome{JobResult: job.JobResult{ID: "job-1", Status: job.StatusDone, ReviewedBy: "alice"}})
		case "/v1/jobs/job-1/reject":
			_ = json.NewEncoder(w).Encode(job.ReviewOutcome{
				JobResult:   job.JobResult{ID: "job-1", Status: job.StatusRejected, ReviewedBy: "alice"},
				ResumeJobID: "job-2",
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	app := NewApp("test")
	out := captureOutput(t, func() {
		if code := app.Run([]string{"job", "accept", "job-1", "--note", "looks good", "--server", ts.URL}); code != 0 {
			t.Fatalf("job accept exit code=%d", code)
		}
		if code := app.Run([]string{"job", "reject", "job-1", "--note", "fix the tests", "--resume", "--server", ts.URL}); code != 0 {
			t.Fatalf("job reject exit code=%d", code)
		}
	})

	if !strings.Contains(gotBody["/v1/jobs/job-1/accept"], `"note":"looks good"`) {
		t.Fatalf("accept body = %s", gotBody["/v1/jobs/job-1/accept"])
	}
	rejectBody := gotBody["/v1/jobs/job-1/reject"]
	if !strings.Contains(rejectBody, `"note":"fix the tests"`) || !strings.Contains(rejectBody, `"resume":true`) {
		t.Fatalf("reject body = %s", rejectBody)
	}
	if !strings.Contains(out, "job-2") {
		t.Fatalf("reject --resume must report the continuation id:\n%s", out)
	}
}

// TestJobRejectRequiresNote: the CLI refuses a reason-less rejection locally — the note
// is what the agent is told to fix, and the server would reject it anyway.
func TestJobRejectRequiresNote(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""
	t.Cleanup(func() { jobConnOpts.server, jobConnOpts.token = "", "" })
	// The review subcommand flags are package globals (like jobRunOpts) and gcli does
	// not reset them between Run calls, so each test starts from the zero literal.
	jobAcceptOpts.note, jobRejectOpts.note, jobRejectOpts.resume = "", "", false
	t.Cleanup(func() { jobAcceptOpts.note, jobRejectOpts.note, jobRejectOpts.resume = "", "", false })

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("a note-less reject must not reach the server: %s", r.URL.Path)
	}))
	defer ts.Close()

	app := NewApp("test")
	if code := app.Run([]string{"job", "reject", "job-1", "--server", ts.URL}); code == 0 {
		t.Fatalf("job reject without --note must fail")
	}
}

// TestJobShowPrintsReview: the review audit fields are part of a job's surface, so
// `job show` states whether review was required and who decided what (absent/zero
// values keep the lines out).
func TestJobShowPrintsReview(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""
	t.Cleanup(func() { jobConnOpts.server, jobConnOpts.token = "", "" })
	// The review subcommand flags are package globals (like jobRunOpts) and gcli does
	// not reset them between Run calls, so each test starts from the zero literal.
	jobAcceptOpts.note, jobRejectOpts.note, jobRejectOpts.resume = "", "", false
	t.Cleanup(func() { jobAcceptOpts.note, jobRejectOpts.note, jobRejectOpts.resume = "", "", false })

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/jobs/job-rv" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(job.JobResult{
			ID: "job-rv", ProjectKey: "self", Status: job.StatusRejected,
			RequireReview: true, ReviewedBy: "alice", ReviewedAt: 1_700_000_000, ReviewNote: "not acceptable",
		})
	}))
	defer ts.Close()
	jobConnOpts.server = ts.URL

	app := NewApp("test")
	out := captureOutput(t, func() {
		if code := app.Run([]string{"job", "show", "job-rv", "--server", ts.URL}); code != 0 {
			t.Fatalf("app.Run exit code=%d", code)
		}
	})
	for _, want := range []string{"require_review: true", "reviewed_by: alice", "review_note: not acceptable"} {
		if !strings.Contains(out, want) {
			t.Fatalf("job show output missing %q:\n%s", want, out)
		}
	}
}

// TestJobRunReviewFlag: `job run --review` reaches the server as review:true in the
// request body (the flag the server's finish gate reads).
func TestJobRunReviewFlag(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""
	jobRunOpts.review = false
	t.Cleanup(func() { jobRunOpts.review = false })

	var gotReq job.JobRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/jobs" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(job.JobResult{ID: "job-rv", Status: job.StatusQueued, RequireReview: true})
	}))
	defer ts.Close()

	code := NewApp("test").Run([]string{
		"job", "run", "-p", "self", "-a", "codex", "--prompt", "do the thing",
		"--review", "--server", ts.URL,
	})
	if code != 0 {
		t.Fatalf("app.Run exit code=%d", code)
	}
	if !gotReq.Review {
		t.Fatalf("the CLI must send review:true, got %+v", gotReq)
	}
}
