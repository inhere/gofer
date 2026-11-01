// Package bgagent is the server's common runner for background agents (design
// 2026-10-30 §3): one place that owns the session book of an agent kind (job_check,
// summarizer, steward), decides before every run whether to continue, clear or replace
// the session, enforces the hard stops (daily limit, budget, timeout) and leaves an audit
// trail. The kinds are adapters: each supplies the prompt and the way to actually run a
// turn (Executor), and keeps its own business logic.
//
// The package sits beside work / steward in the layering: it imports only the data layer
// (config, jobstore, runner) and never the job service, the entry layers or the
// orchestration packages; the job layer is reached through the Executor seam the entry
// layer fills in.
package bgagent

import (
	"strings"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// The background agent kinds. They are the first dimension of a session's key: a kind
// never reuses another kind's session.
const (
	KindJobCheck   = "job_check"
	KindSummarizer = jobstore.BackgroundKindSummarizer
	KindSteward    = "steward"
)

// Key is the ownership key of a session: kind is a hard isolation dimension, and a session
// is only continued by a run whose key is identical.
type Key struct {
	Kind    string
	Agent   string
	Project string
	WorkDir string
	Model   string
}

// Mode is how a run relates to the session book.
type Mode string

const (
	// ModeFresh opens a new agent session.
	ModeFresh Mode = "fresh"
	// ModeResume continues the recorded session (the agent's resume / load).
	ModeResume Mode = "resume"
	// ModeClearContext keeps the session id and starts a clean logical context in it.
	ModeClearContext Mode = "clear_context"
)

// Why a decision was taken (Decision.Reason, audit detail, background_agent_*.rotation_reason).
const (
	ReasonNone             = ""
	ReasonManualReset      = "manual_reset"
	ReasonContextRatio     = "context_ratio"
	ReasonMaxRuns          = "max_runs"
	ReasonMaxAge           = "max_age"
	ReasonKeyChanged       = "key_changed"
	ReasonNotResumable     = "not_resumable"
	ReasonSessionLost      = "session_lost"
	ReasonClearUnsupported = "clear_unsupported"
)

// Policy is the rotation policy: the main rule is the context fill, the run count is the
// secondary rule (it also covers an agent that reports no usage), and MaxAge is only an
// explicit last-resort bound. A zero field disables its rule, so a policy with no MaxAge
// never rotates by time.
type Policy struct {
	MaxRuns         int
	MaxContextRatio float64
	MaxAge          time.Duration
}

// PolicyFromConfig reads server.background_agents.session (defaults: ratio 0.7, 20 runs,
// no age bound).
func PolicyFromConfig(c config.BackgroundAgentSessionConfig) Policy {
	p := Policy{MaxRuns: c.EffectiveMaxRuns(), MaxContextRatio: c.EffectiveMaxContextRatio()}
	if v := strings.TrimSpace(c.MaxAge); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			p.MaxAge = d
		}
	}
	return p
}

// Caps is what the agent behind a kind can do with a recorded session.
type Caps struct {
	// CanResume: the agent can continue a finished session (its resume / load argv).
	CanResume bool
	// CanClearContext: the agent can start a clean context inside the same session id
	// (suga run --sid <id> --clear-ctx). It only matters for an agent that can resume.
	CanClearContext bool
}

// Decision is what to do for the next run.
type Decision struct {
	Mode Mode
	// Reason is why the previous session is not simply continued ("" = it is, or there
	// never was one).
	Reason string
	// Session is the recorded session the run continues or clears (zero for ModeFresh).
	Session Session
	// Rotated reports that an existing session was replaced or cleared by a rule
	// (context ratio, run count, age) — the audited events.
	Rotated bool
}

// Session is the recorded state of one background session.
type Session struct {
	ID           string
	LastJobID    string
	Runs         int
	CreatedAt    int64
	LastUsedAt   int64
	ContextUsed  int64
	ContextLimit int64
}

