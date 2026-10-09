package overview

import (
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

// 2026-10-08 12:00 UTC = 20:00 at UTC+8 (a Thursday there).
var testNow = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

const tz8 = 480

func p64(v int64) *int64 { return &v }

func TestQueryNormalize(t *testing.T) {
	q, err := Query{}.Normalize()
	assert.NoErr(t, err)
	assert.Eq(t, Range7d, q.Range) // empty range defaults to 7d
	q, err = Query{Range: RangeToday, TZMin: tz8}.Normalize()
	assert.NoErr(t, err)
	assert.Eq(t, RangeToday, q.Range)
	_, err = Query{Range: "90d"}.Normalize()
	assert.True(t, errors.Is(err, ErrInvalidQuery))
	_, err = Query{Range: Range7d, TZMin: 15 * 60}.Normalize()
	assert.True(t, errors.Is(err, ErrInvalidQuery))
}

// TestRangeFromUsesViewerMidnight: 7d = local midnight today minus six days.
func TestRangeFromUsesViewerMidnight(t *testing.T) {
	now := testNow.Unix()
	from := rangeFrom(Range7d, now, tz8*60, 0)
	assert.Eq(t, time.Date(2026, 10, 2, 0, 0, 0, 0, time.FixedZone("", tz8*60)).Unix(), from)
	from = rangeFrom(Range30d, now, 0, 0)
	assert.Eq(t, time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC).Unix(), from)
	first := time.Date(2026, 3, 24, 15, 0, 0, 0, time.UTC).Unix()
	assert.Eq(t, time.Date(2026, 3, 24, 0, 0, 0, 0, time.UTC).Unix(), rangeFrom(RangeAll, now, 0, first))
}

// TestRangeTodayStartsAtViewerMidnight: range=today opens at 00:00 in the viewer's tz,
// not at UTC midnight (testNow is 20:00 at UTC+8, 07:00 at UTC-5).
func TestRangeTodayStartsAtViewerMidnight(t *testing.T) {
	now := testNow.Unix()
	assert.Eq(t, time.Date(2026, 10, 8, 0, 0, 0, 0, time.FixedZone("", tz8*60)).Unix(), rangeFrom(RangeToday, now, tz8*60, 0))
	assert.Eq(t, time.Date(2026, 10, 8, 0, 0, 0, 0, time.FixedZone("", -300*60)).Unix(), rangeFrom(RangeToday, now, -300*60, 0))
	assert.Eq(t, 6, heatWeeks(RangeToday)) // same heatmap width as 7d
	assert.Eq(t, ttlFor(Range7d), ttlFor(RangeToday))
}

