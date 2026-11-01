package engine

import (
	"context"
	"slices"
	"sort"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/memorysynthesis"
)

// TickResult is what one Tick did for one project.
type TickResult struct {
	Project string
	Day     string
	// State is a memorysynthesis.Outcome* value; empty when Err is set.
	State string
	Run   jobstore.MemorySynthesisRun
	Err   error
}

// ScheduledBy is the CreatedBy of the runs the built-in daily trigger starts.
const ScheduledBy = "schedule"

// triggerDay is the business day a Tick at `now` works on: the day before the local date,
// once the configured trigger time of that date has passed. ok is false before the trigger.
//
// Missed time points: the rule is stateless, so a server that was down at the trigger time
// catches up at its first Tick later the same local date; a date that passed entirely
// (down for a whole day) is skipped, never back-filled.
func triggerDay(now time.Time, hour, minute int) (day string, ok bool) {
	trigger := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	if now.Before(trigger) {
		return "", false
	}
	return time.Date(now.Year(), now.Month(), now.Day()-1, 0, 0, 0, 0, now.Location()).Format(memorysynthesis.DayLayout), true
}

// Tick is the built-in daily trigger. The serve loop calls it every minute or so; it is
// cheap and idempotent: nothing happens when the feature is off, before the trigger time,
// or once the day is settled. It runs the model at most once per project per target day
// (more only when daily_limit allows and the input changed), never in parallel with itself
// for the same project, and survives restarts because every decision is read back from the
// run records (limits, running, backoff after a failure). Tick is not safe to call
// concurrently with itself; the loop is a single goroutine.
//
// The trigger time and the day boundary use the server time zone (Deps.Loc). The target day
// is the previous local day, so its window is complete.
func (e *Engine) Tick(ctx context.Context) []TickResult {
	cfg := e.d.Config()
	if cfg == nil || !cfg.Memory.Synthesis.Enabled || e.d.Store == nil {
		return nil
	}
	syn := cfg.Memory.Synthesis
	now := e.now().In(e.loc())
	h, m := syn.TriggerClock(cfg.Work)
	day, ok := triggerDay(now, h, m)
	if !ok {
		return nil
	}
	projects := slices.Clone(syn.Projects)
	if len(projects) == 0 {
		for key := range cfg.Projects {
			projects = append(projects, key)
		}
		sort.Strings(projects)
	}
	var out []TickResult
	for _, project := range projects {
		if ctx.Err() != nil {
			return out
		}
		if _, known := cfg.Projects[project]; !known || !e.due(project, day, now, syn.ScheduledRunLimit(), syn.RetryAfterDuration()) {
			continue
		}
		res := TickResult{Project: project, Day: day}
		// Throttle before the attempt so a persistent error (unusable agent) is retried at
		// the retry_after pace, not once per tick.
		e.hold(project, day, now.Add(syn.RetryAfterDuration()))
		outcome, err := e.Run(ctx, Request{Project: project, Day: day, CreatedBy: ScheduledBy, Scheduled: true})
		res.State, res.Run, res.Err = outcome.State, outcome.Run, err
		out = append(out, res)
	}
	return out
}

// due is the cheap pre-check of one project day, from the in-memory throttle and the run
// records only (no material is read): false when the day is settled for the scheduler.
// The store re-checks the same limits atomically before it starts a run.
func (e *Engine) due(project, day string, now time.Time, limit int, retryAfter time.Duration) bool {
	e.mu.Lock()
	if until, ok := e.nextTry[project+"/"+day]; ok && now.Before(until) {
		e.mu.Unlock()
		return false
	}
	e.mu.Unlock()
	runs, err := e.d.Store.ListMemorySynthesisRuns(jobstore.MemorySynthesisRunFilter{ProjectKey: project, Day: day, Limit: 1000})
	if err != nil {
		return false
	}
	started := 0
	var lastFail int64
	for _, r := range runs {
		switch r.Status {
		case jobstore.MemorySynthesisRunning:
			return false
		case jobstore.MemorySynthesisSucceeded, jobstore.MemorySynthesisSkipped:
			started++
		case jobstore.MemorySynthesisFailed:
			lastFail = max(lastFail, r.FinishedAt)
		}
	}
	if limit > 0 && started >= limit {
		return false
	}
	return lastFail == 0 || now.Unix() >= lastFail+int64(retryAfter/time.Second)
}

// hold keeps project/day from being tried again before until, and forgets older days.
func (e *Engine) hold(project, day string, until time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.nextTry == nil {
		e.nextTry = map[string]time.Time{}
	}
	for k := range e.nextTry {
		if len(k) < len(day) || k[len(k)-len(day):] != day {
			delete(e.nextTry, k)
		}
	}
	e.nextTry[project+"/"+day] = until
}