// ContextRatio is the context fill and whether it is known (a missing window is unknown,
// never 0).
func (s Session) ContextRatio() (float64, bool) {
	if s.ContextLimit <= 0 || s.ContextUsed <= 0 {
		return 0, false
	}
	return float64(s.ContextUsed) / float64(s.ContextLimit), true
}

func sessionFrom(b jobstore.BackgroundSession) Session {
	return Session{ID: b.SessionID, LastJobID: b.LastJobID, Runs: b.Runs, CreatedAt: b.CreatedAt,
		LastUsedAt: b.LastUsedAt, ContextUsed: b.ContextUsed, ContextLimit: b.ContextLimit}
}

// DecideInput is everything Decide looks at.
type DecideInput struct {
	Policy Policy
	Caps   Caps
	Now    int64
	// Active is the active session recorded for the exact key (nil = none).
	Active *jobstore.BackgroundSession
	// Reset reports that the newest session of the key was manually reset.
	Reset bool
	// OtherBinding reports an active session of the same kind / project / work_dir that
	// belongs to another agent or model.
	OtherBinding bool
}

// Decide is the unified rotation rule (design §3.3), evaluated before every run:
//
//  1. manual reset: the next run is fresh;
//  2. context ratio at or over the limit, or runs at or over the limit: rotate — clear the
//     context in the same session when the agent can, else a fresh session;
//  3. a changed agent / model / work_dir (no session for this exact key), or a session
//     that cannot be continued: fresh;
//  4. an explicit max age over its limit: rotate the same way;
//  5. otherwise continue.
func Decide(in DecideInput) Decision {
	fresh := func(reason string) Decision { return Decision{Mode: ModeFresh, Reason: reason} }
	if in.Active == nil {
		switch {
		case in.Reset:
			return fresh(ReasonManualReset)
		case in.OtherBinding:
			return fresh(ReasonKeyChanged)
		}
		return fresh(ReasonNone)
	}
	rec := sessionFrom(*in.Active)
	if !in.Caps.CanResume {
		return fresh(ReasonNotResumable)
	}
	rotate := func(reason string) Decision {
		if in.Caps.CanClearContext {
			return Decision{Mode: ModeClearContext, Reason: reason, Session: rec, Rotated: true}
		}
		return Decision{Mode: ModeFresh, Reason: reason, Rotated: true}
	}
	if ratio, ok := rec.ContextRatio(); ok && in.Policy.MaxContextRatio > 0 && ratio >= in.Policy.MaxContextRatio {
		return rotate(ReasonContextRatio)
	}
	if in.Policy.MaxRuns > 0 && rec.Runs >= in.Policy.MaxRuns {
		return rotate(ReasonMaxRuns)
	}
	if in.Policy.MaxAge > 0 && in.Now-rec.CreatedAt >= int64(in.Policy.MaxAge/time.Second) {
		return rotate(ReasonMaxAge)
	}
	return Decision{Mode: ModeResume, Session: rec}
}

// LimitsFromConfig reads the budget hard stops of server.background_agents (the daily run
// limit and the timeout belong to the kind's own configuration, the caller adds them).
func LimitsFromConfig(c config.BackgroundAgentsConfig) Limits {
	return Limits{DailyCostUSD: c.DailyCostUSD, DailyTokens: c.DailyTokens}
}

// ModelFromArgs reads the model an agent argv selects (`--model X`, `--model=X`, `-m X`),
// "" when it names none: the model is part of the session key, so changing it opens a
// new session. Arguments it cannot parse are left alone.
func ModelFromArgs(args []string) string {
	model := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--model" || a == "-m":
			if i+1 < len(args) {
				model = strings.TrimSpace(args[i+1])
				i++
			}
		case strings.HasPrefix(a, "--model="):
			model = strings.TrimSpace(strings.TrimPrefix(a, "--model="))
		}
	}
	return model
}