// TestAggregateTodayHourly: range=today returns 24 zero-filled local hours; a job is
// bucketed by its viewer-local end hour; the per-day 「best」 fields stay nil and the
// best hour fills in; other ranges carry no hourly series.
func TestAggregateTodayHourly(t *testing.T) {
	now := testNow.Unix() // 20:00 at UTC+8
	loc := time.FixedZone("", tz8*60)
	at := func(h, m int) int64 { return time.Date(2026, 10, 8, h, m, 0, 0, loc).Unix() }
	in := inputs{jobs: []jobstore.OverviewJobRow{
		row("a", "claude", "done", at(0, 10)-60, at(0, 10)),
		row("b", "claude", "done", at(13, 5)-120, at(13, 5)),
		row("c", "codex", "done", at(13, 59)-60, at(13, 59)),
		row("d", "codex", "timeout", at(13, 30)-60, at(13, 30)),
		row("e", "codex", "failed", at(19, 59)-60, at(19, 59)),
	}, doneByDay: map[string]int{"2026-10-08": 3, "2026-10-07": 1}}
	in.jobs[1].CommitsLen = 2
	in.from = rangeFrom(RangeToday, now, tz8*60, 0)
	ov := aggregate(in, Query{Range: RangeToday, TZMin: tz8}, now)

	assert.Len(t, ov.Hourly, 24)
	for i, h := range ov.Hourly {
		assert.Eq(t, i, h.Hour)
	}
	assert.Eq(t, HourRow{Hour: 0, Done: 1, WallSec: 60}, ov.Hourly[0])
	assert.Eq(t, HourRow{Hour: 13, Done: 2, Failed: 1, Commits: 2, WallSec: 240}, ov.Hourly[13])
	assert.Eq(t, HourRow{Hour: 19, Failed: 1, WallSec: 60}, ov.Hourly[19])
	assert.Eq(t, HourRow{Hour: 23}, ov.Hourly[23]) // a future hour: zero-filled
	assert.Eq(t, HourRow{Hour: 12}, ov.Hourly[12])
	assert.Len(t, ov.Daily, 1)
	assert.Eq(t, "2026-10-08", ov.Daily[0].Day)
	assert.Eq(t, 3, ov.Daily[0].Done)

	assert.Nil(t, ov.Best.Weekday)
	assert.Nil(t, ov.Best.Day)
	assert.Nil(t, ov.Best.DailyAvg)
	assert.Eq(t, &BestHour{Hour: 13, Done: 2}, ov.Best.Hour)
	assert.Eq(t, 2, ov.Best.StreakDays)
	assert.Eq(t, 6, ov.Heatmap.Weeks)

	// The same instants seen from UTC: 13:05 at UTC+8 is 05:05 UTC.
	in.from = rangeFrom(RangeToday, now, 0, 0)
	ov = aggregate(in, Query{Range: RangeToday, TZMin: 0}, now)
	assert.Len(t, ov.Hourly, 24)
	assert.Eq(t, 2, ov.Hourly[5].Done)
	assert.Eq(t, 1, ov.Hourly[11].Failed) // 19:59 at UTC+8
	assert.Eq(t, 0, ov.Hourly[0].Done)    // 00:10 at UTC+8 is the previous UTC day

	in.from = rangeFrom(Range7d, now, tz8*60, 0)
	ov = aggregate(in, Query{Range: Range7d, TZMin: tz8}, now)
	assert.Nil(t, ov.Hourly)
	assert.Nil(t, ov.Best.Hour)
}

func row(id, agent, status string, started, ended int64) jobstore.OverviewJobRow {
	return jobstore.OverviewJobRow{ID: id, Project: "p1", Agent: agent, Status: status, StartedAt: started, EndedAt: ended}
}

