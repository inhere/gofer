package job

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// reviewTransientText is the stderr bait the automatic-continuation path greps for
// (its TRANSIENT_ERROR pattern table ships "at capacity"). It is used to prove that
// the auto-resume configuration in these tests is LIVE, so "reject does not
// auto-continue" is a real assertion rather than a vacuous one.
const reviewTransientText = "ERROR: Selected model is at capacity. Please try a different model."

// reviewServiceOpts configures newReviewService.
type reviewServiceOpts struct {
	// successCode is what the "codex" agent exits with: 0 = a normal完成 (the only
	// path into needs_review), non-zero = a失败 that must NOT enter it.
	successCode int
	// stderrText is printed to stderr by both agents (reviewTransientText makes the
	// auto-continuation live for the "flaky" control agent).
	stderrText string
	// autoMax is server.auto_resume_max: non-nil and > 0 enables the automatic
	// continuation, which only ever fires for a FAILED job.
	autoMax *int
	// requireReview turns the project's require_review default on.
	requireReview bool
}

// newReviewService builds a service over two resumable cli-agents: "codex" exits
// with opts.successCode (the reviewed job) and "flaky" always exits 1 (the control
// that proves an auto-continuation would have fired).
func newReviewService(t *testing.T, root string, opts reviewServiceOpts) *Service {
	t.Helper()
	bin := testcmd.Path(t)
	newAgent := func(code int) config.AgentConfig {
		return config.AgentConfig{
			Type:          agent.TypeCLIAgent,
			Command:       bin,
			Args:          []string{"stderr-exit", strconv.Itoa(code), opts.stderrText, "{{prompt}}"},
			SessionResume: []string{"stderr-exit", "0", "resumed {{prompt}}"},
		}
	}
	cfg := &config.Config{
		Server:  config.ServerConfig{AutoResumeMax: opts.autoMax},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"codex", "flaky", "exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
				RequireReview:  opts.requireReview,
			},
		},
		Agents: map[string]config.AgentConfig{
			"codex": newAgent(opts.successCode),
			"flaky": newAgent(1),
		},
	}
	return newServiceFromCfg(t, root, cfg)
}

// reviewJob submits an exec-agent-agnostic job on the "codex" agent and waits for
// its (finished) snapshot, with the review flag and an explicit session id (so a
// `reject --resume` has something to continue).
func reviewJob(t *testing.T, s *Service) JobResult {
	t.Helper()
	return submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "do the thing", Cwd: ".", TimeoutSec: 30,
		Review: true, SessionID: "sess-review",
	})
}

// TestReviewJobEntersNeedsReview: a --review job that finishes normally parks in
// needs_review — a NON-terminal state whose process has already ended.
func TestReviewJobEntersNeedsReview(t *testing.T) {
	s := newReviewService(t, t.TempDir(), reviewServiceOpts{successCode: 0})
	final := reviewJob(t, s)

	if final.Status != StatusNeedsReview {
		t.Fatalf("status = %s, want %s (err=%s)", final.Status, StatusNeedsReview, final.Error)
	}
	if !final.RequireReview {
		t.Fatalf("require_review must be recorded on the result: %+v", final)
	}
	if final.ExitCode != 0 {
		t.Fatalf("exit_code = %d, want 0", final.ExitCode)
	}
	if IsTerminal(final.Status) {
		t.Fatalf("needs_review must NOT be terminal (retention/resume depend on it)")
	}
	if !IsFinished(final.Status) {
		t.Fatalf("needs_review must be FINISHED (SSE end/attach/eviction depend on it)")
	}

	types := eventTypes(t, s, final.ID)
	for _, ty := range types {
		if ty == EventJobTerminal {
			t.Fatalf("a needs_review job must not record job.terminal: %v", types)
		}
	}
	if !hasSubsequence(types, []string{EventJobSubmitted, EventJobRunning, EventJobNeedsReview}) {
		t.Fatalf("event order mismatch: got %v, want submitted/running/needs_review", types)
	}

	// The process is over: the in-memory entry is evicted, so Get reads the store.
	if s.entry(final.ID) != nil {
		t.Fatalf("a needs_review job must be evicted from the in-memory map")
	}
	got, ok := s.Get(final.ID)
	if !ok || got.Status != StatusNeedsReview {
		t.Fatalf("Get = %+v ok=%v, want the persisted needs_review row", got, ok)
	}

	// The event carries the job id + exit code (the payload an IM subscribes to).
	evs, err := s.ListJobEvents(final.ID, 0)
	if err != nil {
		t.Fatalf("ListJobEvents: %v", err)
	}
	last := evs[len(evs)-1]
	if last.Type != EventJobNeedsReview {
		t.Fatalf("last event = %s, want %s", last.Type, EventJobNeedsReview)
	}
	if !strings.Contains(last.Detail, `"job_id":"`+final.ID+`"`) || !strings.Contains(last.Detail, `"exit_code":0`) {
		t.Fatalf("needs_review detail = %s", last.Detail)
	}
}

