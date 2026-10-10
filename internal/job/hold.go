package job

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"strings"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/secret"
)

// Hold-for-approval jobs (gofer-9b1b, design 2026-10-10-hold-for-approval-jobs).
//
// A `--hold` submit runs every admission step and then, instead of starting, parks the
// job in awaiting_approval: one database row, no in-memory entry, no concurrency slot,
// no directory lock, no worker dispatch, no job credential. A human then
//
//   - approves it (ApproveJob): the row moves to queued and the ORIGINAL request is
//     submitted again under the same id — every resolution step runs once more, and
//     the result must describe the same work the human approved (the digest check);
//   - rejects it (RejectJob dispatches here): cancelled, nothing ran;
//   - or lets it expire (SweepExpiredHolds): cancelled, nothing ran.
//
// Every transition is one compare-and-set on the row (jobstore.DecideHold), so an
// approve racing a reject, a cancel or the expiry sweep has exactly one winner.

// Hold lifecycle events. The `hold_` prefix keeps them apart from the review verdicts
// (`rejected` status, job.reviewed{verdict:rejected}), which mean something else.
const (
	// EventJobAwaitingApproval is a held job parking for a decision:
	// {reason, timeout_sec, expires_at, origin}.
	EventJobAwaitingApproval = "job.awaiting_approval"
	// EventJobHoldApproved is a human approving it: {by, note?}. The job's own
	// job.running / job.terminal follow as for any job.
	EventJobHoldApproved = "job.hold_approved"
	// EventJobHoldRejected is a human rejecting it: {by, note?}; job.terminal{cancelled}
	// follows.
	EventJobHoldRejected = "job.hold_rejected"
	// EventJobHoldExpired is the expiry sweep cancelling it: {expires_at};
	// job.terminal{cancelled} follows.
	EventJobHoldExpired = "job.hold_expired"
)

// Hold decisions (HoldState.Decision).
const (
	HoldDecisionApproved  = "approved"
	HoldDecisionRejected  = "rejected"
	HoldDecisionExpired   = "expired"
	HoldDecisionCancelled = "cancelled"
)

// holdSystemActor is recorded as decided_by when no human decided (the expiry sweep).
const holdSystemActor = "system"

// holdPromptPreviewRunes caps the prompt text stored for the approver; the full
// prompt stays in request_json.
const holdPromptPreviewRunes = 4000

var (
	// ErrJobNotAwaitingApproval — approve/reject-as-hold of a job that is not (or no
	// longer) awaiting approval, including the loser of a decision race. HTTP: 409.
	ErrJobNotAwaitingApproval = errors.New("job is not awaiting approval")
	// ErrHoldDrift — the approved request no longer resolves to the work the human
	// saw (config, role, template or rules changed while it waited). The job is
	// failed rather than run. HTTP: 409.
	ErrHoldDrift = errors.New("the request changed while awaiting approval; submit it again")
	// ErrHoldResume — `reject --resume` of a held job: nothing ran, so there is
	// nothing to continue. HTTP: 400.
	ErrHoldResume = fmt.Errorf("%w: a job awaiting approval never ran, so it cannot be resumed; reject it without --resume", ErrInvalidRequest)
)

// HoldState is the public hold record of a job (JobResult.Hold, jobs.hold_json).
type HoldState struct {
	// Reason is the submitter's why (JobRequest.HoldReason).
	Reason string `json:"reason,omitempty"`
	// TimeoutSec / ExpiresAt: the resolved wait and the deadline fixed at submit.
	TimeoutSec int   `json:"timeout_sec"`
	ExpiresAt  int64 `json:"expires_at"`
	// Origin says who submitted it: job:<id> (a job credential), agent-session:<id>
	// (an agent session), or the submit channel (cli / web / mcp / ...).
	Origin string `json:"origin,omitempty"`
	// Command is an exec job's argv; PromptPreview the head of an agent job's prompt
	// (the caller's text, without the injected rules). Both secret-redacted.
	Command       []string `json:"command,omitempty"`
	PromptPreview string   `json:"prompt_preview,omitempty"`
	// Decision / DecidedBy / DecidedAt / Note record the outcome once there is one.
	Decision  string `json:"decision,omitempty"`
	DecidedBy string `json:"decided_by,omitempty"`
	DecidedAt int64  `json:"decided_at,omitempty"`
	Note      string `json:"note,omitempty"`
}

