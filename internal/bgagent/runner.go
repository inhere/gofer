package bgagent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/runner"
)

// Hard-stop reasons (HardStopError.Reason, the audit detail).
const (
	StopDailyLimit  = "daily_limit"
	StopDailyCost   = "daily_cost_budget"
	StopDailyTokens = "daily_token_budget"
	StopTimeout     = "timeout"
)

// HardStopError says a hard stop refused or ended a run: the daily run limit or a budget
// was reached before the run, or the run hit its timeout. It is never retried by the
// runner: a protection that trips must stay tripped until the day rolls over or the
// operator changes the limit.
type HardStopError struct {
	Kind   string
	Reason string
	// Limit / Used are the figure that tripped (runs, USD, tokens or seconds).
	Limit float64
	Used  float64
}

func (e *HardStopError) Error() string {
	return fmt.Sprintf("background agent %s stopped: %s (limit %g, used %g)", e.Kind, e.Reason, e.Limit, e.Used)
}

// Limits are the cost protections of one kind; a zero field is off.
type Limits struct {
	// DailyRuns caps the runs that started today (per kind, UTC day by default).
	DailyRuns int
	// DailyCostUSD / DailyTokens cap today's reported usage (per kind). Runs that
	// reported no usage add nothing: the budget is checked against what is known.
	DailyCostUSD float64
	DailyTokens  int64
	// Timeout bounds one run (0 = no bound).
	Timeout time.Duration
}

// ExecRequest is what an adapter's Executor is asked to run.
type ExecRequest struct {
	Key
	Mode   Mode
	Prompt string
	Title  string
	Args   []string
	// Origin is the ceiling owner the run is submitted under ("" = none).
	Origin  string
	Timeout time.Duration
	// SessionID / LastJobID are the recorded session a resume or clear continues.
	SessionID string
	LastJobID string
}

// ExecResult is what one turn produced.
type ExecResult struct {
	JobID     string
	SessionID string
	Output    []byte
	Usage     *runner.Usage
}

// Executor is the seam to whatever actually runs a turn (the entry layer adapts the job
// service). It must not be handed anything but what ExecRequest carries.
type Executor interface {
	// Caps says what the agent can do with a recorded session.
	Caps(agent string) Caps
	// Execute runs one turn in the requested mode. For ModeResume / ModeClearContext it
	// returns an error wrapping ErrSessionLost or ErrClearUnsupported when the recorded
	// session cannot be used, which makes the runner fall back to a fresh session.
	Execute(ctx context.Context, r ExecRequest) (ExecResult, error)
}

var (
	// ErrSessionLost: the recorded session cannot be continued (deleted, the agent refused).
	ErrSessionLost = errors.New("background agent session cannot be continued")
	// ErrClearUnsupported: the agent does not support clearing the context.
	ErrClearUnsupported = errors.New("background agent cannot clear its context")
)

// Options configures a Runner.
type Options struct {
	// Now is the clock (nil = time.Now).
	Now func() time.Time
	// StartOfDay returns the start (unix seconds) of the day now belongs to; nil = the UTC day.
	StartOfDay func(now time.Time) int64
}

// Runner is the common background-agent runner: session book, rotation decisions, hard
// stops, run log and audit events. Its methods are safe for concurrent use; callers
// serialise runs of one session themselves (the kinds already claim their sessions).
type Runner struct {
	store *jobstore.Store
	now   func() time.Time
	day   func(time.Time) int64
}

// New builds a Runner over the store.
func New(store *jobstore.Store, opt Options) *Runner {
	r := &Runner{store: store, now: opt.Now, day: opt.StartOfDay}
	if r.now == nil {
		r.now = time.Now
	}
	if r.day == nil {
		r.day = func(t time.Time) int64 {
			u := t.UTC()
			return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC).Unix()
		}
	}
	return r
}

// Plan is the decision for one run plus what Commit / Fail need to record it.
type Plan struct {
	Key
	Decision Decision
	Started  int64
	policy   Policy
	rec      *jobstore.BackgroundSession
}

