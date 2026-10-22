package work

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// The summarizer's own agent session (gofer-ayu0). Every tidy-up used to open a fresh
// cli-agent session, so a busy workspace ended up with a long tail of sessions nobody
// ever talks in. Now a project keeps ONE summarizer session: the first run opens it, every
// later run continues it through the agent's own resume argv (session_resume, the same
// seam `job resume` uses), and it is replaced once it has served
// work.summarizer_session_max_runs tidy-ups or is older than
// work.summarizer_session_max_age. A session that cannot be continued (deleted, agent
// refuses) falls back to a fresh one — the tidy-up itself must not fail over that.

// errSummaryOutput marks "the job ran but its answer is not the fixed JSON". It is what
// tells a bad answer (retry in the SAME session) apart from a session that could not be
// continued (start a new one).
var errSummaryOutput = errors.New("summarizer output is not the fixed JSON")

// summarizerProjectKey is the in-flight claim of a project's summarizer session: two
// tidy-ups sharing one session would interleave their turns.
func summarizerProjectKey(project string) string { return "project:" + project }

// summarizerSession picks the session this tidy-up runs in: the project's recorded one, or
// a fresh record when there is none / the rotation says so. resumed reports whether the run
// continues the previous job's session (only then is there something to ignore).
func (s *Service) summarizerSession(project string, c config.WorkConfig, canResume bool) (jobstore.SummarizerSession, bool) {
	now := s.nowFn().Unix()
	rec, ok, err := s.store.GetSummarizerSession(project)
	if err != nil {
		slog.Warn("work.summarizer_session_read_failed", "event", "work.summarizer_session_read_failed",
			"project", project, "err", err)
		ok = false
	}
	if ok {
		runs := c.SummarizerSessionRuns()
		age := int64(c.SummarizerSessionAge() / time.Second)
		rotated := (runs > 0 && rec.Runs >= runs) || (age > 0 && now-rec.CreatedAt >= age)
		if !rotated {
			return rec, canResume && rec.LastJobID != ""
		}
	}
	return jobstore.SummarizerSession{ProjectKey: project, CreatedAt: now, UpdatedAt: now}, false
}

// commitSummarizerSession records a successful run: the session now has served one more
// tidy-up and the job whose session the next run continues. Without a job id there is
// nothing to continue, so the next run starts a new session.
func (s *Service) commitSummarizerSession(rec jobstore.SummarizerSession, project, jobID, sessionID string) {
	if project == "" {
		return
	}
	rec.ProjectKey = project
	rec.Runs++
	rec.LastJobID = jobID
	if sessionID != "" {
		rec.SessionID = sessionID
	}
	rec.UpdatedAt = s.nowFn().Unix()
	if rec.CreatedAt == 0 {
		rec.CreatedAt = rec.UpdatedAt
	}
	if err := s.store.SaveSummarizerSession(rec); err != nil {
		slog.Warn("work.summarizer_session_save_failed", "event", "work.summarizer_session_save_failed",
			"project", project, "err", err)
	}
}

// resetSummarizerSession drops the recorded session after a continuation failed, so the
// next run opens a fresh one, and leaves the event behind.
func (s *Service) resetSummarizerSession(project, agentName string, cause error) {
	if project != "" {
		if err := s.store.DeleteSummarizerSession(project); err != nil {
			slog.Warn("work.summarizer_session_reset_failed", "event", "work.summarizer_session_reset_failed",
				"project", project, "err", err)
		}
	}
	slog.Info("work.summarizer_session_reset", "event", "work.summarizer_session_reset",
		"project", project, "agent", agentName, "err", cause)
}

// runSummaryAttempt runs one tidy-up in the chosen session: a continuation of the recorded
// job when resumed, a fresh job otherwise. The JSON retry lives here, so a bad answer is
// retried in the same session; anything else climbs out, which lets the caller start a new
// session instead of failing the tidy-up.
func (s *Service) runSummaryAttempt(ctx context.Context, w jobstore.WorkItem, material string, degraded bool,
	agentName string, c config.WorkConfig, project, title string, session jobstore.SummarizerSession,
	resumed bool, origin string) (OneShotResult, SummaryOut, error) {
	var last OneShotResult
	for attempt := 0; attempt < 2; attempt++ {
		prompt := s.buildPrompt(w, material, degraded, resumed)
		if attempt > 0 {
			prompt += "\n\n上一次的输出不是合法的 JSON。这一次只输出一个 JSON 对象，不要任何其它文字。"
		}
		var r OneShotResult
		var err error
		if resumed {
			resumer, ok := s.oneShot.(SessionResumer)
			if !ok {
				return last, SummaryOut{}, errors.New("整理器没有续用会话的通道")
			}
			r, err = resumer.Resume(ctx, ResumeRequest{
				JobID: session.LastJobID, Prompt: prompt, ProjectKey: project, Title: title,
				TimeoutSec: int(summarizeTimeout / time.Second), Origin: origin,
			})
		} else {
			r, err = s.oneShot.Run(ctx, OneShotRequest{
				Agent: agentName, Args: c.SummarizerArgsOrDefault(), ProjectKey: project, Prompt: prompt,
				Title: title, TimeoutSec: int(summarizeTimeout / time.Second), Origin: origin,
			})
		}
		if r.JobID != "" {
			last.JobID = r.JobID
		}
		if r.SessionID != "" {
			last.SessionID = r.SessionID
		}
		if err != nil {
			return last, SummaryOut{}, err
		}
		out, perr := ParseSummary(r.Output)
		if perr == nil {
			return last, out, nil
		}
		lastErr := fmt.Errorf("%w: %v", errSummaryOutput, perr)
		if attempt == 1 {
			return last, SummaryOut{}, lastErr
		}
	}
	return last, SummaryOut{}, fmt.Errorf("%w: 没有拿到输出", errSummaryOutput)
}