// TestAggregateJobsAndRates pins the Jobs / time / git / signal口径: success rate leaves
// cancelled and needs_review out; exec is out of the signal denominator; a job without
// metrics adds to wall time but not to active / signal sums; nothing unknown becomes 0.
func TestAggregateJobsAndRates(t *testing.T) {
	now := testNow.Unix()
	day := now - 3600
	jobs := []jobstore.OverviewJobRow{
		row("d1", "claude", "done", day-100, day),
		row("d2", "claude", "done", day-300, day),
		row("f1", "codex", "failed", day-50, day),
		row("t1", "codex", "timeout", day-10, day),
		row("c1", "codex", "cancelled", day-10, day),
		row("r1", "claude", "rejected", day-10, day),
		row("n1", "claude", "needs_review", day-10, day),
		row("x1", "exec", "done", day-1000, day),
	}
	jobs[0].HasMetrics, jobs[0].MTurns, jobs[0].MToolCalls, jobs[0].MHuman = true, p64(4), p64(10), p64(2)
	jobs[0].MActive, jobs[0].MHumanWait = p64(80), p64(20)
	jobs[0].MFiles, jobs[0].MIns, jobs[0].MDel, jobs[0].MCommits = p64(2), p64(30), p64(5), p64(1)
	jobs[1].CommitsLen = 2
	jobs[7].HasMetrics, jobs[7].MTurns, jobs[7].MToolCalls, jobs[7].MHuman = true, p64(0), p64(0), p64(0)
	in := inputs{from: now - 7*86400, jobs: jobs, byStatus: map[string]int{"running": 2, "queued": 1, "needs_review": 1, "done": 9}}

	ov := aggregate(in, Query{Range: Range7d, TZMin: tz8}, now)
	assert.Eq(t, 8, ov.Jobs.Total)
	assert.Eq(t, 3, ov.Jobs.Done)
	assert.Eq(t, 2, ov.Jobs.Failed)
	assert.Eq(t, 1, ov.Jobs.Cancelled)
	assert.Eq(t, 1, ov.Jobs.Rejected)
	assert.Eq(t, 1, ov.Jobs.NeedsReview)
	assert.Eq(t, 3, ov.Jobs.InProgress)
	assert.Eq(t, 11, ov.Totals.Jobs)
	assert.Eq(t, 3.0/6.0, *ov.Jobs.SuccessRate) // done 3 / (3 + failed 1 + timeout 1 + rejected 1)

	assert.Eq(t, int64(100+300+50+10+10+10+10+1000), ov.Time.WallSec)
	assert.Eq(t, int64(30), *ov.Time.MedianSec) // 10 10 10 10 50 100 300 1000: (10+50)/2
	assert.Eq(t, int64(80), *ov.Time.ActiveSec)
	assert.Eq(t, int64(20), *ov.Time.HumanWaitSec)

	assert.Eq(t, int64(3), ov.Git.Commits) // metrics 1 + commit list 2
	assert.Eq(t, 2, ov.Git.JobsWithCommits)
	assert.Eq(t, int64(30), *ov.Git.Insertions)
	assert.Eq(t, 1, ov.Git.GitJobs)

	assert.NotNil(t, ov.Signal)
	assert.Eq(t, 7, ov.Signal.DenominatorJobs) // exec left out
	assert.Eq(t, int64(4), *ov.Signal.Turns)
	assert.Eq(t, int64(10), *ov.Signal.ToolCalls)
	assert.Eq(t, int64(2), ov.Signal.Human)
	assert.Eq(t, 1, ov.Signal.JobsWithHuman)
	assert.Eq(t, 1, ov.Signal.TurnJobs)
	assert.Eq(t, 1.0/7.0, ov.Signal.Coverage)

	assert.Eq(t, "claude", ov.Agents[0].Agent)
	assert.Eq(t, 4, ov.Agents[0].Jobs)
	assert.Eq(t, 2.0/3.0, *ov.Agents[0].SuccessRate) // done 2 / (2 + rejected 1)
	assert.Eq(t, 1, ov.Review.PendingNow)
	assert.Nil(t, ov.Usage)
}

// TestAggregateNoMetricsStaysNull: with no job_metrics at all the signal card and the
// line counts are null (「—」), not zero.
func TestAggregateNoMetricsStaysNull(t *testing.T) {
	now := testNow.Unix()
	in := inputs{from: now - 86400, jobs: []jobstore.OverviewJobRow{row("a", "claude", "done", now-100, now-50)}}
	ov := aggregate(in, Query{Range: Range7d}, now)
	assert.Nil(t, ov.Signal)
	assert.Nil(t, ov.Git.Files)
	assert.Nil(t, ov.Time.ActiveSec)
	assert.Nil(t, ov.Usage)

	empty := aggregate(inputs{from: now - 86400}, Query{Range: Range7d}, now)
	assert.Nil(t, empty.Jobs.SuccessRate)
	assert.Nil(t, empty.Time.AvgSec)
	assert.NotNil(t, empty.Agents)
	assert.NotNil(t, empty.Workload.Longest)
}

// TestAggregateDailyBucketsByViewerDay: a job that ended at 23:30 UTC belongs to the
// NEXT day for a UTC+8 viewer; the series is zero-filled and covers exactly the range.
func TestAggregateDailyBucketsByViewerDay(t *testing.T) {
	now := testNow.Unix()
	late := time.Date(2026, 10, 6, 23, 30, 0, 0, time.UTC).Unix()
	in := inputs{jobs: []jobstore.OverviewJobRow{row("a", "claude", "done", late-60, late), row("b", "claude", "failed", late-60, late)}}
	in.from = rangeFrom(Range7d, now, tz8*60, 0)
	ov := aggregate(in, Query{Range: Range7d, TZMin: tz8}, now)
	assert.Len(t, ov.Daily, 7)
	assert.Eq(t, "2026-10-02", ov.Daily[0].Day)
	assert.Eq(t, "2026-10-08", ov.Daily[6].Day)
	assert.Eq(t, DayRow{Day: "2026-10-07", Done: 1, Failed: 1, WallSec: 120}, ov.Daily[5])

	ov = aggregate(in, Query{Range: Range7d, TZMin: 0}, now)
	assert.Eq(t, 1, ov.Daily[len(ov.Daily)-3].Done) // 2026-10-06 in UTC
}