// TestReviewNotAppliedOnFailure: only a NORMAL完成 enters needs_review; a failed job
// keeps its ordinary terminal status (and no review event).
func TestReviewNotAppliedOnFailure(t *testing.T) {
	s := newReviewService(t, t.TempDir(), reviewServiceOpts{successCode: 1})
	final := reviewJob(t, s)

	if final.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", final.Status)
	}
	for _, ty := range eventTypes(t, s, final.ID) {
		if ty == EventJobNeedsReview {
			t.Fatalf("a failed job must not enter needs_review")
		}
	}
}

// TestProjectRequireReview: the project-level require_review default turns the gate
// on for every job of the project (no per-job flag).
func TestProjectRequireReview(t *testing.T) {
	s := newReviewService(t, t.TempDir(), reviewServiceOpts{successCode: 0, requireReview: true})
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "do the thing", Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusNeedsReview {
		t.Fatalf("status = %s, want %s (project require_review)", final.Status, StatusNeedsReview)
	}
}

// TestAcceptJob: a human accept moves needs_review -> done, records the reviewer and
// re-emits the terminal event the parked job never recorded.
func TestAcceptJob(t *testing.T) {
	s := newReviewService(t, t.TempDir(), reviewServiceOpts{successCode: 0})
	final := reviewJob(t, s)
	if final.Status != StatusNeedsReview {
		t.Fatalf("setup: status = %s, want needs_review", final.Status)
	}

	before := time.Now().Unix()
	out, err := s.AcceptJob(final.ID, "alice", "looks good")
	if err != nil {
		t.Fatalf("AcceptJob: %v", err)
	}
	if out.Status != StatusDone {
		t.Fatalf("status = %s, want done", out.Status)
	}
	if out.ReviewedBy != "alice" || out.ReviewNote != "looks good" || out.ReviewedAt < before {
		t.Fatalf("review audit fields = by %q at %d note %q", out.ReviewedBy, out.ReviewedAt, out.ReviewNote)
	}
	if !out.RequireReview {
		t.Fatalf("require_review must survive the review (it is the audit trail)")
	}
	if out.ResumeJobID != "" {
		t.Fatalf("accept must not start a continuation, got %q", out.ResumeJobID)
	}

	// Durable: the store row (not just the response) carries the decision.
	rec, ok, err := s.meta.GetJob(final.ID)
	if err != nil || !ok {
		t.Fatalf("GetJob: ok=%v err=%v", ok, err)
	}
	if rec.Status != StatusDone || rec.ReviewedBy != "alice" || rec.ReviewNote != "looks good" ||
		rec.ReviewedAt != out.ReviewedAt || !rec.RequireReview {
		t.Fatalf("persisted review row = %+v", rec)
	}
	if got, ok := s.Get(final.ID); !ok || got.Status != StatusDone || got.ReviewedBy != "alice" {
		t.Fatalf("Get after accept = %+v ok=%v", got, ok)
	}

	// job.reviewed{accepted} then job.terminal{done}, in that order.
	types := eventTypes(t, s, final.ID)
	if !hasSubsequence(types, []string{EventJobNeedsReview, EventJobReviewed, EventJobTerminal}) {
		t.Fatalf("event order mismatch: %v", types)
	}
	evs, _ := s.ListJobEvents(final.ID, 0)
	last := evs[len(evs)-1]
	if last.Type != EventJobTerminal || !strings.Contains(last.Detail, `"status":"done"`) {
		t.Fatalf("terminal event = %s %s", last.Type, last.Detail)
	}
	var reviewed string
	for _, e := range evs {
		if e.Type == EventJobReviewed {
			reviewed = e.Detail
		}
	}
	if !strings.Contains(reviewed, `"verdict":"accepted"`) || !strings.Contains(reviewed, `"by":"alice"`) {
		t.Fatalf("reviewed detail = %s", reviewed)
	}
}

