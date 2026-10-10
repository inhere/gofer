package commands

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
)

// Hold-for-approval CLI (gofer-9b1b): `job run --hold`, `job approve`, `job reject` of
// a held job and the hold section of `job show`.

// heldJob is the awaiting_approval snapshot the stub servers answer with.
func heldJob(id string) job.JobResult {
	return job.JobResult{
		ID: id, ProjectKey: "self", Agent: "exec", Status: job.StatusAwaitingApproval,
		ResultDir: "/tmp/r/" + id,
		Hold: &job.HoldState{
			Reason: "push the release branch", TimeoutSec: 3600, ExpiresAt: 1800003600,
			Origin: "agent-session:s1", Command: []string{"git", "push", "origin", "main"},
		},
	}
}

// TestJobApproveRefusedInAgentSession: any agent-session / job-credential variable makes
// `job approve` refuse locally — nothing reaches the server, and there is no flag around it.
func TestJobApproveRefusedInAgentSession(t *testing.T) {
	isolateConfigEnv(t)
	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "must not be called", http.StatusTeapot)
	}))
	defer ts.Close()

	for _, key := range approvalSessionEnvKeys {
		t.Run(key, func(t *testing.T) {
			for _, k := range approvalSessionEnvKeys {
				t.Setenv(k, "")
			}
			t.Setenv(key, "agent-xyz")
			app := NewApp("test")
			out := captureOutput(t, func() {
				if code := app.Run([]string{"job", "approve", "job-h", "--server", ts.URL}); code == 0 {
					t.Errorf("job approve under %s must fail", key)
				}
			})
			if !strings.Contains(out, "批准必须由人在 web 或 agent 会话之外的终端操作") || !strings.Contains(out, key) {
				t.Errorf("refusal must name the variable and the rule:\n%s", out)
			}
		})
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("a refused approve reached the server %d time(s)", n)
	}
}

// TestJobApproveCommand: outside an agent session `job approve <id> --note` posts the
// approval and reports who approved it.
func TestJobApproveCommand(t *testing.T) {
	isolateConfigEnv(t)
	t.Setenv(config.EnvJobToken, "")
	var body string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/jobs/job-h/approve" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusNotFound)
			return
		}
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		res := heldJob("job-h")
		res.Status = job.StatusQueued
		res.Hold.Decision, res.Hold.DecidedBy = job.HoldDecisionApproved, "alice"
		_ = json.NewEncoder(w).Encode(res)
	}))
	defer ts.Close()

	app := NewApp("test")
	out := captureOutput(t, func() {
		if code := app.Run([]string{"job", "approve", "job-h", "--note", "ok to push", "--server", ts.URL}); code != 0 {
			t.Fatalf("job approve exit code=%d", code)
		}
	})
	if body != `{"note":"ok to push"}` {
		t.Fatalf("approve body = %s", body)
	}
	if !strings.Contains(out, "job job-h approved: status=queued by=alice") {
		t.Fatalf("approve output:\n%s", out)
	}
}

// TestJobRunHoldSync: `job run --hold --sync` submits async (the note says why), prints
// the submitted line and the approval link, never a "finished" line, and does not poll.
func TestJobRunHoldSync(t *testing.T) {
	isolateConfigEnv(t)
	var submitted job.JobRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/jobs" {
			t.Errorf("a held submit must not poll: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusNotFound)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&submitted)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Gofer-Async", "1")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(struct {
			job.JobResult
			ApproveURL string `json:"approve_url"`
		}{heldJob("job-h"), "https://gofer.example/jobs/job-h"})
	}))
	defer ts.Close()

	app := NewApp("test")
	out := captureOutput(t, func() {
		if code := app.Run([]string{"job", "run", "-p", "self", "-a", "exec", "--hold", "--hold-reason", "push the release branch",
			"--hold-timeout", "3600", "--sync", "--server", ts.URL, "--", "git", "push", "origin", "main"}); code != 0 {
			t.Fatalf("job run exit code=%d", code)
		}
	})
	if !submitted.Hold || submitted.HoldReason != "push the release branch" || submitted.HoldTimeoutSec != 3600 || submitted.Sync {
		t.Fatalf("submitted request = hold:%v reason:%q timeout:%d sync:%v", submitted.Hold, submitted.HoldReason, submitted.HoldTimeoutSec, submitted.Sync)
	}
	for _, want := range []string{"job job-h submitted: status=awaiting_approval", "awaiting approval: https://gofer.example/jobs/job-h", "--sync is ignored"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "finished") {
		t.Fatalf("a held submit must not print a finished line:\n%s", out)
	}
}