// TestBestAndHeatmap: best weekday is the highest mean, the streak walks back from
// today (or yesterday when today has nothing yet), the heat levels split the producing
// days of the window.
func TestBestAndHeatmap(t *testing.T) {
	now := testNow.Unix()
	daily := []DayRow{{Day: "2026-10-05", Done: 2}, {Day: "2026-10-06", Done: 9}, {Day: "2026-10-07", Done: 4}, {Day: "2026-10-08", Done: 0}}
	done := map[string]int{"2026-10-05": 2, "2026-10-06": 9, "2026-10-07": 4, "2026-10-03": 1, "2026-01-01": 50}
	b := best(daily, done, Range7d, now, 0)
	assert.Eq(t, 2, b.Weekday.Dow) // 2026-10-06 is a Tuesday
	assert.Eq(t, &BestDay{Day: "2026-10-06", Done: 9}, b.Day)
	assert.Eq(t, 3, b.StreakDays) // today empty: 10-07, 10-06, 10-05; 10-04 breaks
	assert.Nil(t, b.Month)
	assert.Eq(t, 15.0/4.0, *b.DailyAvg)

	b = best(daily, done, RangeAll, now, 0)
	assert.Eq(t, &BestMonth{Month: "2026-10", Done: 15}, b.Month)

	h := heatmap(done, Range7d, now, 0)
	assert.Eq(t, 6, h.Weeks)
	assert.Len(t, h.Days, 4) // 2026-01-01 is outside the six weeks
	assert.Eq(t, []int{2, 4, 9}, h.Levels)
}

// TestReviewAndWorkload: accept / reject rates over the reviewed jobs, the wait from
// ended to reviewed, and the longest / quickest lists (quickest: done, not exec).
func TestReviewAndWorkload(t *testing.T) {
	in := inputs{reviews: []jobstore.OverviewReviewRow{
		{ID: "a", Status: "done", EndedAt: 100, ReviewedAt: 160},
		{ID: "b", Status: "rejected", EndedAt: 100, ReviewedAt: 400, Rerun: true},
		{ID: "c", Status: "rejected", EndedAt: 100, ReviewedAt: 130},
	}, plansDone: 2, todosDone: 5}
	r := review(in, 4)
	assert.Eq(t, 3, r.Reviewed)
	assert.Eq(t, 1, r.Accepted)
	assert.Eq(t, 2, r.Rejected)
	assert.Eq(t, 1, r.Rerun)
	assert.Eq(t, int64((60+300+30)/3), *r.WaitAvgSec)
	assert.Eq(t, int64(60), *r.WaitMedianSec)
	assert.Eq(t, 4, r.PendingNow)

	mk := func(id, agent, status string, wall int64, active *int64) candidate {
		rw := row(id, agent, status, 0, wall)
		rw.MActive = active
		return candidate{row: rw, agent: agent, wall: wall}
	}
	cands := []candidate{
		mk("long", "claude", "failed", 900, p64(100)),
		mk("wallonly", "codex", "done", 500, nil),
		mk("fast", "codex", "done", 50, p64(5)),
		mk("execfast", "exec", "done", 1, p64(1)),
		mk("mid", "claude", "done", 300, p64(200)),
	}
	w := workload(cands, func(ids []string) map[string]string {
		return map[string]string{"long": "a very long title that goes on and on and on and on and on and on and on"}
	})
	assert.Eq(t, []string{"wallonly", "mid", "long"}, briefIDs(w.Longest))
	assert.Eq(t, []string{"fast", "mid", "wallonly"}, briefIDs(w.Quickest))
	assert.Eq(t, 60, len([]rune(w.Longest[2].Title)))
	assert.Nil(t, w.Longest[0].ActiveSec)
}