// TestRejectJob: a reject moves needs_review -> the terminal rejected, and — unlike a
// failure — starts no automatic continuation and no job-level retry.
func TestRejectJob(t *testing.T) {
	autoMax := 2
	s := newReviewService(t, t.TempDir(), reviewServiceOpts{successCode: 0, stderrText: reviewTransientText, autoMax: &autoMax})
	final := reviewJob(t, s)
	if final.Status != StatusNeedsReview {
		t.Fatalf("setup: status = %s, want needs_review", final.Status)
	}

	// Control: the SAME auto-resume configuration really does continue a failed job,
	// so the assertions below are not vacuous.
	control := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "flaky", Runner: "local",
		Prompt: "do the thing", Cwd: ".", TimeoutSec: 30, SessionID: "sess-flaky",
	})
	if control.Status != StatusFailed {
		t.Fatalf("control: status = %s, want failed", control.Status)
	}
	waitAutoResumed(t, s, control.ID, true)

	out, err := s.RejectJob(final.ID, "bob", "not acceptable", false)
	if err != nil {
		t.Fatalf("RejectJob: %v", err)
	}
	if out.Status != StatusRejected {
		t.Fatalf("status = %s, want %s", out.Status, StatusRejected)
	}
	if !IsTerminal(StatusRejected) {
		t.Fatalf("rejected must be a terminal state (retention/workflow depend on it)")
	}
	if out.ReviewedBy != "bob" || out.ReviewNote != "not acceptable" {
		t.Fatalf("review audit fields = by %q note %q", out.ReviewedBy, out.ReviewNote)
	}
	if out.ResumeJobID != "" {
		t.Fatalf("reject without --resume must not start a continuation, got %q", out.ResumeJobID)
	}

	got, ok := s.Get(final.ID)
	if !ok || got.Status != StatusRejected || got.AutoResumedBy != "" {
		t.Fatalf("rejected job = %+v ok=%v, want status rejected and no auto-continuation", got, ok)
	}
	for _, ty := range eventTypes(t, s, final.ID) {
		if ty == "job.auto_resumed" {
			t.Fatalf("a rejected job must not be auto-continued: %v", eventTypes(t, s, final.ID))
		}
	}
	derived, err := s.meta.ListJobs(jobstore.ListQuery{SourceJob: final.ID})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(derived) != 0 {
		t.Fatalf("reject without --resume must create no derived job, got %d", len(derived))
	}

	types := eventTypes(t, s, final.ID)
	if !hasSubsequence(types, []string{EventJobNeedsReview, EventJobReviewed, EventJobTerminal}) {
		t.Fatalf("event order mismatch: %v", types)
	}
	evs, _ := s.ListJobEvents(final.ID, 0)
	last := evs[len(evs)-1]
	if last.Type != EventJobTerminal || !strings.Contains(last.Detail, `"status":"rejected"`) {
		t.Fatalf("terminal event = %s %s", last.Type, last.Detail)
	}
	var reviewed string
	for _, e := range evs {
		if e.Type == EventJobReviewed {
			reviewed = e.Detail
		}
	}
	if !strings.Contains(reviewed, `"verdict":"rejected"`) || !strings.Contains(reviewed, `"by":"bob"`) ||
		!strings.Contains(reviewed, `"note":"not acceptable"`) {
		t.Fatalf("reviewed detail = %s", reviewed)
	}
}

