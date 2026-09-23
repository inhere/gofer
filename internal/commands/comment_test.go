package commands

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// commentAPIServer records every request and answers the comment endpoints the CLI
// talks to. It is a hand-written stub (the CLI is a pure HTTP client, so a stub is the
// whole contract) — one entry per (method, path).
type commentCall struct {
	method string
	path   string
	body   string
}

func newCommentAPIServer(t *testing.T, calls *[]commentCall) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !strings.HasSuffix(r.URL.Path, "/comments") {
			// newClient probes /v1/stats for the server timezone; not this test's subject.
			_, _ = w.Write([]byte("{}"))
			return
		}
		b, _ := io.ReadAll(r.Body)
		*calls = append(*calls, commentCall{method: r.Method, path: r.URL.Path, body: string(b)})
		switch {
		case r.Method == http.MethodPost:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "cm-1", "scope": "job", "scope_id": "job-1", "author": "default",
				"author_kind": "user", "body": "@ok 补上测试", "mentions": []string{"ok"},
				"created_at": 100, "triggered_job_id": "job-2",
				"dispatched": []map[string]string{{"mention": "ok", "kind": "agent", "job_id": "job-2"}},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"comments": []map[string]any{{
					"id": "cm-1", "scope": "job", "scope_id": "job-1", "author": "default",
					"author_kind": "user", "body": "@ok 补上测试", "mentions": []string{"ok"},
					"created_at": 100, "triggered_job_id": "job-2",
				}},
			})
		}
	}))
}

// TestJobAndPlanCommentCommands covers the CLI faces of MCP-05 阶段 A: posting and
// listing a job thread and a plan thread, the --todo variant, and the in-job identity
// rule (with GOFER_JOB_ID set the CLI sends it as as_job — the comment is that agent's,
// so it dispatches nothing).
func TestJobAndPlanCommentCommands(t *testing.T) {
	t.Run("job", testJobCommentCommands)
	t.Run("plan", testPlanCommentCommands)
}

// testJobCommentCommands: `gofer job comment <job> <text>` posts to the job's thread and
// prints the dispatched job, `gofer job comments <job>` lists it.
func testJobCommentCommands(t *testing.T) {
	var calls []commentCall
	ts := newCommentAPIServer(t, &calls)
	defer ts.Close()
	// An interactive terminal: no job identity, so the caller writes as the user.
	t.Setenv("GOFER_JOB_ID", "")

	app := NewApp("test")
	out := captureOutput(t, func() {
		if code := app.Run([]string{"job", "comment", "job-1", "@ok 补上测试", "--server", ts.URL}); code != 0 {
			t.Fatalf("job comment exit code=%d", code)
		}
	})
	if !strings.Contains(out, "already dispatched") && !strings.Contains(out, "job-2") {
		t.Fatalf("comment output does not name the dispatched job:\n%s", out)
	}

	out = captureOutput(t, func() {
		if code := app.Run([]string{"job", "comments", "job-1", "--server", ts.URL}); code != 0 {
			t.Fatalf("job comments exit code=%d", code)
		}
	})
	if !strings.Contains(out, "cm-1") || !strings.Contains(out, "@ok 补上测试") {
		t.Fatalf("comments output missing the row:\n%s", out)
	}

	if len(calls) != 2 {
		t.Fatalf("calls = %+v, want two", calls)
	}
	if calls[0].method != http.MethodPost || calls[0].path != "/v1/jobs/job-1/comments" {
		t.Fatalf("comment call = %+v", calls[0])
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(calls[0].body), &body); err != nil {
		t.Fatalf("decode comment body: %v (%s)", err, calls[0].body)
	}
	if body["body"] != "@ok 补上测试" {
		t.Fatalf("comment body = %q", body["body"])
	}
	if _, ok := body["as_job"]; ok {
		t.Fatalf("an interactive CLI comment carried as_job: %s", calls[0].body)
	}
	if calls[1].method != http.MethodGet || calls[1].path != "/v1/jobs/job-1/comments" {
		t.Fatalf("list call = %+v", calls[1])
	}

	// Inside a job the CLI speaks as that job's agent (same rule as gofer_comment).
	calls = nil
	t.Setenv("GOFER_JOB_ID", "job-inside")
	captureOutput(t, func() {
		if code := app.Run([]string{"job", "comment", "job-1", "记录一句", "--server", ts.URL}); code != 0 {
			t.Fatalf("in-job comment exit code=%d", code)
		}
	})
	if len(calls) != 1 {
		t.Fatalf("calls = %+v, want one", calls)
	}
	body = nil
	if err := json.Unmarshal([]byte(calls[0].body), &body); err != nil {
		t.Fatalf("decode comment body: %v", err)
	}
	if body["as_job"] != "job-inside" {
		t.Fatalf("as_job = %q, want job-inside (%s)", body["as_job"], calls[0].body)
	}
}

// testPlanCommentCommands: a plan-level comment, the --todo variant that addresses one
// checklist item's thread, and the plan listing.
func testPlanCommentCommands(t *testing.T) {
	var calls []commentCall
	ts := newCommentAPIServer(t, &calls)
	defer ts.Close()

	app := NewApp("test")
	captureOutput(t, func() {
		if code := app.Run([]string{"plan", "comment", "plan-1", "计划怎么看", "--server", ts.URL}); code != 0 {
			t.Fatalf("plan comment exit code=%d", code)
		}
	})
	captureOutput(t, func() {
		if code := app.Run([]string{"plan", "comment", "plan-1", "--todo", "todo-1", "@ok 你来做", "--server", ts.URL}); code != 0 {
			t.Fatalf("plan comment --todo exit code=%d", code)
		}
	})
	out := captureOutput(t, func() {
		if code := app.Run([]string{"plan", "comments", "plan-1", "--server", ts.URL}); code != 0 {
			t.Fatalf("plan comments exit code=%d", code)
		}
	})
	if !strings.Contains(out, "cm-1") {
		t.Fatalf("plan comments output missing the row:\n%s", out)
	}

	if len(calls) != 3 {
		t.Fatalf("calls = %+v, want three", calls)
	}
	if calls[0].path != "/v1/plans/plan-1/comments" {
		t.Fatalf("plan comment path = %q", calls[0].path)
	}
	if calls[1].path != "/v1/todos/todo-1/comments" {
		t.Fatalf("todo comment path = %q", calls[1].path)
	}
	if calls[2].method != http.MethodGet || calls[2].path != "/v1/plans/plan-1/comments" {
		t.Fatalf("plan list call = %+v", calls[2])
	}
}