func briefIDs(bs []JobBrief) []string {
	out := []string{}
	for _, b := range bs {
		out = append(out, b.ID)
	}
	return out
}

// TestUsageMergesJobAndSessionModels: per-model rows merge both sides, a job without a
// model is booked as its agent's default model, and cost per turn / per job divide the
// job spend.
func TestUsageMergesJobAndSessionModels(t *testing.T) {
	now := testNow.Unix()
	a := row("a", "claude", "done", now-100, now-50)
	a.HasUsage, a.UsageCost, a.UsageInput, a.RequestModel = true, 1.0, 100, "m-1"
	a.HasMetrics, a.MTurns = true, p64(4)
	b := row("b", "codex", "done", now-100, now-50)
	b.HasUsage, b.UsageInput = true, 50
	in := inputs{from: now - 86400, jobs: []jobstore.OverviewJobRow{a, b},
		sessions:     []jobstore.SessionModelUsage{{Model: "m-1", CostUSD: 2, OutputTokens: 7}, {Model: "m-2", CostUSD: 0.5}},
		sessionCount: 3}
	ov := aggregate(in, Query{Range: Range7d}, now)
	u := ov.Usage
	assert.NotNil(t, u)
	assert.Eq(t, 3.5, u.CostUSD)
	assert.Eq(t, 1.0, u.JobCostUSD)
	assert.Eq(t, 2.5, u.SessionCostUSD)
	assert.Eq(t, int64(150), u.InputTokens)
	assert.Eq(t, 0.25, *u.PerTurnUSD)
	assert.Eq(t, 0.5, *u.PerJobUSD)
	assert.Eq(t, 3, u.Sessions)
	assert.Eq(t, 3, ov.Totals.Sessions)
	assert.Len(t, u.ByModel, 3)
	assert.Eq(t, ModelRow{Model: "m-1", Source: "job+session", CostUSD: 3, InputTokens: 100, OutputTokens: 7}, u.ByModel[0])
	assert.Eq(t, "session", u.ByModel[1].Source)
	assert.Eq(t, ModelRow{Agent: "codex", Source: "job", InputTokens: 50}, u.ByModel[2])
}

// TestBuildMatchesStore runs the whole read + fold against a real store: the numbers
// equal what the rows say.
func TestBuildMatchesStore(t *testing.T) {
	st, err := jobstore.Open(filepath.Join(t.TempDir(), "gofer.db"))
	assert.NoErr(t, err)
	t.Cleanup(func() { _ = st.Close() })
	now := testNow.Unix()
	put := func(id, status string, started, ended int64, extra func(*jobstore.JobRecord)) {
		rec := jobstore.JobRecord{ID: id, ProjectKey: "p1", Agent: "claude", Runner: "local", Status: status,
			ResultDir: "/r/" + id, StartedAt: started, EndedAt: ended}
		if extra != nil {
			extra(&rec)
		}
		assert.NoErr(t, st.UpsertJob(rec))
	}
	put("in", "done", now-7200, now-3600, func(r *jobstore.JobRecord) {
		r.CommitsJSON = `[{"sha":"a","subject":"x"}]`
		r.UsageJSON = `{"input_tokens":10,"cost_usd":0.2}`
		r.RequestJSON = `{"title":"Ship it","model":"m-x"}`
	})
	put("old", "done", now-90*86400, now-89*86400, nil)
	put("live", "running", now-60, 0, nil)
	put("rev", "rejected", now-5000, now-4000, func(r *jobstore.JobRecord) { r.ReviewedAt = now - 3000 })
	turns := int64(3)
	assert.NoErr(t, st.UpsertJobMetrics(jobstore.JobMetrics{JobID: "in", Turns: &turns}))

	ov, err := Build(st, Query{Range: Range7d}, testNow)
	assert.NoErr(t, err)
	assert.Eq(t, 2, ov.Jobs.Total)
	assert.Eq(t, 1, ov.Jobs.InProgress)
	assert.Eq(t, int64(1), ov.Git.Commits)
	assert.Eq(t, int64(3600+1000), ov.Time.WallSec)
	assert.Eq(t, 1, ov.Review.Rejected)
	assert.Eq(t, "Ship it", ov.Workload.Longest[0].Title)
	assert.Eq(t, "m-x", ov.Usage.ByModel[0].Model)
	assert.Eq(t, int64(3), *ov.Signal.Turns)
	assert.Eq(t, now-90*86400, ov.Range.FirstJobAt)

	all, err := Build(st, Query{Range: RangeAll}, testNow)
	assert.NoErr(t, err)
	assert.Eq(t, 3, all.Jobs.Total)
	assert.True(t, len(all.Daily) >= 90)
	assert.NotNil(t, all.Best.Month)
}