// holdSecret is the server-only half of the hold record: never part of an API body.
type holdSecret struct {
	// Digest is the sha256 of what the approver was shown (holdDigest).
	Digest string `json:"digest,omitempty"`
	// Carry holds the request's internal markers (json:"-" fields set by server-side
	// producers such as resume / rebuild / retry) that request_json cannot carry, so the
	// approval re-entry runs the request exactly as it was submitted — also after a
	// restart. Dropped once the job was approved.
	Carry *heldInternals `json:"carry,omitempty"`
}

// holdRecord is the stored form of jobs.hold_json: the public state plus the secret.
type holdRecord struct {
	HoldState
	holdSecret
}

// heldInternals are the internal (json:"-") request fields a server-side producer may
// have set on a held request. The access-control markers among them (e.g.
// ResumeSourceAgent) are restored only from this server-written record, never from a
// client body.
type heldInternals struct {
	BudgetFixed       bool           `json:"budget_fixed,omitempty"`
	ReviewFixed       bool           `json:"review_fixed,omitempty"`
	SkillsResolved    bool           `json:"skills_resolved,omitempty"`
	RulesResolved     bool           `json:"rules_resolved,omitempty"`
	TodoForeign       bool           `json:"todo_foreign,omitempty"`
	SourceJobID       string         `json:"source_job_id,omitempty"`
	Attempt           int            `json:"attempt,omitempty"`
	ResumeSourceAgent string         `json:"resume_source_agent,omitempty"`
	AutoResumeAttempt int            `json:"auto_resume_attempt,omitempty"`
	FellBackFrom      string         `json:"fell_back_from,omitempty"`
	Fallback          *FallbackState `json:"fallback,omitempty"`
	RequestedAgent    string         `json:"requested_agent,omitempty"`
	EnvDenyExtra      []string       `json:"env_deny_extra,omitempty"`
}

func captureHeldInternals(r *JobRequest) *heldInternals {
	h := &heldInternals{
		BudgetFixed: r.BudgetFixed, ReviewFixed: r.ReviewFixed,
		SkillsResolved: r.SkillsResolved, RulesResolved: r.RulesResolved,
		TodoForeign: r.TodoForeign, SourceJobID: r.SourceJobID, Attempt: r.Attempt,
		ResumeSourceAgent: r.ResumeSourceAgent, AutoResumeAttempt: r.AutoResumeAttempt,
		FellBackFrom: r.FellBackFrom, RequestedAgent: r.RequestedAgent,
	}
	if len(r.EnvDenyExtra) > 0 {
		h.EnvDenyExtra = append([]string(nil), r.EnvDenyExtra...)
	}
	if r.Fallback != nil {
		f := *r.Fallback
		f.Candidates = append([]string(nil), f.Candidates...)
		h.Fallback = &f
	}
	if reflect.DeepEqual(*h, heldInternals{}) {
		return nil // a plain client request: nothing to carry
	}
	return h
}

func (h *heldInternals) restore(r *JobRequest) {
	if h == nil {
		return
	}
	r.BudgetFixed, r.ReviewFixed = h.BudgetFixed, h.ReviewFixed
	r.SkillsResolved, r.RulesResolved = h.SkillsResolved, h.RulesResolved
	r.TodoForeign, r.SourceJobID, r.Attempt = h.TodoForeign, h.SourceJobID, h.Attempt
	r.ResumeSourceAgent, r.AutoResumeAttempt = h.ResumeSourceAgent, h.AutoResumeAttempt
	r.FellBackFrom, r.Fallback, r.RequestedAgent = h.FellBackFrom, h.Fallback, h.RequestedAgent
	r.EnvDenyExtra = h.EnvDenyExtra
}

// holdPending reports whether this submit must park the job (a held request that is
// not the approval re-entry).
func (r *JobRequest) holdPending() bool { return r.Hold && r.heldJobID == "" }

