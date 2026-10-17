package job

import (
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// gofer-3jap: the agent's error_kind decides whether a failure may be auto-resumed. The
// jobs below run as agent "suga" (built-in transient patterns active) and print one
// stream-json error row on stderr, exactly as `suga run --output-format stream-json`.

func errorRow(kind, msg, resetAt string) string {
	row := `{"type":"error","error":"` + msg + `"`
	if kind != "" {
		row += `,"error_kind":"` + kind + `"`
	}
	if resetAt != "" {
		row += `,"reset_at":"` + resetAt + `"`
	}
	return row + `}`
}

// runSugaFailure runs a failing suga job that printed stderrText and returns it.
func runSugaFailure(t *testing.T, stderrText string) JobResult {
	t.Helper()
	root := t.TempDir()
	s := newServiceFromCfg(t, root, autoResumeCfg(testcmd.Path(t), root, "suga", stderrText, nil))
	return submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "suga", Runner: "local",
		Prompt: "do the thing", Cwd: ".", TimeoutSec: 30, SessionID: "sess-kind",
	})
}

func TestErrorKindTransientAutoResumes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// The message matches NO text pattern: only the label makes it transient.
	cfg := autoResumeCfg(testcmd.Path(t), root, "suga", errorRow("provider_transient", "stream ended after retries", ""), nil)
	s := newServiceFromCfg(t, root, cfg)
	src := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "suga", Runner: "local",
		Prompt: "do the thing", Cwd: ".", TimeoutSec: 30, SessionID: "sess-kind",
	})
	if src.Status != StatusFailed || src.FailureClass != FailureClassTransient {
		t.Fatalf("status=%s class=%q err=%q, want failed/transient", src.Status, src.FailureClass, src.Error)
	}
	src = waitAutoResumed(t, s, src.ID, true)
	if final, _ := s.Wait(src.AutoResumedBy); final.Status != StatusDone {
		t.Fatalf("continuation status = %s, want done", final.Status)
	}
}

// Every other kind — and a kind this build has never heard of — stops, even when the
// message itself reads like the old transient patterns (429 / rate limit).
func TestErrorKindNonTransientKindsNeverAutoResume(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"provider_quota", "provider_rejected", "provider_bad_response", "provider_from_the_future"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			cfg := autoResumeCfg(testcmd.Path(t), root, "suga",
				errorRow(kind, "HTTP 429 rate limit exceeded, too many requests", ""), nil)
			s := newServiceFromCfg(t, root, cfg)
			src := submitAndWait(t, s, JobRequest{
				ProjectKey: "self", Agent: "suga", Runner: "local",
				Prompt: "do the thing", Cwd: ".", TimeoutSec: 30, SessionID: "sess-kind",
			})
			if src.Status != StatusFailed || src.FailureClass != FailureClassOther {
				t.Fatalf("status=%s class=%q err=%q, want failed/other", src.Status, src.FailureClass, src.Error)
			}
			waitAutoResumed(t, s, src.ID, false)
			if !hasSubsequence(eventTypes(t, s, src.ID), []string{EventJobTerminal}) {
				t.Fatal("a non-transient failure must emit job.terminal")
			}
		})
	}
}

func TestErrorKindQuotaShowsResetAt(t *testing.T) {
	t.Parallel()
	src := runSugaFailure(t, errorRow("provider_quota", "weekly usage limit reached", "2026-10-19T00:00:00Z"))
	if src.FailureClass != FailureClassOther {
		t.Fatalf("class = %q, want other", src.FailureClass)
	}
	if !strings.Contains(src.Error, "2026-10-19T00:00:00Z") || !strings.Contains(src.Error, "quota") {
		t.Fatalf("job error = %q, want the quota reset time", src.Error)
	}
}

// Without error_kind the text fallback still works for ordinary transient text...
func TestErrorKindAbsentFallsBackToText(t *testing.T) {
	t.Parallel()
	src := runSugaFailure(t, `{"type":"error","error":"HTTP 503 service temporarily unavailable"}`)
	if src.FailureClass != FailureClassTransient {
		t.Fatalf("class = %q (err=%q), want transient by text", src.FailureClass, src.Error)
	}
}

// ...but quota / usage-limit texts are never transient, even though they also say 429.
func TestQuotaTextIsNotTransientWithoutErrorKind(t *testing.T) {
	t.Parallel()
	for _, text := range []string{
		"HTTP 429 rate_limit_error: you have hit your weekly usage limit; limit resets at 2026-10-19",
		"429 Too Many Requests: insufficient_quota, check your plan and billing details",
		"You exceeded your current quota (rate limit)",
		"request failed: status 402 payment required",
		"usage limit reached, try again later (overloaded)",
	} {
		src := runSugaFailure(t, text)
		if src.FailureClass != FailureClassOther {
			t.Errorf("text %q: class = %q, want other", text, src.FailureClass)
		}
	}
}

func TestErrorKindMarkerFromACPErrorText(t *testing.T) {
	t.Parallel()
	ev, ok := errorKindEvidence(JobResult{
		Error: "acp: session/prompt: acp: jsonrpc error -32603: boom" + runner.ErrorKindMarker("provider_quota", "2026-10-19T00:00:00Z"),
	})
	if !ok || ev.Kind != runner.ErrorKindQuota || ev.ResetAt != "2026-10-19T00:00:00Z" {
		t.Fatalf("evidence = %+v, %v", ev, ok)
	}
	if note := failureErrorNote(JobResult{Error: "x" + runner.ErrorKindMarker("provider_quota", "2026-10-19T00:00:00Z")}); note != "" {
		t.Fatalf("a reset time already in the error must not be repeated: %q", note)
	}
	if _, ok := errorKindEvidence(JobResult{Error: "acp: session/prompt: plain failure"}); ok {
		t.Fatal("no marker, no evidence")
	}
}

func TestErrorKindMarkerClassifiesACPFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newServiceFromCfg(t, root, autoResumeCfg(testcmd.Path(t), root, "suga", "x", nil))
	if hit, ok := s.transientHit(JobResult{Agent: "suga",
		Error: "acp: session/prompt: boom" + runner.ErrorKindMarker("provider_transient", "")}); !ok || !strings.Contains(hit, "provider_transient") {
		t.Fatalf("transient marker: hit=%q ok=%v", hit, ok)
	}
	// A 429 text next to a quota label is a quota failure.
	if _, ok := s.transientHit(JobResult{Agent: "suga",
		Error: "acp: session/prompt: 429 rate limit" + runner.ErrorKindMarker("provider_quota", "")}); ok {
		t.Fatal("quota marker must not be transient")
	}
}

// Part 2: a resume suga refuses for its directory carries suga's message verbatim into
// the job error (exit 1); gofer adds nothing like --allow-cwd-change on its own.
func TestResumeCwdMismatchMessageReachesJobError(t *testing.T) {
	t.Parallel()
	const msg = "suga: session sess-kind was created in /work/old but the current directory is /work/new; pass --allow-cwd-change to resume it here (cwd differs)"
	src := runSugaFailure(t, msg)
	if src.Status != StatusFailed {
		t.Fatalf("status = %s", src.Status)
	}
	if !strings.Contains(src.Error, msg) {
		t.Fatalf("job error = %q, want suga's message verbatim", src.Error)
	}
	// The only occurrence in the command is inside the fake agent's own message argument.
	if strings.Count(src.RenderedCommand, "--allow-cwd-change") != 1 {
		t.Fatalf("gofer must not add --allow-cwd-change: %q", src.RenderedCommand)
	}
}