// TestJobRunHoldWait: --wait polls a held job (slowly while it waits) through to its
// end, with the fallback approval link when the server has no web base URL.
func TestJobRunHoldWait(t *testing.T) {
	isolateConfigEnv(t)
	old := holdPollInterval
	holdPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { holdPollInterval = old })
	var gets atomic.Int32
	var tsURL string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/jobs":
			w.Header().Set("X-Gofer-Async", "1")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(heldJob("job-w"))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/jobs/job-w":
			res := heldJob("job-w")
			if gets.Add(1) >= 3 {
				res.Status = job.StatusDone
			}
			_ = json.NewEncoder(w).Encode(res)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	defer ts.Close()
	tsURL = ts.URL

	app := NewApp("test")
	out := captureOutput(t, func() {
		if code := app.Run([]string{"job", "run", "-p", "self", "-a", "exec", "--hold", "--wait", "--server", ts.URL, "--", "true"}); code != 0 {
			t.Fatalf("job run exit code=%d", code)
		}
	})
	if !strings.Contains(out, "awaiting approval: "+tsURL+"/jobs/job-w") {
		t.Fatalf("fallback approval link missing:\n%s", out)
	}
	if !strings.Contains(out, "job job-w finished: status=done") {
		t.Fatalf("--wait must end with the finished line:\n%s", out)
	}
	if got := jobRunWaitSec(heldJob("x"), 60); got != 60+3600 {
		t.Fatalf("held wait window = %d, want job + hold timeout", got)
	}
	if got := jobRunWaitSec(heldJob("x"), 0); got != 0 {
		t.Fatalf("no --timeout keeps the window open, got %d", got)
	}
}

// TestJobRejectHeldWithoutNote: a held job is rejected without a reason (the job is read
// first to tell it from a delivery), and --reason is accepted as --note.
func TestJobRejectHeldWithoutNote(t *testing.T) {
	isolateConfigEnv(t)
	bodies := map[string]string{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		id := strings.TrimPrefix(strings.TrimSuffix(r.URL.Path, "/reject"), "/v1/jobs/")
		switch {
		case r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(heldJob(id))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/reject"):
			b, _ := io.ReadAll(r.Body)
			bodies[id] = string(b)
			res := heldJob(id)
			res.Status = job.StatusCancelled
			res.Hold.Decision, res.Hold.DecidedBy = job.HoldDecisionRejected, "alice"
			_ = json.NewEncoder(w).Encode(job.ReviewOutcome{JobResult: res})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	defer ts.Close()

	app := NewApp("test")
	out := captureOutput(t, func() {
		if code := app.Run([]string{"job", "reject", "job-a", "--server", ts.URL}); code != 0 {
			t.Fatalf("reason-less reject of a held job exit code=%d", code)
		}
		if code := app.Run([]string{"job", "reject", "job-b", "--reason", "wrong branch", "--server", ts.URL}); code != 0 {
			t.Fatalf("reject --reason exit code=%d", code)
		}
	})
	if bodies["job-a"] != `{}` || bodies["job-b"] != `{"note":"wrong branch"}` {
		t.Fatalf("reject bodies = %v", bodies)
	}
	if !strings.Contains(out, "job job-a rejected (hold): status=cancelled by=alice") {
		t.Fatalf("reject output:\n%s", out)
	}
}

// TestJobShowPrintsHold: `job show` of a held job states why, who submitted it, when it
// expires and what it would run.
func TestJobShowPrintsHold(t *testing.T) {
	isolateConfigEnv(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/jobs/job-s/wakeups":
			_, _ = w.Write([]byte(`{"wakeups":[]}`))
		case "/v1/jobs/job-s/retries":
			_, _ = w.Write([]byte(`{"job_id":"job-s","retries":[]}`))
		case "/v1/jobs/job-s":
			_ = json.NewEncoder(w).Encode(heldJob("job-s"))
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	defer ts.Close()

	app := NewApp("test")
	out := captureOutput(t, func() {
		if code := app.Run([]string{"job", "show", "job-s", "--server", ts.URL}); code != 0 {
			t.Fatalf("job show exit code=%d", code)
		}
	})
	for _, want := range []string{
		"status:     awaiting_approval",
		"hold_reason: push the release branch",
		"hold_origin: agent-session:s1",
		"hold_expires: " + formatStarted(1800003600),
		"hold_cmd:   git push origin main",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("job show missing %q:\n%s", want, out)
		}
	}
}