// TestRejectJobWithResume: reject --resume continues the rejected work with the note
// as the prompt and reports the continuation in the outcome/event.
func TestRejectJobWithResume(t *testing.T) {
	s := newReviewService(t, t.TempDir(), reviewServiceOpts{successCode: 0})
	final := reviewJob(t, s)
	if final.Status != StatusNeedsReview {
		t.Fatalf("setup: status = %s, want needs_review", final.Status)
	}

	const note = "fix the failing tests and re-run them"
	out, err := s.RejectJob(final.ID, "bob", note, true)
	if err != nil {
		t.Fatalf("RejectJob: %v", err)
	}
	if out.Status != StatusRejected || out.ResumeJobID == "" {
		t.Fatalf("outcome = status %s resume_job_id %q, want rejected + a continuation", out.Status, out.ResumeJobID)
	}

	cont, ok := s.Get(out.ResumeJobID)
	if !ok {
		t.Fatalf("continuation %s not found", out.ResumeJobID)
	}
	if cont.ResumedFrom != final.ID || cont.SourceJobID != final.ID {
		t.Fatalf("continuation lineage = resumed_from %q source %q, want %s", cont.ResumedFrom, cont.SourceJobID, final.ID)
	}
	if cont.SessionID != "sess-review" {
		t.Fatalf("continuation session = %q, want the source session", cont.SessionID)
	}
	// The note IS the continuation prompt (rendered into the carrier argv; an
	// acp-style continuation would carry it in Prompt verbatim).
	var req JobRequest
	if err := json.Unmarshal([]byte(cont.RequestJSON), &req); err != nil {
		t.Fatalf("unmarshal continuation request: %v", err)
	}
	if req.Prompt != "" && req.Prompt != note {
		t.Fatalf("continuation prompt = %q, want the note", req.Prompt)
	}
	if !strings.Contains(strings.Join(req.Cmd, " "), note) {
		t.Fatalf("continuation argv %q does not carry the note as its prompt", req.Cmd)
	}

	// The reviewed job's event names the continuation it spawned.
	evs, _ := s.ListJobEvents(final.ID, 0)
	var reviewed string
	for _, e := range evs {
		if e.Type == EventJobReviewed {
			reviewed = e.Detail
		}
	}
	if !strings.Contains(reviewed, `"resume_job_id":"`+out.ResumeJobID+`"`) {
		t.Fatalf("reviewed detail = %s, want resume_job_id", reviewed)
	}
}

// TestCancelNeedsReviewRejected: the process already ended, so `job cancel` refuses
// a needs_review job and points at reject instead of silently doing nothing.
func TestCancelNeedsReviewRejected(t *testing.T) {
	s := newReviewService(t, t.TempDir(), reviewServiceOpts{successCode: 0})
	final := reviewJob(t, s)

	err := s.Cancel(final.ID)
	if err == nil || !errors.Is(err, ErrJobNotRunning) {
		t.Fatalf("Cancel = %v, want ErrJobNotRunning", err)
	}
	if !strings.Contains(err.Error(), "reject") {
		t.Fatalf("Cancel error must point at reject: %v", err)
	}
	if got, ok := s.Get(final.ID); !ok || got.Status != StatusNeedsReview {
		t.Fatalf("a refused cancel must not change the job: %+v ok=%v", got, ok)
	}
}

// TestResumeNeedsReviewRejected: resuming a job that is still awaiting review is
// refused (the existing not-terminal rule) rather than silently forking the work.
func TestResumeNeedsReviewRejected(t *testing.T) {
	s := newReviewService(t, t.TempDir(), reviewServiceOpts{successCode: 0})
	final := reviewJob(t, s)

	if _, err := s.ResumeJob(final.ID, "go on", "", "alice"); err == nil || !errors.Is(err, ErrJobNotTerminal) {
		t.Fatalf("ResumeJob = %v, want ErrJobNotTerminal", err)
	}
}

