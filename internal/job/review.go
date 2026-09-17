package job

import (
	"errors"
	"fmt"
	"strings"

	"github.com/inhere/gofer/internal/config"
)

// 人工验收 (GATE-01 S3) sentinels. Like the resume/interaction ones they are wrapped
// so the entry layers map each failure to a stable status code with errors.Is.
var (
	// ErrJobNotNeedsReview — accept/reject was asked of a job that is not awaiting
	// review (queued/running/done/failed/...). HTTP: 409.
	ErrJobNotNeedsReview = errors.New("job is not awaiting review")
	// ErrReviewNoteRequired — a reject carried no note. The note IS the reason the
	// agent is told to fix on --resume and the audit trail of the decision, so it is
	// required. HTTP: 400.
	ErrReviewNoteRequired = errors.New("reject requires a note")
	// ErrJobNotRunning — an operation that needs a LIVE job (cancel) was asked of a
	// job whose process already ended but which is not terminal (needs_review: the
	// caller wants reject). HTTP: 409.
	ErrJobNotRunning = errors.New("job is not running")
)

// reviewAnonymousCaller is recorded as reviewed_by when the caller has no identity
// (an allow_empty_token HTTP caller), so every review still has an author.
const reviewAnonymousCaller = "anonymous"

// review verdicts (the job.reviewed event's `verdict` field).
const (
	verdictAccepted = "accepted"
	verdictRejected = "rejected"
)

// ReviewOutcome is the result of a human accept/reject: the job's new snapshot plus,
// for a `reject --resume`, the id of the continuation it started. The embedded
// JobResult keeps the HTTP/MCP body the job's own shape (the detail view is flattened)
// with one extra field, so a caller that ignores review still parses it as a job.
type ReviewOutcome struct {
	JobResult
	// ResumeJobID is the continuation started by reject+resume ("" = none).
	ResumeJobID string `json:"resume_job_id,omitempty"`
}

// reviewRequested resolves whether a submit asks for人工验收: an explicit flag, or the
// project's require_review default. A workflow step that states `review: true|false`
// is FINAL (ReviewFixed) and overrides the default in BOTH directions — a bool alone
// cannot tell "explicitly off" from "unset". Mirrors worktreeRequested.
func reviewRequested(cfg *config.Config, req *JobRequest) bool {
	if req.ReviewFixed {
		return req.Review
	}
	return req.Review || cfg.Projects[req.ProjectKey].RequireReview
}

// AcceptJob records a human's接受 of a needs_review job: needs_review -> done, with
// the reviewer/note in the audit fields, the job.reviewed{accepted} + job.terminal{done}
// events, and the same post-finish hook a normally-completed job runs (a workflow step's
// chain advances now). by is the authenticated caller; an empty id is recorded as
// "anonymous". Only a needs_review job may be accepted (ErrJobNotNeedsReview otherwise).
func (s *Service) AcceptJob(jobID, by, note string) (ReviewOutcome, error) {
	return s.reviewJob(jobID, by, note, verdictAccepted, false)
}

// RejectJob records a human's拒绝 of a needs_review job: needs_review -> the terminal
// rejected (a workflow aggregates it as a failure; retention collects it; nothing
// retries or auto-continues it). note is REQUIRED — it is the reason recorded in the
// audit trail and, with resume, the continuation's prompt. resume starts that
// continuation (ResumeJob with the note as its prompt) and reports it in
// ReviewOutcome.ResumeJobID + the job.reviewed detail; when the continuation cannot be
// started the rejection still stands and the returned error says why.
func (s *Service) RejectJob(jobID, by, note string, resume bool) (ReviewOutcome, error) {
	if strings.TrimSpace(note) == "" {
		return ReviewOutcome{}, fmt.Errorf("%w", ErrReviewNoteRequired)
	}
	return s.reviewJob(jobID, by, note, verdictRejected, resume)
}

// reviewJob is the shared transition behind accept/reject: it validates the job is
// awaiting review, persists the decision, records the events (reviewed then terminal)
// and runs the post-finish hook. It works from the store-backed snapshot because a
// needs_review job has already been evicted from the in-memory map (its process ended);
// an entry can still exist when the needs_review persist itself failed, so it is kept
// consistent too.
func (s *Service) reviewJob(jobID, by, note, verdict string, resume bool) (ReviewOutcome, error) {
	if strings.TrimSpace(by) == "" {
		by = reviewAnonymousCaller
	}
	snap, ok := s.Get(jobID)
	if !ok {
		return ReviewOutcome{}, fmt.Errorf("%w: %q", ErrUnknownJob, jobID)
	}
	if snap.Status != StatusNeedsReview {
		return ReviewOutcome{}, fmt.Errorf("%w: job %q is %s", ErrJobNotNeedsReview, jobID, snap.Status)
	}

	newStatus := StatusDone
	if verdict == verdictRejected {
		newStatus = StatusRejected
	}
	now := s.nowFn().Unix()
	snap.Status = newStatus
	snap.ReviewedBy = by
	snap.ReviewedAt = now
	snap.ReviewNote = note

	if e := s.entry(jobID); e != nil {
		e.mu.Lock()
		e.result.Status = newStatus
		e.result.ReviewedBy = by
		e.result.ReviewedAt = now
		e.result.ReviewNote = note
		e.mu.Unlock()
	}
	if err := s.persist(snap); err != nil {
		return ReviewOutcome{}, fmt.Errorf("persist review of job %q: %w", jobID, err)
	}

	out := ReviewOutcome{JobResult: snap}
	detail := map[string]any{"verdict": verdict, "by": by}
	if note != "" {
		detail["note"] = note
	}
	// The continuation is decided by the rejection alone: the state transition above is
	// already durable, so a failed resume must not undo it (nor hide it) — the error is
	// returned on top of the rejected outcome instead.
	var resumeErr error
	if resume {
		cont, rerr := s.ResumeJob(jobID, note, "", by)
		if rerr != nil {
			resumeErr = rerr
		} else {
			out.ResumeJobID = cont.ID
			detail["resume_job_id"] = cont.ID
		}
	}
	s.recordEvent(jobID, EventJobReviewed, detail)
	s.recordEvent(jobID, EventJobTerminal, map[string]any{"status": newStatus, "exit_code": snap.ExitCode})
	// Post-finish hook (finish's tail): a reviewed step-job unblocks/advances its
	// workflow now — an accepted step is a done step, a rejected one is a failed step.
	// Never blocking, and a no-op for a non-workflow job.
	if s.wf != nil && snap.WorkflowID != "" {
		go s.wf.Advance(snap.WorkflowID)
	}
	if resumeErr != nil {
		return out, fmt.Errorf("job %q was rejected but its continuation could not be started: %w", jobID, resumeErr)
	}
	return out, nil
}