// fakeGen is a settable invalidation counter.
type fakeGen struct{ n atomic.Uint64 }

func (g *fakeGen) StatsGen() uint64 { return g.n.Load() }

func newCachedService(build func(Query, time.Time) (*Overview, error)) (*Service, *fakeGen, *time.Time) {
	g := &fakeGen{}
	now := testNow
	s := &Service{gen: g, now: func() time.Time { return now }, build: build,
		cache: map[cacheKey]cacheEntry{}, inflight: map[cacheKey]*call{}}
	return s, g, &now
}

// TestCacheInvalidation: a hit inside the TTL is marked cached; a moved generation
// rebuilds once MinHold has passed (not before); the TTL expires an unchanged entry;
// range=all keeps its entry for TTLAll; keys are per (range, tz).
func TestCacheInvalidation(t *testing.T) {
	var builds atomic.Int32
	s, g, now := newCachedService(func(q Query, at time.Time) (*Overview, error) {
		builds.Add(1)
		return &Overview{GeneratedAt: at.Unix(), Range: RangeInfo{Key: q.Range}}, nil
	})
	get := func(q Query) *Overview {
		ov, err := s.Get(q)
		assert.NoErr(t, err)
		return ov
	}
	q := Query{Range: Range7d, TZMin: tz8}
	assert.False(t, get(q).Cached)
	assert.True(t, get(q).Cached)
	assert.Eq(t, int32(1), builds.Load())

	g.n.Add(1) // a job ended
	*now = now.Add(MinHold / 2)
	assert.True(t, get(q).Cached) // still inside the hold
	*now = now.Add(MinHold)
	assert.False(t, get(q).Cached)
	assert.Eq(t, int32(2), builds.Load())

	*now = now.Add(TTL)
	assert.False(t, get(q).Cached) // TTL expired
	assert.Eq(t, int32(3), builds.Load())

	get(Query{Range: Range7d, TZMin: 0}) // another tz is another key
	assert.Eq(t, int32(4), builds.Load())

	qa := Query{Range: RangeAll}
	get(qa)
	*now = now.Add(2 * TTL)
	assert.True(t, get(qa).Cached)
	*now = now.Add(TTLAll)
	assert.False(t, get(qa).Cached)

	_, err := s.Get(Query{Range: "1y"})
	assert.True(t, errors.Is(err, ErrInvalidQuery))
}

// TestCacheSingleFlight: concurrent misses on one key share a single build; a failed
// build is not cached.
func TestCacheSingleFlight(t *testing.T) {
	var builds atomic.Int32
	release := make(chan struct{})
	s, _, _ := newCachedService(func(q Query, at time.Time) (*Overview, error) {
		builds.Add(1)
		<-release
		return &Overview{}, nil
	})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Get(Query{Range: Range30d})
			assert.NoErr(t, err)
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	assert.Eq(t, int32(1), builds.Load())

	fail, _, _ := newCachedService(func(Query, time.Time) (*Overview, error) {
		builds.Add(1)
		return nil, errors.New("boom")
	})
	_, err := fail.Get(Query{})
	assert.Err(t, err)
	_, err = fail.Get(Query{})
	assert.Err(t, err)
	assert.Eq(t, int32(3), builds.Load())
}