// Plan loads the session of the key and decides how the next run uses it. A session book
// that cannot be read is treated as empty (the run must not fail over bookkeeping).
func (r *Runner) Plan(key Key, pol Policy, caps Caps) Plan {
	now := r.now()
	in := DecideInput{Policy: pol, Caps: caps, Now: now.Unix()}
	rec, ok, err := r.store.ActiveBackgroundSession(key.Kind, key.Project, key.WorkDir, key.Agent, key.Model)
	if err != nil {
		slog.Warn("bgagent.session_read_failed", "event", "bgagent.session_read_failed", "kind", key.Kind, "project", key.Project, "err", err)
	}
	if !ok && err == nil {
		// A session the older per-kind books left behind has no agent / model yet: the
		// first run that asks adopts it (it is bound by that run's Commit).
		if unbound, found, uerr := r.store.ActiveUnboundBackgroundSession(key.Kind, key.Project, key.WorkDir); uerr == nil && found {
			rec, ok = unbound, true
		}
	}
	if ok {
		in.Active = &rec
	} else if err == nil {
		if last, found, _ := r.store.LatestBackgroundSession(key.Kind, key.Project, key.WorkDir, key.Agent, key.Model); found && last.State == "reset" {
			in.Reset = true
		}
		if !in.Reset {
			if other := r.otherBinding(key); other {
				in.OtherBinding = true
			}
		}
	}
	p := Plan{Key: key, Decision: Decide(in), Started: now.Unix(), policy: pol}
	if in.Active != nil {
		p.rec = in.Active
	}
	return p
}

// otherBinding reports an active session of the same kind / project / work_dir bound to
// another agent or model.
func (r *Runner) otherBinding(key Key) bool {
	list, err := r.store.ListBackgroundSessions(key.Kind)
	if err != nil {
		return false
	}
	for _, b := range list {
		if b.Project == key.Project && b.WorkDir == key.WorkDir && (b.Agent != key.Agent || b.Model != key.Model) {
			return true
		}
	}
	return false
}

// Fallback ends the recorded session of a plan whose continuation failed (the session is
// gone, the agent refused) and returns the plan for a fresh session instead. The failed
// attempt is logged; it is not a failure of the run.
func (r *Runner) Fallback(p Plan, cause error, reason string) Plan {
	r.Fail(p, cause, true)
	p.rec = nil
	p.Decision = Decision{Mode: ModeFresh, Reason: reason}
	p.Started = r.now().Unix()
	return p
}

// Outcome is what a finished run reports to Commit.
type Outcome struct {
	JobID     string
	SessionID string
	Usage     *runner.Usage
	// Mode is the mode the run actually used (it differs from the plan's after a
	// fallback); empty = the planned one.
	Mode        Mode
	InputBytes  int64
	OutputBytes int64
}

// Commit records a successful run: the session book follows the mode actually used (a
// resume adds a run, a fresh or cleared session starts counting from this one), the run
// is logged with its usage, and a rotation by rule is audited.
func (r *Runner) Commit(p Plan, o Outcome) error {
	now := r.now().Unix()
	mode := o.Mode
	if mode == "" {
		mode = p.Decision.Mode
	}
	snap := SnapshotFromUsage(o.Usage)
	b := jobstore.BackgroundSession{
		Kind: p.Kind, Agent: p.Agent, Project: p.Project, WorkDir: p.WorkDir, Model: p.Model,
		LastUsedAt: now, LastJobID: o.JobID, State: "active", LastUsage: usageJSON(o.Usage),
		ContextUsed: snap.ContextUsed, ContextLimit: snap.ContextLimit,
	}
	prevID := ""
	switch {
	case mode == ModeResume && p.rec != nil:
		prevID = p.rec.SessionID
		b.SessionID, b.CreatedAt, b.Runs = p.rec.SessionID, p.rec.CreatedAt, p.rec.Runs+1
	case mode == ModeClearContext && p.rec != nil:
		prevID = p.rec.SessionID
		b.SessionID, b.CreatedAt, b.Runs = p.rec.SessionID, now, 1
		b.RotationReason = p.Decision.Reason
	default:
		if p.rec != nil {
			prevID = p.rec.SessionID
		}
		b.CreatedAt, b.Runs = now, 1
		b.RotationReason = p.Decision.Reason
	}
	if o.SessionID != "" {
		b.SessionID = o.SessionID
	}
	if b.SessionID == "" {
		b.SessionID = jobstore.BackgroundLocalSessionPrefix + p.Kind + ":" + p.Project + ":" + strconv.FormatInt(now, 10) + ":" + o.JobID
	}
	if mode == ModeResume && p.rec != nil && o.Usage == nil {
		// A turn that reported no usage keeps the previous level rather than forgetting it.
		b.ContextUsed, b.ContextLimit, b.LastUsage = p.rec.ContextUsed, p.rec.ContextLimit, p.rec.LastUsage
	}
	if err := r.store.ReplaceBackgroundSession(b); err != nil {
		return err
	}
	if _, err := r.store.InsertBackgroundRun(jobstore.BackgroundRun{
		Kind: p.Kind, Project: p.Project, JobID: o.JobID, SessionID: b.SessionID, StartedAt: p.Started, EndedAt: now,
		Status: jobstore.BackgroundRunOK, Mode: string(mode), RotationReason: b.RotationReason,
		InputBytes: o.InputBytes, OutputBytes: o.OutputBytes, UsageJSON: usageJSON(o.Usage),
	}); err != nil {
		slog.Warn("bgagent.run_log_failed", "event", "bgagent.run_log_failed", "kind", p.Kind, "err", err)
	}
	if p.Decision.Rotated && (mode == ModeFresh || mode == ModeClearContext) {
		r.audit(jobstore.AuditBackgroundRotated, p.Key, map[string]any{
			"reason": p.Decision.Reason, "mode": string(mode), "from_session": prevID, "to_session": b.SessionID,
			"runs": p.rec.Runs, "max_runs": p.policy.MaxRuns, "max_context_ratio": p.policy.MaxContextRatio,
			"max_age_sec": int64(p.policy.MaxAge / time.Second), "context_ratio": ratioOf(p.rec),
		})
	}
	return nil
}

