package work

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/inhere/gofer/internal/bgagent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
)

// The summarizer's own agent session (gofer-ayu0). Every tidy-up used to open a fresh
// cli-agent session, so a busy workspace ended up with a long tail of sessions nobody
// ever talks in. Now a project keeps ONE summarizer session: the first run opens it, every
// later run continues it through the agent's own resume argv (session_resume, the same
// seam `job resume` uses), and the common background runner (internal/bgagent) replaces it
// by the unified rotation rule (context fill, run count, explicit max age). A session that
// cannot be continued (deleted, agent refuses) falls back to a fresh one — the tidy-up
// itself must not fail over that. The session book is the common background session table;
// this file is the summarizer's adapter to it.

// errSummaryOutput marks "the job ran but its answer is not the fixed JSON". It is what
// tells a bad answer (retry in the SAME session) apart from a session that could not be
// continued (start a new one).
var errSummaryOutput = errors.New("summarizer output is not the fixed JSON")

// summarizerProjectKey is the in-flight claim of a project's summarizer session: two
// tidy-ups sharing one session would interleave their turns.
func summarizerProjectKey(project string) string { return "project:" + project }

// summarizerPolicy is the rotation policy of the summarizer's session: the unified
// server.background_agents.session thresholds (context ratio first, run count second,
// max_age only when configured), with the older work.summarizer_session_* fields still
// honoured when the operator set them.
//
// DEPRECATED(v0.208): remove in v0.211 — the work.summarizer_session_max_runs /
// summarizer_session_max_age fields are read as an override of the unified thresholds; the
// 24h default age of the older field is kept ONLY because the summarizer's behaviour is
// unchanged in this step (a deliberately unconfigured age should stop rotating by time once
// the legacy fields go).
func summarizerPolicy(c config.WorkConfig, bg config.BackgroundAgentsConfig) bgagent.Policy {
	p := bgagent.PolicyFromConfig(bg.Session)
	if c.SummarizerSessionMaxRuns != 0 {
		p.MaxRuns = c.SummarizerSessionRuns()
	}
	switch {
	case c.SummarizerSessionMaxAge != "":
		p.MaxAge = c.SummarizerSessionAge()
	case p.MaxAge == 0:
		p.MaxAge = config.DefaultWorkSummarizerSessionAge
	}
	return p
}

// bgRunner is the common background runner over the work store, on this service's clock.
func (s *Service) bgRunner() *bgagent.Runner {
	return bgagent.New(s.store, bgagent.Options{
		Now:        func() time.Time { return s.nowFn() },
		StartOfDay: func(time.Time) int64 { return s.startOfDay() },
	})
}

func (s *Service) bgConfig() config.BackgroundAgentsConfig {
	if s.bgCfgFn != nil {
		return s.bgCfgFn()
	}
	return config.BackgroundAgentsConfig{}
}

// summarizerKey is the ownership key of the tidy-up session: kind + project (the summarizer
// job runs in the project's directory) + agent + model.
func summarizerKey(project, agentName string, c config.WorkConfig) bgagent.Key {
	return bgagent.Key{Kind: bgagent.KindSummarizer, Agent: agentName, Project: project,
		Model: bgagent.ModelFromArgs(c.SummarizerArgsOrDefault())}
}

// summarizerPlan picks the session this tidy-up runs in through the common runner: the
// project's recorded one (continue), or a fresh session when there is none / the rotation
// says so. resumed reports whether the run continues the previous job's session (only then
// is there something to ignore).
func (s *Service) summarizerPlan(project, agentName string, c config.WorkConfig, canResume bool) (bgagent.Plan, bool) {
	plan := s.bgRunner().Plan(summarizerKey(project, agentName, c), summarizerPolicy(c, s.bgConfig()),
		bgagent.Caps{CanResume: canResume})
	resumed := plan.Decision.Mode == bgagent.ModeResume && plan.Decision.Session.LastJobID != ""
	return plan, resumed
}

// commitSummarizerSession records a successful run: the session now has served one more
// tidy-up and the job whose session the next run continues. Without a job id there is
// nothing to continue, so the next run starts a new session.
func (s *Service) commitSummarizerSession(plan bgagent.Plan, resumed bool, project string, last OneShotResult, inputBytes int) {
	if project == "" {
		return
	}
	mode := bgagent.ModeFresh
	if resumed {
		mode = bgagent.ModeResume
	}
	if err := s.bgRunner().Commit(plan, bgagent.Outcome{JobID: last.JobID, SessionID: last.SessionID, Usage: last.Usage,
		Mode: mode, InputBytes: int64(inputBytes), OutputBytes: int64(len(last.Output))}); err != nil {
		slog.Warn("work.summarizer_session_save_failed", "event", "work.summarizer_session_save_failed",
			"project", project, "err", err)
	}
}

// resetSummarizerSession drops the recorded session after a continuation failed, so the
// next run opens a fresh one, and leaves the event behind. It returns the plan of that
// fresh run.
func (s *Service) resetSummarizerSession(plan bgagent.Plan, project, agentName string, cause error) bgagent.Plan {
	slog.Info("work.summarizer_session_reset", "event", "work.summarizer_session_reset",
		"project", project, "agent", agentName, "err", cause)
	return s.bgRunner().Fallback(plan, fmt.Errorf("%w: %v", bgagent.ErrSessionLost, cause), bgagent.ReasonSessionLost)
}