// marshalHold serialises a job's hold record for jobs.hold_json ("" = never held).
func marshalHold(state *HoldState, sec *holdSecret) string {
	if state == nil {
		return ""
	}
	rec := holdRecord{HoldState: *state}
	if sec != nil {
		rec.holdSecret = *sec
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return ""
	}
	return string(b)
}

// unmarshalHold reads jobs.hold_json back. "" or a malformed blob reads as "never
// held" rather than failing the read of the whole row.
func unmarshalHold(raw string) (*HoldState, *holdSecret) {
	if raw == "" {
		return nil, nil
	}
	var rec holdRecord
	if json.Unmarshal([]byte(raw), &rec) != nil {
		return nil, nil
	}
	sec := rec.holdSecret
	return &rec.HoldState, &sec
}

// holdExpiresAt is the jobs.hold_expires_at value of a result (0 = never held).
func holdExpiresAt(r JobResult) int64 {
	if r.Hold == nil {
		return 0
	}
	return r.Hold.ExpiresAt
}

// checkHoldCombination refuses the request shapes v1 cannot hold (design §组合): a
// continuous session and an interactive terminal need a human at the keyboard anyway,
// and a workflow step is started by the chain engine, which has no approval step.
func checkHoldCombination(req JobRequest) error {
	switch {
	case req.Session:
		return fmt.Errorf("%w: --hold cannot be combined with a continuous session (--session): the session's later turns would run unapproved", ErrInvalidRequest)
	case req.Interactive:
		return fmt.Errorf("%w: --hold cannot be combined with --interactive: an interactive job already runs under a human at the terminal", ErrInvalidRequest)
	case req.WorkflowID != "":
		return fmt.Errorf("%w: --hold is not supported for workflow steps: the chain engine cannot wait for an approval", ErrInvalidRequest)
	case req.Steward || req.LeaderOfPlan != "" || req.Messenger != nil:
		return fmt.Errorf("%w: --hold is not supported for internal jobs (steward / leader / messenger)", ErrInvalidRequest)
	}
	return nil
}

