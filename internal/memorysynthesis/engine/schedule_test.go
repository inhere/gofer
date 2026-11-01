package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/memorysynthesis"
)

// fakeOneShot stands in for the model: it answers with a valid synthesis citing refs and
// counts the calls; failNext makes the next calls fail.
type fakeOneShot struct {
	mu       sync.Mutex
	calls    int
	failNext int
	ref      string
}

func (f *fakeOneShot) Check(string) error { return nil }

func (f *fakeOneShot) Run(_ context.Context, _ memorysynthesis.OneShotRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.failNext > 0 {
		f.failNext--
		return "", errors.New("model unavailable")
	}
	return fmt.Sprintf(`{"schema":"daily-memory-synthesis.v1","summary":"shipped","highlights":[{"text":"x","source_refs":["%s"]}],"open_questions":[]}`, f.ref), nil
}

func (f *fakeOneShot) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// clock is an injectable, adjustable server clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) set(t time.Time)     { c.mu.Lock(); c.t = t; c.mu.Unlock() }
func (c *clock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

type tickRig struct {
	t     *testing.T
	store *jobstore.Store
	cfg   *config.Config
	shot  *fakeOneShot
	clk   *clock
	eng   *Engine
	day   string // the business day that holds the seeded material (UTC date)
}

// newTickRig seeds one work item today (UTC) and starts the clock on the next UTC date at
// 08:00, i.e. before the default trigger time (09:15 with no digest time configured:
// 09:00 - 15m), so `day` is the target day of every Tick after the trigger.
func newTickRig(t *testing.T, mutate func(*config.Config)) *tickRig {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Storage:  config.StorageConfig{Root: root},
		Server:   config.ServerConfig{Language: string(i18n.EN)},
		Projects: map[string]config.ProjectConfig{"proj": {HostPath: root}, "other": {HostPath: root}},
		Work:     config.WorkConfig{SummarizerAgent: "codex"},
		Memory:   config.MemoryConfig{Synthesis: config.SynthesisConfig{Enabled: true, Projects: []string{"proj"}}},
	}
	if mutate != nil {
		mutate(cfg)
	}
	st, err := jobstore.Open(filepath.Join(root, "gofer.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	item, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "ship the feature", ProjectKey: "proj", Status: jobstore.WorkParked, By: "human:t"})
	if err != nil {
		t.Fatal(err)
	}
	created := time.Now().UTC()
	r := &tickRig{t: t, store: st, cfg: cfg, shot: &fakeOneShot{ref: "work:" + item.ID}, day: created.Format(memorysynthesis.DayLayout)}
	next := created.AddDate(0, 0, 1)
	r.clk = &clock{t: time.Date(next.Year(), next.Month(), next.Day(), 8, 0, 0, 0, time.UTC)}
	r.eng = r.newEngine(time.UTC)
	return r
}

func (r *tickRig) newEngine(loc *time.Location) *Engine {
	return New(Deps{Store: r.store, Config: func() *config.Config { return r.cfg }, OneShot: r.shot, Now: r.clk.now, Loc: loc})
}

func (r *tickRig) at(h, m int) {
	t := r.clk.now()
	r.clk.set(time.Date(t.Year(), t.Month(), t.Day(), h, m, 0, 0, t.Location()))
}

func (r *tickRig) runs() []jobstore.MemorySynthesisRun {
	rows, err := r.store.ListMemorySynthesisRuns(jobstore.MemorySynthesisRunFilter{Limit: 1000})
	if err != nil {
		r.t.Fatal(err)
	}
	return rows
}

func TestTriggerDay(t *testing.T) {
	t.Parallel()
	utc := time.UTC
	for _, tc := range []struct {
		name string
		now  time.Time
		want string
		ok   bool
	}{
		{"before trigger", time.Date(2026, 10, 30, 9, 14, 59, 0, utc), "", false},
		{"at trigger", time.Date(2026, 10, 30, 9, 15, 0, 0, utc), "2026-10-29", true},
		{"late same date catches up", time.Date(2026, 10, 30, 23, 59, 0, 0, utc), "2026-10-29", true},
		{"month rollover", time.Date(2026, 11, 1, 10, 0, 0, 0, utc), "2026-10-31", true},
		{"year rollover", time.Date(2027, 1, 1, 10, 0, 0, 0, utc), "2026-12-31", true},
		{"midnight is before trigger", time.Date(2026, 10, 30, 0, 0, 0, 0, utc), "", false},
	} {
		day, ok := triggerDay(tc.now, 9, 15)
		if day != tc.want || ok != tc.ok {
			t.Errorf("%s: got (%q,%v) want (%q,%v)", tc.name, day, ok, tc.want, tc.ok)
		}
	}
}

func TestTriggerClockDefaultsToQuarterHourBeforeDigest(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		syn, digest string
		h, m        int
	}{
		{"", "", 8, 45},       // digest default 09:00
		{"", "00:10", 23, 55}, // wraps over midnight
		{"07:30", "09:00", 7, 30},
		{"bad", "10:00", 9, 45}, // malformed falls back like the digest does
	} {
		h, m := config.SynthesisConfig{Time: tc.syn}.TriggerClock(config.WorkConfig{DigestTime: tc.digest})
		if h != tc.h || m != tc.m {
			t.Errorf("time=%q digest=%q: got %02d:%02d want %02d:%02d", tc.syn, tc.digest, h, m, tc.h, tc.m)
		}
	}
}

// Nothing runs before the trigger time; at the trigger the previous day is synthesized once,
// and later ticks (and a "restarted" engine with an empty throttle) do not run it again.
func TestTickRunsOnceAfterTriggerAndNotAgainAfterRestart(t *testing.T) {
	r := newTickRig(t, nil)
	ctx := context.Background()
	if res := r.eng.Tick(ctx); len(res) != 0 || r.shot.count() != 0 {
		t.Fatalf("before trigger: res=%v calls=%d", res, r.shot.count())
	}
	r.at(9, 15)
	res := r.eng.Tick(ctx)
	if len(res) != 1 || res[0].State != memorysynthesis.OutcomeSucceeded || res[0].Day != r.day || res[0].Project != "proj" {
		t.Fatalf("at trigger: %+v (want one succeeded run of %s)", res, r.day)
	}
	if got := r.runs(); len(got) != 1 || got[0].CreatedBy != ScheduledBy || got[0].Day != r.day {
		t.Fatalf("runs=%+v", got)
	}
	r.clk.add(time.Hour)
	if res := r.eng.Tick(ctx); len(res) != 0 {
		t.Fatalf("second tick ran again: %+v", res)
	}
	restarted := r.newEngine(time.UTC)
	r.clk.add(time.Hour)
	if res := restarted.Tick(ctx); len(res) != 0 || r.shot.count() != 1 {
		t.Fatalf("after restart: res=%v calls=%d", res, r.shot.count())
	}
}

// A server that was down at the trigger time catches up later the same date; a date that
// went by entirely is skipped (the next date works on its own previous day only).
func TestTickCatchUpSameDateAndSkipsMissedDates(t *testing.T) {
	r := newTickRig(t, nil)
	ctx := context.Background()
	r.at(23, 30)
	if res := r.eng.Tick(ctx); len(res) != 1 || res[0].State != memorysynthesis.OutcomeSucceeded || res[0].Day != r.day {
		t.Fatalf("catch-up: %+v", res)
	}
	// Two dates later nothing is back-filled: the target is the day before THAT date,
	// which has no material, so the model is not called.
	r2 := newTickRig(t, nil)
	r2.clk.add(48 * time.Hour)
	r2.at(10, 0)
	res := r2.eng.Tick(ctx)
	if len(res) != 1 || res[0].State != memorysynthesis.OutcomeEmpty || res[0].Day == r2.day || r2.shot.count() != 0 {
		t.Fatalf("missed date was back-filled: %+v calls=%d", res, r2.shot.count())
	}
}

func TestTickDisabledAndProjectFilter(t *testing.T) {
	r := newTickRig(t, func(c *config.Config) { c.Memory.Synthesis.Enabled = false })
	r.at(12, 0)
	if res := r.eng.Tick(context.Background()); len(res) != 0 || r.shot.count() != 0 || len(r.runs()) != 0 {
		t.Fatalf("disabled ran: %v calls=%d", res, r.shot.count())
	}
	// Enabled with projects empty = every project; "other" has no material, so only the
	// empty outcome shows for it.
	r = newTickRig(t, func(c *config.Config) { c.Memory.Synthesis.Projects = nil })
	r.at(12, 0)
	res := r.eng.Tick(context.Background())
	states := map[string]string{}
	for _, x := range res {
		states[x.Project] = x.State
	}
	if states["proj"] != memorysynthesis.OutcomeSucceeded || states["other"] != memorysynthesis.OutcomeEmpty || len(states) != 2 {
		t.Fatalf("states=%v", states)
	}
}

// The trigger time and the target day follow the server time zone (Deps.Loc), not UTC.
func TestTickUsesServerTimeZone(t *testing.T) {
	r := newTickRig(t, func(c *config.Config) { c.Memory.Synthesis.Time = "09:15" })
	r.at(20, 0) // 20:00 UTC: past the trigger in UTC ...
	west := r.newEngine(time.FixedZone("west", -12*3600))
	if res := west.Tick(context.Background()); len(res) != 0 { // ... but 08:00 at UTC-12
		t.Fatalf("ran before the local trigger: %+v", res)
	}
	east := r.newEngine(time.FixedZone("east", 12*3600)) // 08:00 next date at UTC+12? 20:00+12h = 08:00 (+1d): before trigger too
	if res := east.Tick(context.Background()); len(res) != 0 {
		t.Fatalf("east ran before the local trigger: %+v", res)
	}
	r.at(22, 0) // UTC+12 is 10:00 of the following date: past the trigger, target = that date minus 1 = the UTC date after r.day
	res := east.Tick(context.Background())
	if len(res) != 1 {
		t.Fatalf("east did not run after its local trigger: %+v", res)
	}
	want := r.clk.now().In(time.FixedZone("east", 12*3600)).AddDate(0, 0, -1).Format(memorysynthesis.DayLayout)
	if res[0].Day != want {
		t.Fatalf("day=%s want %s", res[0].Day, want)
	}
}

// retry_after paces retries of a failed run (failed runs do not use the daily limit);
// once a run succeeded daily_limit stops further runs even when the input changed.
func TestTickRetryAfterAndDailyLimit(t *testing.T) {
	r := newTickRig(t, func(c *config.Config) { c.Memory.Synthesis.RetryAfter = "30m" })
	ctx := context.Background()
	r.shot.failNext = 1
	r.at(9, 15)
	if res := r.eng.Tick(ctx); len(res) != 1 || res[0].State != memorysynthesis.OutcomeFailed {
		t.Fatalf("first: %+v", res)
	}
	r.clk.add(29 * time.Minute)
	if res := r.eng.Tick(ctx); len(res) != 0 || r.shot.count() != 1 {
		t.Fatalf("retried inside retry_after: %v calls=%d", res, r.shot.count())
	}
	// A fresh engine (restart) honours the pause from the stored failure, not from memory.
	if res := r.newEngine(time.UTC).Tick(ctx); len(res) != 0 || r.shot.count() != 1 {
		t.Fatalf("restart ignored the backoff: %v calls=%d", res, r.shot.count())
	}
	r.clk.add(2 * time.Minute)
	if res := r.eng.Tick(ctx); len(res) != 1 || res[0].State != memorysynthesis.OutcomeSucceeded {
		t.Fatalf("retry after pause: %+v", res)
	}
	// New material, but daily_limit (default 1) is used up.
	if _, err := r.store.CreateWorkItem(jobstore.WorkItemInput{Title: "second item", ProjectKey: "proj", Status: jobstore.WorkParked, By: "human:t"}); err != nil {
		t.Fatal(err)
	}
	r.clk.add(time.Hour)
	if res := r.eng.Tick(ctx); len(res) != 0 || r.shot.count() != 2 {
		t.Fatalf("ran past daily_limit: %v calls=%d", res, r.shot.count())
	}
}

func TestTickDailyLimitAllowsChangedInputUpToLimit(t *testing.T) {
	r := newTickRig(t, func(c *config.Config) { c.Memory.Synthesis.DailyLimit = 2; c.Memory.Synthesis.RetryAfter = "1m" })
	ctx := context.Background()
	r.at(9, 15)
	if res := r.eng.Tick(ctx); len(res) != 1 || res[0].State != memorysynthesis.OutcomeSucceeded {
		t.Fatalf("first: %+v", res)
	}
	// Unchanged input: the tick re-reads it and reuses the record, no model call.
	r.clk.add(5 * time.Minute)
	if res := r.eng.Tick(ctx); len(res) != 1 || res[0].State != memorysynthesis.OutcomeReused || r.shot.count() != 1 {
		t.Fatalf("unchanged: %+v calls=%d", res, r.shot.count())
	}
	if _, err := r.store.CreateWorkItem(jobstore.WorkItemInput{Title: "second item", ProjectKey: "proj", Status: jobstore.WorkParked, By: "human:t"}); err != nil {
		t.Fatal(err)
	}
	r.clk.add(5 * time.Minute)
	if res := r.eng.Tick(ctx); len(res) != 1 || res[0].State != memorysynthesis.OutcomeSucceeded || r.shot.count() != 2 {
		t.Fatalf("changed input under the limit: %+v calls=%d", res, r.shot.count())
	}
	r.clk.add(5 * time.Minute)
	if res := r.eng.Tick(ctx); len(res) != 0 {
		t.Fatalf("limit reached but ran: %+v", res)
	}
}

// An explicit run (a person asking) is not held by daily_limit=1 — force works — up to the
// manual ceiling, and daily_limit -1 lifts every cap. Failures never count.
func TestExplicitRunLimits(t *testing.T) {
	r := newTickRig(t, nil)
	ctx := context.Background()
	run := func(force bool) memorysynthesis.Outcome {
		out, err := r.eng.Run(ctx, Request{Project: "proj", Day: r.day, Force: force})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if out := run(false); out.State != memorysynthesis.OutcomeSucceeded {
		t.Fatalf("first: %+v", out)
	}
	if out := run(false); out.State != memorysynthesis.OutcomeReused {
		t.Fatalf("unchanged is reused: %+v", out)
	}
	for i := 1; i < config.DefaultSynthesisManualRuns; i++ {
		if out := run(true); out.State != memorysynthesis.OutcomeSucceeded {
			t.Fatalf("force #%d: %+v", i, out)
		}
	}
	if out := run(true); out.State != memorysynthesis.OutcomeLimited {
		t.Fatalf("force past the manual ceiling: %+v", out)
	}
	// The scheduler sees the day as used up too.
	r.at(10, 0)
	if res := r.eng.Tick(ctx); len(res) != 0 {
		t.Fatalf("tick after explicit runs: %+v", res)
	}
	r.cfg.Memory.Synthesis.DailyLimit = -1
	if out := run(true); out.State != memorysynthesis.OutcomeSucceeded {
		t.Fatalf("unlimited: %+v", out)
	}
}

// A live run of the day is never doubled by the scheduler.
func TestTickSkipsDayWithRunningRecord(t *testing.T) {
	r := newTickRig(t, nil)
	r.at(10, 0)
	if _, outcome, err := r.store.BeginMemorySynthesisIfChanged(jobstore.MemorySynthesisBegin{ID: "ms-live", ProjectKey: "proj", Day: r.day,
		InputDigest: "d", MaterialSchema: "daily-memory-materials.v1", Now: r.clk.now().Unix()}); err != nil || outcome != jobstore.MemorySynthesisStarted {
		t.Fatalf("seed: %v %v", outcome, err)
	}
	if res := r.eng.Tick(context.Background()); len(res) != 0 || r.shot.count() != 0 {
		t.Fatalf("ran beside a live run: %v calls=%d", res, r.shot.count())
	}
}
