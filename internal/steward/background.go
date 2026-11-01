package steward

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/inhere/gofer/internal/bgagent"
	"github.com/inhere/gofer/internal/config"
)

// The steward's session book lives in the common background runner (internal/bgagent,
// kind 'steward'). Its session is resident and ends when idle; the next need asks the
// runner first, and the runner says continue (load the previous session), or rebuild
// from the prime — after the unified rotation rule (context fill, run count, explicit max
// age, manual reset) or when the continuation failed. The steward never clears its
// context: it needs its role and its working thread, and the database (notes, work items)
// is the source of truth for a rebuild.

// ResumeSpec asks the host to continue the session of an earlier steward job (the agent
// loads the same session) and drive Prompt as its next turn.
type ResumeSpec struct {
	// JobID is the earlier steward session job whose session is loaded.
	JobID   string
	Prompt  string
	Project string
}

// SessionResumer is the optional face of a SessionHost that can load an earlier steward
// session. A host without it always rebuilds.
type SessionResumer interface {
	ResumeSession(ResumeSpec) (string, error)
}

// SetBackgroundAgentsFn supplies the live server.background_agents block (unified
// rotation thresholds and budget hard stops). nil keeps the defaults.
func (s *Service) SetBackgroundAgentsFn(fn func() config.BackgroundAgentsConfig) { s.bgCfgFn = fn }

func (s *Service) bgConfig() config.BackgroundAgentsConfig {
	if s.bgCfgFn != nil {
		return s.bgCfgFn()
	}
	return config.BackgroundAgentsConfig{}
}

func (s *Service) bgRunner() *bgagent.Runner {
	return bgagent.New(s.store, bgagent.Options{Now: func() time.Time { return s.nowFn() }})
}

// bgKey is the ownership key of the steward's session: kind + the configured project
// (the steward runs in no work directory of its own) + agent.
func bgKey(c config.StewardConfig) bgagent.Key {
	return bgagent.Key{Kind: bgagent.KindSteward, Agent: c.AgentName(), Project: c.ProjectKey()}
}

// bgBegin prepares the next (re)start: it folds the previous session's usage into the
// book, checks the hard stops and plans the session. A tripped hard stop is returned as
// an error; the wake fails and shows it, nothing loops.
func (s *Service) bgBegin(c config.StewardConfig) (bgagent.Plan, error) {
	r := s.bgRunner()
	key := bgKey(c)
	s.observePrevious(r, key)
	if err := r.Guard(key, bgagent.LimitsFromConfig(s.bgConfig())); err != nil {
		return bgagent.Plan{}, err
	}
	_, canResume := s.host.(SessionResumer)
	return r.Plan(key, bgagent.PolicyFromConfig(s.bgConfig().Session), bgagent.Caps{CanResume: canResume}), nil
}

// observePrevious reads what the previous session job reported (usage, the agent-side
// session id) now that it has ended, and records it on the book.
func (s *Service) observePrevious(r *bgagent.Runner, key bgagent.Key) {
	rec, ok := s.recordedSession(r, key)
	if !ok || rec.LastJobID == "" {
		return
	}
	info, found := s.host.Job(rec.LastJobID)
	if !found || info.Live {
		return
	}
	r.Observe(key, rec.LastJobID, info.SessionID, info.Usage)
}

// recordedSession is the active record of the key, through the runner's plan (so the
// adoption rules stay in one place).
func (s *Service) recordedSession(r *bgagent.Runner, key bgagent.Key) (bgagent.Session, bool) {
	p := r.Plan(key, bgagent.Policy{}, bgagent.Caps{CanResume: true})
	if p.Decision.Mode != bgagent.ModeResume {
		return bgagent.Session{}, false
	}
	return p.Decision.Session, true
}

// tryResume continues the previous session when the plan says so. ok=false means the
// caller rebuilds from the prime (the returned plan is then the fresh one).
func (s *Service) tryResume(r *bgagent.Runner, plan bgagent.Plan, c config.StewardConfig, turn string) (bgagent.Plan, string, bool) {
	if plan.Decision.Mode != bgagent.ModeResume || plan.Decision.Session.LastJobID == "" {
		return plan, "", false
	}
	resumer, ok := s.host.(SessionResumer)
	if !ok {
		return plan, "", false
	}
	id, err := resumer.ResumeSession(ResumeSpec{JobID: plan.Decision.Session.LastJobID, Prompt: turn, Project: c.ProjectKey()})
	if err == nil {
		slog.Info("steward.session_resumed", "event", "steward.session_resumed", "job", id,
			"from_job", plan.Decision.Session.LastJobID, "agent", c.AgentName())
		if cerr := r.Commit(plan, bgagent.Outcome{JobID: id, SessionID: plan.Decision.Session.ID, Mode: bgagent.ModeResume,
			InputBytes: int64(len(turn))}); cerr != nil {
			slog.Warn("steward.session_record_failed", "event", "steward.session_record_failed", "err", cerr)
		}
		return plan, id, true
	}
	// The session was deleted, the agent / project / model changed under it, or the load
	// failed: say why and rebuild from the prime — the notes and work items are the facts.
	slog.Info("steward.resume_failed", "event", "steward.resume_failed", "job", plan.Decision.Session.LastJobID,
		"agent", c.AgentName(), "err", err)
	return r.Fallback(plan, fmt.Errorf("%w: %v", bgagent.ErrSessionLost, err), bgagent.ReasonSessionLost), "", false
}

// commitRebuild records a session opened from the prime.
func (s *Service) commitRebuild(r *bgagent.Runner, plan bgagent.Plan, jobID string, promptBytes int) {
	if err := r.Commit(plan, bgagent.Outcome{JobID: jobID, Mode: bgagent.ModeFresh, InputBytes: int64(promptBytes)}); err != nil {
		slog.Warn("steward.session_record_failed", "event", "steward.session_record_failed", "err", err)
	}
}

// failBackground records a start that failed.
func (s *Service) failBackground(r *bgagent.Runner, plan bgagent.Plan, err error) {
	if err != nil && !errors.Is(err, ErrDisabled) {
		r.Fail(plan, err, false)
	}
}