// TestReviewRequiresNeedsReviewState: accept/reject are only legal on the review
// state — a done/failed/running job is refused instead of being rewritten.
func TestReviewRequiresNeedsReviewState(t *testing.T) {
	s := newReviewService(t, t.TempDir(), reviewServiceOpts{successCode: 0})
	plain := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "do the thing", Cwd: ".", TimeoutSec: 30,
	})
	if plain.Status != StatusDone {
		t.Fatalf("setup: status = %s, want done", plain.Status)
	}
	if _, err := s.AcceptJob(plain.ID, "alice", ""); err == nil || !errors.Is(err, ErrJobNotNeedsReview) {
		t.Fatalf("AcceptJob on a done job = %v, want ErrJobNotNeedsReview", err)
	}
	if _, err := s.RejectJob(plain.ID, "alice", "nope", false); err == nil || !errors.Is(err, ErrJobNotNeedsReview) {
		t.Fatalf("RejectJob on a done job = %v, want ErrJobNotNeedsReview", err)
	}
	if _, err := s.AcceptJob("no-such-job", "alice", ""); err == nil || !errors.Is(err, ErrUnknownJob) {
		t.Fatalf("AcceptJob on an unknown job = %v, want ErrUnknownJob", err)
	}
}

// TestRejectRequiresNote: a rejection without a reason is refused (the note is what
// the agent is told to fix on --resume, and what the audit trail needs).
func TestRejectRequiresNote(t *testing.T) {
	s := newReviewService(t, t.TempDir(), reviewServiceOpts{successCode: 0})
	final := reviewJob(t, s)

	if _, err := s.RejectJob(final.ID, "bob", "   ", false); err == nil || !errors.Is(err, ErrReviewNoteRequired) {
		t.Fatalf("RejectJob without a note = %v, want ErrReviewNoteRequired", err)
	}
	if got, ok := s.Get(final.ID); !ok || got.Status != StatusNeedsReview {
		t.Fatalf("a refused reject must not change the job: %+v ok=%v", got, ok)
	}
}

// TestReviewCallerFallsBackToAnonymous: an empty caller (allow_empty_token) is still
// recorded, so every review has an author.
func TestReviewCallerFallsBackToAnonymous(t *testing.T) {
	s := newReviewService(t, t.TempDir(), reviewServiceOpts{successCode: 0})
	final := reviewJob(t, s)

	out, err := s.AcceptJob(final.ID, "", "")
	if err != nil {
		t.Fatalf("AcceptJob: %v", err)
	}
	if out.ReviewedBy != reviewAnonymousCaller {
		t.Fatalf("reviewed_by = %q, want %q", out.ReviewedBy, reviewAnonymousCaller)
	}
}

// TestIsFinishedCoversNeedsReview: the finished-vs-terminal split is the S3 contract
// the SSE/attach/eviction paths depend on.
func TestIsFinishedCoversNeedsReview(t *testing.T) {
	cases := []struct {
		status   string
		terminal bool
		finished bool
	}{
		{StatusQueued, false, false},
		{StatusRunning, false, false},
		{StatusPendingInteraction, false, false},
		{StatusRecovering, false, false},
		{StatusNeedsReview, false, true},
		{StatusDone, true, true},
		{StatusFailed, true, true},
		{StatusCancelled, true, true},
		{StatusTimeout, true, true},
		{StatusRejected, true, true},
	}
	for _, tc := range cases {
		if got := IsTerminal(tc.status); got != tc.terminal {
			t.Errorf("IsTerminal(%s) = %v, want %v", tc.status, got, tc.terminal)
		}
		if got := IsFinished(tc.status); got != tc.finished {
			t.Errorf("IsFinished(%s) = %v, want %v", tc.status, got, tc.finished)
		}
	}
}