func ratioOf(b *jobstore.BackgroundSession) any {
	if b == nil {
		return nil
	}
	if r, ok := sessionFrom(*b).ContextRatio(); ok {
		return r
	}
	return nil
}

// Observe folds in what a finished job reported after its run was committed: a resident
// session's turn only knows its usage (and the agent-side session id) once it has ended.
// The usage joins the run's log row, and when jobID is still the session's newest job the
// session's context level follows it, so the next Plan sees the real fill. Nothing here
// fails a run: a store error only warns.
func (r *Runner) Observe(key Key, jobID, sessionID string, u *runner.Usage) {
	if jobID == "" {
		return
	}
	if u != nil {
		if _, err := r.store.SetBackgroundRunUsage(key.Kind, jobID, usageJSON(u)); err != nil {
			slog.Warn("bgagent.observe_failed", "event", "bgagent.observe_failed", "kind", key.Kind, "err", err)
		}
	}
	rec, ok, err := r.store.ActiveBackgroundSession(key.Kind, key.Project, key.WorkDir, key.Agent, key.Model)
	if err != nil || !ok || rec.LastJobID != jobID {
		return
	}
	changed := false
	if snap := SnapshotFromUsage(u); u != nil && (snap.ContextLimit > 0 || snap.ContextUsed > 0) {
		rec.ContextUsed, rec.ContextLimit, rec.LastUsage = snap.ContextUsed, snap.ContextLimit, usageJSON(u)
		changed = true
	}
	if sessionID != "" && sessionID != rec.SessionID {
		rec.SessionID = sessionID
		changed = true
	}
	if !changed {
		return
	}
	if err := r.store.ReplaceBackgroundSession(rec); err != nil {
		slog.Warn("bgagent.observe_failed", "event", "bgagent.observe_failed", "kind", key.Kind, "err", err)
	}
}