// oneShotExecutor adapts the summarizer's OneShot / SessionResumer seams to the common
// runner's Executor: a fresh run is OneShot.Run, a resume is SessionResumer.Resume of the
// recorded job. A failed continuation is reported as bgagent.ErrSessionLost, which is what
// makes the caller start a new session instead of failing the tidy-up.
type oneShotExecutor struct {
	oneShot  OneShot
	noResume error
}

func (e oneShotExecutor) Caps(agentName string) bgagent.Caps {
	if keeper, ok := e.oneShot.(SessionKeeper); ok {
		return bgagent.Caps{CanResume: keeper.CanContinueSession(agentName)}
	}
	return bgagent.Caps{}
}

func (e oneShotExecutor) Execute(ctx context.Context, r bgagent.ExecRequest) (bgagent.ExecResult, error) {
	var res OneShotResult
	var err error
	switch r.Mode {
	case bgagent.ModeResume:
		resumer, ok := e.oneShot.(SessionResumer)
		if !ok {
			return bgagent.ExecResult{}, e.noResume
		}
		res, err = resumer.Resume(ctx, ResumeRequest{
			JobID: r.LastJobID, Prompt: r.Prompt, ProjectKey: r.Project, Title: r.Title,
			TimeoutSec: int(r.Timeout / time.Second), Origin: r.Origin,
		})
		if err != nil {
			err = fmt.Errorf("%w: %w", bgagent.ErrSessionLost, err)
		}
	case bgagent.ModeClearContext:
		return bgagent.ExecResult{}, bgagent.ErrClearUnsupported
	default:
		res, err = e.oneShot.Run(ctx, OneShotRequest{
			Agent: r.Agent, Args: r.Args, ProjectKey: r.Project, Prompt: r.Prompt,
			Title: r.Title, TimeoutSec: int(r.Timeout / time.Second), Origin: r.Origin,
		})
	}
	out := bgagent.ExecResult{JobID: res.JobID, SessionID: res.SessionID, Output: []byte(res.Output), Usage: res.Usage}
	return out, err
}

// runSummaryAttempt runs one tidy-up in the chosen session: a continuation of the recorded
// job when resumed, a fresh job otherwise. The JSON retry lives here, so a bad answer is
// retried in the same session; anything else climbs out, which lets the caller start a new
// session instead of failing the tidy-up.
func (s *Service) runSummaryAttempt(ctx context.Context, w jobstore.WorkItem, material string, degraded bool,
	agentName string, c config.WorkConfig, project, title string, plan bgagent.Plan,
	resumed bool, origin string) (OneShotResult, SummaryOut, error) {
	var last OneShotResult
	exec := oneShotExecutor{oneShot: s.oneShot,
		noResume: errors.New(i18n.T(s.lang(w.ProjectKey), "work.summarize.no_resume_channel"))}
	for attempt := 0; attempt < 2; attempt++ {
		prompt := s.buildPrompt(w, material, degraded, resumed)
		if attempt > 0 {
			prompt += "\n\n" + i18n.T(s.lang(w.ProjectKey), "work.prompt.retry_json")
		}
		mode := bgagent.ModeFresh
		req := bgagent.ExecRequest{Key: plan.Key, Prompt: prompt, Title: title, Args: c.SummarizerArgsOrDefault(),
			Origin: origin, Timeout: summarizeTimeout}
		if resumed {
			mode = bgagent.ModeResume
			req.SessionID, req.LastJobID = plan.Decision.Session.ID, plan.Decision.Session.LastJobID
		}
		req.Mode = mode
		er, err := exec.Execute(ctx, req)
		r := OneShotResult{JobID: er.JobID, SessionID: er.SessionID, Output: string(er.Output), Usage: er.Usage}
		if r.JobID != "" {
			last.JobID = r.JobID
		}
		if r.SessionID != "" {
			last.SessionID = r.SessionID
		}
		if err != nil {
			return last, SummaryOut{}, err
		}
		last.Output = r.Output
		if r.Usage != nil {
			last.Usage = r.Usage
		}
		out, perr := ParseSummary(r.Output)
		if perr == nil {
			return last, out, nil
		}
		if errors.Is(perr, ErrNoSummaryObject) {
			perr = errors.New(i18n.T(s.lang(w.ProjectKey), "work.summarize.no_json_object"))
		}
		lastErr := fmt.Errorf("%w: %v", errSummaryOutput, perr)
		if attempt == 1 {
			return last, SummaryOut{}, lastErr
		}
	}
	return last, SummaryOut{}, fmt.Errorf("%w: %s", errSummaryOutput, i18n.T(s.lang(w.ProjectKey), "work.summarize.no_output"))
}

// BackgroundStatus is the common-runner status of the tidy-up kind (sessions, today's totals
// on this service's day, and the policy its runs use, including the legacy overrides).
func (s *Service) BackgroundStatus() (bgagent.KindStatus, error) {
	return s.bgRunner().Status(bgagent.KindSummarizer, summarizerPolicy(s.cfg(), s.bgConfig()))
}
