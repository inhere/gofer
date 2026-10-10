package commands

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/tracker"
)

// TestJobFindingsCommand (gofer-3nxa.3): `job findings` lists the report's
// 「发现但不碰」 items; --create-issues files them in the current repo's tracker with the
// job id in the description and the given priority/tags.
func TestJobFindingsCommand(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""
	reset := func() { jobFindingsOpts.create, jobFindingsOpts.priority, jobFindingsOpts.tag = false, 2, "discovered" }
	reset()
	t.Cleanup(reset)

	const report = "## 结果\nok\n\n## 发现但不碰\n- internal/x.go:3：重复代码\n- docs/y.md：过时\n"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/jobs/job-f/logs/stdout":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte(report))
		case "/v1/jobs/job-none/logs/stdout":
			_, _ = w.Write([]byte("all good\n"))
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer ts.Close()

	out := captureOutput(t, func() {
		if code := NewApp("test").Run([]string{"job", "findings", "job-f", "--server", ts.URL}); code != 0 {
			t.Fatalf("job findings exit code=%d", code)
		}
	})
	if !strings.Contains(out, "1. internal/x.go:3：重复代码") || !strings.Contains(out, "2. docs/y.md：过时") {
		t.Fatalf("job findings output:\n%s", out)
	}
	out = captureOutput(t, func() {
		if code := NewApp("test").Run([]string{"job", "findings", "job-none", "--server", ts.URL}); code != 0 {
			t.Fatalf("job findings exit code=%d", code)
		}
	})
	if !strings.Contains(out, "no 「发现但不碰」 items") {
		t.Fatalf("empty findings output:\n%s", out)
	}

	repo := t.TempDir()
	store, _, err := tracker.Init(repo, "fd", true)
	if err != nil {
		t.Fatalf("tracker init: %v", err)
	}
	t.Chdir(repo)
	reset()
	out = captureOutput(t, func() {
		if code := NewApp("test").Run([]string{"job", "findings", "job-f", "--create-issues", "-p", "3", "--tag", "discovered,scope", "--server", ts.URL}); code != 0 {
			t.Fatalf("job findings --create-issues exit code=%d", code)
		}
	})
	items, err := store.ReadIssues()
	if err != nil {
		t.Fatalf("read issues: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("created %d issues, want 2; output:\n%s", len(items), out)
	}
	for _, it := range items {
		if !strings.Contains(out, it.ID) || it.Priority != 3 || !strings.Contains(it.Description, "discovered in job job-f") ||
			strings.Join(it.Tags, ",") != "discovered,scope" {
			t.Fatalf("issue %+v (output:\n%s)", it, out)
		}
	}
}

// TestJobReviewPrintsFindings: the review screen lists the findings after the report.
func TestJobReviewPrintsFindings(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""
	jobReviewOpts.tail, jobReviewOpts.diff = 0, false
	t.Cleanup(func() { jobReviewOpts.tail, jobReviewOpts.diff = 0, false })

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/jobs/job-r":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"job-r","status":"needs_review"}`))
		case "/v1/jobs/job-r/logs/stdout":
			_, _ = w.Write([]byte("done\n\n## 发现但不碰\n- a.go：坏味道\n"))
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer ts.Close()

	out := captureOutput(t, func() {
		if code := NewApp("test").Run([]string{"job", "review", "job-r", "--tail", "1", "--server", ts.URL}); code != 0 {
			t.Fatalf("job review exit code=%d", code)
		}
	})
	if !strings.Contains(out, "发现但不碰 (1;") || !strings.Contains(out, "- a.go：坏味道") {
		t.Fatalf("job review must list the findings:\n%s", out)
	}
}