// Fail records a run that did not produce an answer. dropSession ends the recorded
// session (state 'dropped': the continuation failed, the next run opens a new one).
// Cancellation and timeout get their own run status; they never drop the session.
func (r *Runner) Fail(p Plan, cause error, dropSession bool) {
	now := r.now().Unix()
	status := jobstore.BackgroundRunFailed
	switch {
	case errors.Is(cause, context.Canceled):
		status = jobstore.BackgroundRunCanceled
	case errors.Is(cause, context.DeadlineExceeded):
		status = jobstore.BackgroundRunTimedOut
	}
	sid := ""
	if p.rec != nil {
		sid = p.rec.SessionID
	}
	if _, err := r.store.InsertBackgroundRun(jobstore.BackgroundRun{
		Kind: p.Kind, Project: p.Project, SessionID: sid, StartedAt: p.Started, EndedAt: now, Status: status,
		Mode: string(p.Decision.Mode), RotationReason: p.Decision.Reason, Error: errText(cause),
	}); err != nil {
		slog.Warn("bgagent.run_log_failed", "event", "bgagent.run_log_failed", "kind", p.Kind, "err", err)
	}
	if dropSession && p.rec != nil {
		if err := r.store.RetireBackgroundSession(p.Kind, p.rec.SessionID, "dropped"); err != nil {
			slog.Warn("bgagent.session_drop_failed", "event", "bgagent.session_drop_failed", "kind", p.Kind, "err", err)
		}
		slog.Info("bgagent.session_dropped", "event", "bgagent.session_dropped", "kind", p.Kind, "project", p.Project, "agent", p.Agent, "err", cause)
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > 512 {
		s = s[:512]
	}
	return s
}

// Guard checks the daily limit and the budgets of a kind before a run. A trip is a hard
// stop: it is logged as a blocked run, audited, and returned as *HardStopError.
func (r *Runner) Guard(key Key, lim Limits) error {
	if lim.DailyRuns <= 0 && lim.DailyCostUSD <= 0 && lim.DailyTokens <= 0 {
		return nil
	}
	now := r.now()
	st, err := r.store.BackgroundStatsSince(key.Kind, r.day(now))
	if err != nil {
		// A broken counter must not turn a protection into an outage, nor silently pass:
		// the kinds are fail-open for their own infrastructure, so surface it as a warning.
		slog.Warn("bgagent.stats_failed", "event", "bgagent.stats_failed", "kind", key.Kind, "err", err)
		return nil
	}
	var stop *HardStopError
	switch {
	case lim.DailyRuns > 0 && st.Runs >= lim.DailyRuns:
		stop = &HardStopError{Kind: key.Kind, Reason: StopDailyLimit, Limit: float64(lim.DailyRuns), Used: float64(st.Runs)}
	case lim.DailyCostUSD > 0 && st.CostUSD >= lim.DailyCostUSD:
		stop = &HardStopError{Kind: key.Kind, Reason: StopDailyCost, Limit: lim.DailyCostUSD, Used: st.CostUSD}
	case lim.DailyTokens > 0 && st.TotalTokens >= lim.DailyTokens:
		stop = &HardStopError{Kind: key.Kind, Reason: StopDailyTokens, Limit: float64(lim.DailyTokens), Used: float64(st.TotalTokens)}
	}
	if stop == nil {
		return nil
	}
	r.recordStop(key, stop, now.Unix(), now.Unix())
	return stop
}

// recordStop logs a blocked / timed-out run and writes the audit event.
func (r *Runner) recordStop(key Key, stop *HardStopError, started, ended int64) {
	status := jobstore.BackgroundRunBlocked
	if stop.Reason == StopTimeout {
		status = jobstore.BackgroundRunTimedOut
	}
	if stop.Reason != StopTimeout { // a timed-out run is logged by Fail
		if _, err := r.store.InsertBackgroundRun(jobstore.BackgroundRun{
			Kind: key.Kind, Project: key.Project, StartedAt: started, EndedAt: ended, Status: status, Error: errText(stop),
		}); err != nil {
			slog.Warn("bgagent.run_log_failed", "event", "bgagent.run_log_failed", "kind", key.Kind, "err", err)
		}
	}
	r.audit(jobstore.AuditBackgroundStopped, key, map[string]any{"reason": stop.Reason, "limit": stop.Limit, "used": stop.Used})
	slog.Warn("bgagent.hard_stop", "event", "bgagent.hard_stop", "kind", key.Kind, "project", key.Project, "reason", stop.Reason,
		"limit", stop.Limit, "used", stop.Used)
}

func (r *Runner) audit(kind string, key Key, detail map[string]any) {
	detail["kind"], detail["project"], detail["work_dir"], detail["agent"], detail["model"] = key.Kind, key.Project, key.WorkDir, key.Agent, key.Model
	raw, err := marshalDetail(detail)
	if err != nil {
		return
	}
	target := key.Kind + ":" + key.Project
	if _, err := r.store.AppendAuditEvent(kind, target, "", raw); err != nil {
		slog.Warn("bgagent.audit_failed", "event", "bgagent.audit_failed", "audit", kind, "err", err)
	}
}

// Request is one run through Run.
type Request struct {
	Key
	Prompt string
	Title  string
	Args   []string
	Origin string
	Policy Policy
	Limits Limits
}

// Result is what Run returns.
type Result struct {
	ExecResult
	// Mode is the mode the answer was produced in (after any fallback); Reason why the
	// previous session was not simply continued.
	Mode   Mode
	Reason string
}

// Run is the whole lifecycle of one run for an adapter that only needs "ask the agent":
// guard the hard stops, plan the session, execute (falling back to a fresh session when a
// resume or clear fails because the session is unusable), then record the run.
func (r *Runner) Run(ctx context.Context, exec Executor, req Request) (Result, error) {
	if err := r.Guard(req.Key, req.Limits); err != nil {
		return Result{}, err
	}
	if req.Limits.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Limits.Timeout)
		defer cancel()
	}
	plan := r.Plan(req.Key, req.Policy, exec.Caps(req.Agent))
	exreq := func(p Plan) ExecRequest {
		er := ExecRequest{Key: req.Key, Mode: p.Decision.Mode, Prompt: req.Prompt, Title: req.Title, Args: req.Args,
			Origin: req.Origin, Timeout: req.Limits.Timeout}
		if p.Decision.Mode != ModeFresh {
			er.SessionID, er.LastJobID = p.Decision.Session.ID, p.Decision.Session.LastJobID
		}
		return er
	}
	res, err := exec.Execute(ctx, exreq(plan))
	if err != nil && plan.Decision.Mode != ModeFresh && (errors.Is(err, ErrSessionLost) || errors.Is(err, ErrClearUnsupported)) {
		// The session cannot be used: note why, drop it and start a new one. The
		// fallback is not a failure of the run.
		reason := ReasonSessionLost
		if errors.Is(err, ErrClearUnsupported) {
			reason = ReasonClearUnsupported
		}
		plan = r.Fallback(plan, err, reason)
		res, err = exec.Execute(ctx, exreq(plan))
	}
	if err != nil {
		var stop *HardStopError
		if errors.Is(err, context.DeadlineExceeded) && req.Limits.Timeout > 0 && ctx.Err() != nil {
			stop = &HardStopError{Kind: req.Kind, Reason: StopTimeout, Limit: req.Limits.Timeout.Seconds(), Used: req.Limits.Timeout.Seconds()}
			r.recordStop(req.Key, stop, plan.Started, r.now().Unix())
		}
		r.Fail(plan, err, false)
		if stop != nil {
			return Result{ExecResult: res, Mode: plan.Decision.Mode, Reason: plan.Decision.Reason}, stop
		}
		return Result{ExecResult: res, Mode: plan.Decision.Mode, Reason: plan.Decision.Reason}, err
	}
	out := Outcome{JobID: res.JobID, SessionID: res.SessionID, Usage: res.Usage, Mode: plan.Decision.Mode,
		InputBytes: int64(len(req.Prompt)), OutputBytes: int64(len(res.Output))}
	if cerr := r.Commit(plan, out); cerr != nil {
		slog.Warn("bgagent.commit_failed", "event", "bgagent.commit_failed", "kind", req.Kind, "err", cerr)
	}
	return Result{ExecResult: res, Mode: plan.Decision.Mode, Reason: plan.Decision.Reason}, nil
}