// holdDigest fingerprints what the approver is shown and approves: where the job
// runs, what it runs and with which environment. It is taken at the admission point of
// the held submit and again at the same point of the approval re-entry; a mismatch
// means the work changed while it waited (ErrHoldDrift). The injected rules section is
// left out — it is deployment discipline the approver does not see.
func holdDigest(req *JobRequest) string {
	payload := struct {
		Project   string            `json:"project"`
		Agent     string            `json:"agent"`
		Runner    string            `json:"runner"`
		Cwd       string            `json:"cwd"`
		Cmd       []string          `json:"cmd"`
		Prompt    string            `json:"prompt"`
		AgentArgs []string          `json:"agent_args"`
		ReadOnly  bool              `json:"read_only"`
		Worktree  bool              `json:"worktree"`
		Env       map[string]string `json:"env"` // encoding/json sorts map keys
	}{
		Project: req.ProjectKey, Agent: req.Agent, Runner: req.Runner, Cwd: req.Cwd,
		Cmd: req.Cmd, Prompt: stripRulesSection(req.Prompt), AgentArgs: req.AgentArgs,
		ReadOnly: req.ReadOnly, Worktree: req.Worktree, Env: req.Env,
	}
	b, _ := json.Marshal(payload)
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// holdOrigin names who submitted a held job: a job credential (its caller id IS a job
// id), an agent session, or the submit channel.
func (s *Service) holdOrigin(req *JobRequest) string {
	if req.CallerID != "" {
		if _, ok, err := s.meta.GetJob(req.CallerID); err == nil && ok {
			return "job:" + req.CallerID
		}
	}
	if req.SourceSessionID != "" {
		return "agent-session:" + req.SourceSessionID
	}
	return req.Channel
}

// holdPreview fills the approver-facing view of the request: an exec job's argv, or
// the head of an agent job's prompt. Both are secret-redacted — the job detail is a
// wider audience than request_json.
func holdPreview(h *HoldState, req *JobRequest) {
	if len(req.Cmd) > 0 {
		h.Command = make([]string, len(req.Cmd))
		for i, a := range req.Cmd {
			h.Command[i], _ = secret.RedactString(a)
		}
		return
	}
	preview, _ := secret.RedactString(stripRulesSection(req.Prompt))
	h.PromptPreview = firstRunes(preview, holdPromptPreviewRunes)
}

// holdJob is the held branch of submitAdmitted: the request passed every admission
// step, so the job becomes a fact — a result dir and an awaiting_approval row whose
// request_json is the ORIGINAL request (rawJSON, re-submitted on approval) — and the
// submit returns without starting anything.
func (s *Service) holdJob(cfg *config.Config, proj config.ProjectConfig, req *JobRequest, workDir string, rawJSON []byte, carry *heldInternals) (JobResult, error) {
	timeoutSec, err := cfg.EffectiveHoldTimeoutSec(req.HoldTimeoutSec)
	if err != nil {
		return JobResult{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	// The stored request keeps the decided title, so the row reads the same while it
	// waits as after it ran (fromRecord takes the title from request_json).
	var raw JobRequest
	if err := json.Unmarshal(rawJSON, &raw); err != nil {
		return JobResult{}, fmt.Errorf("hold: decode request: %w", err)
	}
	raw.Title = req.Title
	requestJSON, err := json.Marshal(raw)
	if err != nil {
		return JobResult{}, fmt.Errorf("hold: marshal request: %w", err)
	}

	base, err := project.ResultBaseDir(cfg, req.ProjectKey, proj)
	if err != nil {
		return JobResult{}, err
	}
	st := s.newStore(base)
	jobID, err := s.createJobDir(st)
	if err != nil {
		return JobResult{}, err
	}
	resultDir := st.Dir(jobID)

	now := s.nowFn().Unix()
	hold := &HoldState{
		Reason:     strings.TrimSpace(req.HoldReason),
		TimeoutSec: timeoutSec,
		ExpiresAt:  now + int64(timeoutSec),
		Origin:     s.holdOrigin(req),
	}
	holdPreview(hold, req)
	snap := JobResult{
		ID:              jobID,
		ProjectKey:      req.ProjectKey,
		Agent:           req.Agent,
		Runner:          req.Runner,
		Model:           req.Model,
		Acceptance:      req.Acceptance,
		Scope:           req.Scope,
		Budget:          req.Budget,
		ReadOnly:        req.ReadOnly,
		RequireReview:   req.Review,
		Title:           req.Title,
		WorkerID:        req.WorkerID,
		Status:          StatusAwaitingApproval,
		Cwd:             workDir,
		ResultDir:       resultDir,
		StartedAt:       now,
		RequestJSON:     string(requestJSON),
		CallerID:        req.CallerID,
		SourceSessionID: req.SourceSessionID,
		RequestID:       req.RequestID,
		Tags:            req.Tags,
		Channel:         req.Channel,
		Client:          req.Client,
		OriginAgent:     req.OriginAgent,
		EscalateTo:      req.EscalateTo,
		Role:            req.Role,
		PlanID:          req.PlanID,
		TodoID:          req.TodoID,
		IssueID:         req.IssueID,
		TrackerID:       req.TrackerID,
		TodoForeign:     req.TodoForeign,
		SourceJobID:     req.SourceJobID,
		ResumedFrom:     req.ResumedFrom,
		RequestedAgent:  req.RequestedAgent,
		FellBackFrom:    req.FellBackFrom,
		Attempt:         req.Attempt,
		// The run's deadline is resolved when it runs; until then only the ask is known.
		RequestedTimeoutSec: req.TimeoutSec,
		Hold:                hold,
		holdSecret:          &holdSecret{Digest: holdDigest(req), Carry: carry},
		KnowledgeCapture:    req.KnowledgeCapture,
	}
	snap.AutoResumeAttempt = req.AutoResumeAttempt

	// The source session watches the held job like any other: its eventual terminal
	// state (ran, rejected or expired) reaches the session through the same channel.
	if req.SourceSessionID != "" {
		if _, err := s.meta.AddSessionJobWatch(req.SourceSessionID, jobID); err != nil {
			_ = os.RemoveAll(resultDir)
			return JobResult{}, fmt.Errorf("register source session watch: %w", err)
		}
	}
	if perr := s.persist(snap); perr != nil {
		if req.SourceSessionID != "" {
			_, _ = s.meta.RemoveSessionJobWatch(req.SourceSessionID, jobID)
		}
		_ = os.RemoveAll(resultDir)
		// C5: a concurrent held submit with the same request_id won the unique index.
		if req.RequestID != "" && errors.Is(perr, jobstore.ErrRequestIDConflict) {
			if rec, ok, gerr := s.meta.GetJobByRequestID(req.RequestID); gerr != nil {
				return JobResult{}, gerr
			} else if ok {
				return s.reuseRequestJob(rec, *req)
			}
		}
		return JobResult{}, fmt.Errorf("persist held job: %w", perr)
	}

	s.recordEvent(jobID, EventJobSubmitted, map[string]any{
		"project":   req.ProjectKey,
		"agent":     req.Agent,
		"runner":    req.Runner,
		"caller_id": req.CallerID,
		"tags":      req.Tags,
	})
	s.recordEvent(jobID, EventJobAwaitingApproval, map[string]any{
		"reason":      hold.Reason,
		"timeout_sec": hold.TimeoutSec,
		"expires_at":  hold.ExpiresAt,
		"origin":      hold.Origin,
	})
	if req.TodoID != "" && !req.TodoForeign {
		s.linkTodoSubmit(req.TodoID, jobID)
	}
	if s.metrics != nil {
		s.metrics.JobSubmitted(req.CallerID, req.ProjectKey, req.Agent, req.Runner)
	}
	return snap, nil
}

// heldRecord loads a job's row and its hold record, refusing a job that is not
// awaiting approval.
func (s *Service) heldRecord(jobID string) (jobstore.JobRecord, holdRecord, error) {
	rec, ok, err := s.meta.GetJob(jobID)
	if err != nil {
		return jobstore.JobRecord{}, holdRecord{}, err
	}
	if !ok {
		return jobstore.JobRecord{}, holdRecord{}, fmt.Errorf("%w: %q", ErrUnknownJob, jobID)
	}
	if rec.Status != StatusAwaitingApproval {
		return jobstore.JobRecord{}, holdRecord{}, fmt.Errorf("%w: job %q is %s", ErrJobNotAwaitingApproval, jobID, rec.Status)
	}
	var hr holdRecord
	if state, sec := unmarshalHold(rec.HoldJSON); state != nil {
		hr.HoldState = *state
		hr.holdSecret = *sec
	}
	return rec, hr, nil
}

// ApproveJob is a human's approval of a held job: awaiting_approval -> queued, the
// job.hold_approved event, then the ORIGINAL request is submitted again under the
// same id and runs like any job (credential, worktree, worker dispatch and all happen
// now, not at hold time). by is the authenticated caller ("" = anonymous).
//
// It takes an upgrade-admission permit first, so a draining server refuses the
// approval (ErrUpgradeDraining) and the job keeps waiting. A job that is not awaiting
// approval — including one somebody else decided a moment earlier — is
// ErrJobNotAwaitingApproval. When the approved request cannot be started (it no longer
// validates, or ErrHoldDrift) the job is failed with the reason and the error is
// returned.
func (s *Service) ApproveJob(jobID, by, note string) (JobResult, error) {
	by = reviewActor(by)
	permit, err := s.BeginUpgradeWork()
	if err != nil {
		return JobResult{}, err
	}
	defer permit.Release()

	rec, hr, err := s.heldRecord(jobID)
	if err != nil {
		return JobResult{}, err
	}
	var req JobRequest
	if err := json.Unmarshal([]byte(rec.RequestJSON), &req); err != nil {
		return JobResult{}, fmt.Errorf("decode request_json of %q: %w", jobID, err)
	}
	now := s.nowFn().Unix()
	hr.Decision, hr.DecidedBy, hr.DecidedAt, hr.Note = HoldDecisionApproved, by, now, strings.TrimSpace(note)
	carry := hr.Carry
	hr.Carry = nil // consumed by this approval; the digest stays as the audit
	won, err := s.meta.DecideHold(jobstore.HoldDecision{
		ID: jobID, To: StatusQueued, HoldJSON: marshalHold(&hr.HoldState, &hr.holdSecret), At: now,
	})
	if err != nil {
		return JobResult{}, err
	}
	if !won {
		return JobResult{}, fmt.Errorf("%w: job %q was decided by someone else", ErrJobNotAwaitingApproval, jobID)
	}
	s.signalJob(jobID)
	detail := map[string]any{"by": by}
	if hr.Note != "" {
		detail["note"] = hr.Note
	}
	s.recordEvent(jobID, EventJobHoldApproved, detail)

	carry.restore(&req)
	req.heldJobID = jobID
	req.held = &hr
	res, serr := s.submitAdmitted(req)
	if serr != nil {
		s.failApprovedHold(jobID, hr, serr)
		return JobResult{}, fmt.Errorf("job %q was approved but could not start: %w", jobID, serr)
	}
	return res, nil
}

// failApprovedHold ends an approved job whose re-submit was refused: its row is still
// the queued one ApproveJob wrote (nothing else owns it), so it goes queued -> failed
// with the reason and the usual terminal tail.
func (s *Service) failApprovedHold(jobID string, hr holdRecord, cause error) {
	now := s.nowFn().Unix()
	msg := "approved but could not start: " + cause.Error()
	won, err := s.meta.DecideHold(jobstore.HoldDecision{
		ID: jobID, From: StatusQueued, To: StatusFailed,
		HoldJSON: marshalHold(&hr.HoldState, &hr.holdSecret), Error: msg, At: now,
	})
	if err != nil || !won {
		slog.Warn("hold: fail approved job", "job_id", jobID, "won", won, "err", err)
		return
	}
	s.signalJob(jobID)
	s.recordEvent(jobID, EventJobTerminal, map[string]any{"status": StatusFailed, "exit_code": 0, "error": msg})
	if snap, ok := s.Get(jobID); ok {
		s.finishUnrun(snap)
	}
}

// rejectHold is RejectJob for a held job: awaiting_approval -> cancelled with
// "hold rejected by <who>[: <note>]". The note is optional here (the job never ran, so
// there is no delivery to explain a rejection of). ReviewOutcome keeps RejectJob's
// shape; ResumeJobID is always empty.
func (s *Service) rejectHold(jobID, by, note string) (ReviewOutcome, error) {
	by = reviewActor(by)
	note = strings.TrimSpace(note)
	msg := "hold rejected by " + by
	if note != "" {
		msg += ": " + note
	}
	detail := map[string]any{"by": by}
	if note != "" {
		detail["note"] = note
	}
	snap, err := s.decideUnrun(jobID, HoldDecisionRejected, by, note, msg, EventJobHoldRejected, detail)
	if err != nil {
		return ReviewOutcome{}, err
	}
	return ReviewOutcome{JobResult: snap}, nil
}

// ExpireHold cancels a held job whose deadline passed (the expiry sweep's per-job
// step): awaiting_approval -> cancelled with "hold expired" and job.hold_expired. A
// job somebody decided first is ErrJobNotAwaitingApproval.
func (s *Service) ExpireHold(jobID string) (JobResult, error) {
	rec, _, err := s.heldRecord(jobID)
	if err != nil {
		return JobResult{}, err
	}
	return s.decideUnrun(jobID, HoldDecisionExpired, holdSystemActor, "", "hold expired",
		EventJobHoldExpired, map[string]any{"expires_at": rec.HoldExpiresAt})
}

// cancelHold is Cancel for a held job: the submitter withdrawing it.
func (s *Service) cancelHold(jobID string) error {
	_, err := s.decideUnrun(jobID, HoldDecisionCancelled, "", "", "cancelled while awaiting approval",
		EventJobCancelled, map[string]any{"was_terminal": false})
	return err
}

// decideUnrun ends a held job WITHOUT running it: one CAS awaiting_approval ->
// cancelled carrying the decision, the decision's own event, job.terminal{cancelled},
// then the terminal tail (finishUnrun).
func (s *Service) decideUnrun(jobID, decision, by, note, errMsg, eventType string, detail map[string]any) (JobResult, error) {
	_, hr, err := s.heldRecord(jobID)
	if err != nil {
		return JobResult{}, err
	}
	now := s.nowFn().Unix()
	hr.Decision, hr.DecidedBy, hr.DecidedAt, hr.Note = decision, by, now, note
	hr.Carry = nil
	won, err := s.meta.DecideHold(jobstore.HoldDecision{
		ID: jobID, To: StatusCancelled, HoldJSON: marshalHold(&hr.HoldState, &hr.holdSecret), Error: errMsg, At: now,
	})
	if err != nil {
		return JobResult{}, err
	}
	if !won {
		return JobResult{}, fmt.Errorf("%w: job %q was decided by someone else", ErrJobNotAwaitingApproval, jobID)
	}
	s.signalJob(jobID)
	s.recordEvent(jobID, eventType, detail)
	s.recordEvent(jobID, EventJobTerminal, map[string]any{"status": StatusCancelled, "exit_code": 0, "error": errMsg})
	snap, ok := s.Get(jobID)
	if !ok {
		return JobResult{}, fmt.Errorf("%w: %q", ErrUnknownJob, jobID)
	}
	s.finishUnrun(snap)
	return snap, nil
}

// finishUnrun is the terminal tail of a job that ended without ever running (a held
// job rejected / expired / cancelled, or one whose approved re-submit was refused) —
// the subset of finish's tail that applies, as in review.go: the checklist item, the
// plan chain, the terminal hooks and a workflow advance. Nothing ran, so there is no
// credential to revoke, no output to capture and no retry or continuation to start.
func (s *Service) finishUnrun(snap JobResult) {
	s.bg.add()
	defer s.bg.done()
	if s.metrics != nil {
		dur := float64(snap.EndedAt - snap.StartedAt)
		if dur < 0 {
			dur = 0
		}
		s.metrics.JobTerminal(snap.Status, snap.CallerID, snap.ProjectKey, snap.Agent, snap.Runner, dur)
	}
	s.linkTodoOutcome(snap)
	s.maybeBlockPlan(snap)
	s.notifyTerminalHooks(snap)
	if s.wf != nil && snap.WorkflowID != "" {
		wfID := snap.WorkflowID
		s.goBG(func() { s.wf.Advance(wfID) })
	}
}

// holdSweepBatch bounds one expiry pass; a backlog drains over consecutive ticks.
const holdSweepBatch = 50

// SweepExpiredHolds cancels every held job whose deadline is at or before now
// (unix seconds) and reports how many it expired. A job decided between the scan and
// its CAS is skipped silently — that decision won. Called by serve once at startup
// and then periodically, so holds that expired while the server was down end on boot.
func (s *Service) SweepExpiredHolds(now int64) (int, error) {
	due, err := s.meta.ListDueHolds(now, holdSweepBatch)
	if err != nil {
		return 0, err
	}
	expired := 0
	for _, rec := range due {
		if _, err := s.ExpireHold(rec.ID); err != nil {
			if !errors.Is(err, ErrJobNotAwaitingApproval) {
				slog.Warn("hold: expire", "job_id", rec.ID, "err", err)
			}
			continue
		}
		expired++
	}
	return expired, nil
}

// reviewActor is the recorded author of a human decision: an empty caller id (an
// allow_empty_token HTTP caller) is "anonymous", so every decision has one.
func reviewActor(by string) string {
	if strings.TrimSpace(by) == "" {
		return reviewAnonymousCaller
	}
	return by
}

// heldUnrun reports whether a job was held and ended without its approved run (its
// request_json is then the request as received, not the resolved one).
func heldUnrun(r JobResult) bool {
	return r.Hold != nil && r.Hold.Decision != HoldDecisionApproved
}

// holdFromRequest reads the hold fields of a stored request, so a continuation built
// field by field (ResumeJob) inherits the hold explicitly: an approval covers one run.
func holdFromRequest(requestJSON string) (hold bool, reason string, timeoutSec int) {
	if requestJSON == "" {
		return false, "", 0
	}
	var h struct {
		Hold           bool   `json:"hold"`
		HoldReason     string `json:"hold_reason"`
		HoldTimeoutSec int    `json:"hold_timeout_sec"`
	}
	if json.Unmarshal([]byte(requestJSON), &h) != nil {
		return false, "", 0
	}
	return h.Hold, h.HoldReason, h.HoldTimeoutSec
}