// Reset marks the active sessions of kind + project + work_dir as manually reset: the
// next run is fresh. It leaves history and usage alone and does not stop a run in
// flight. n is how many sessions it ended.
func (r *Runner) Reset(kind, project, workDir, actor string) (int64, error) {
	n, err := r.store.ResetBackgroundSessions(kind, project, workDir)
	if err != nil {
		return 0, err
	}
	r.audit("background_agent.reset", Key{Kind: kind, Project: project, WorkDir: workDir}, map[string]any{"sessions": n, "actor": actor})
	return n, nil
}

// SessionStatus is one session as the status view shows it.
type SessionStatus struct {
	Session
	Agent   string
	Project string
	WorkDir string
	Model   string
	// ContextRatio is nil when the context fill is unknown (not 0).
	ContextRatio *float64
	// RotationReason is why this session replaced its predecessor.
	RotationReason string
	LastUsage      *runner.Usage
}

// KindStatus is the read model of one kind: its sessions, today's totals and the policy.
type KindStatus struct {
	Kind     string
	Sessions []SessionStatus
	Today    jobstore.BackgroundDayStats
	Policy   Policy
}

// Status reads the status of a kind. DC3's management page builds on it.
func (r *Runner) Status(kind string, pol Policy) (KindStatus, error) {
	list, err := r.store.ListBackgroundSessions(kind)
	if err != nil {
		return KindStatus{}, err
	}
	st := KindStatus{Kind: kind, Policy: pol}
	for _, b := range list {
		ss := SessionStatus{Session: sessionFrom(b), Agent: b.Agent, Project: b.Project, WorkDir: b.WorkDir,
			Model: b.Model, RotationReason: b.RotationReason}
		if ratio, ok := ss.Session.ContextRatio(); ok {
			ss.ContextRatio = &ratio
		}
		ss.LastUsage = decodeUsage(b.LastUsage)
		st.Sessions = append(st.Sessions, ss)
	}
	st.Today, err = r.store.BackgroundStatsSince(kind, r.day(r.now()))
	return st, err
}
