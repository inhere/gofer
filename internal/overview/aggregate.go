package overview

import (
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/util"
)

const (
	daySec = int64(86400)
	// streakLookbackDays bounds the 「连续有产出」 walk (the streak query's window).
	streakLookbackDays = 400
	// maxDailyDays caps the zero-filled daily series of range=all.
	maxDailyDays = 3700
	// titleMaxRunes truncates a workload title (design: ≤60 字).
	titleMaxRunes = 60
	// workloadN is the size of the longest / quickest lists.
	workloadN = 3
)

// inProgressStatuses are the live states the Jobs card counts as 「进行中」.
var inProgressStatuses = []string{"running", "queued", "waiting_dir", "pending_interaction", "recovering", "awaiting_input"}

// inputs is everything aggregate folds; load reads it, the tests build it by hand.
type inputs struct {
	from, firstJobAt int64
	jobs             []jobstore.OverviewJobRow
	byStatus         map[string]int
	reviews          []jobstore.OverviewReviewRow
	plansDone        int
	todosDone        int
	doneByDay        map[string]int // done per local day, heatmap + streak window
	sessions         []jobstore.SessionModelUsage
	sessionCount     int
	titles           func(ids []string) map[string]string
}

// rangeFrom resolves the window start: local midnight today (range=today), local
// midnight today minus (days-1) days, or the first job for range=all.
func rangeFrom(key string, now int64, tzSec int, firstJobAt int64) int64 {
	today := localMidnight(now, tzSec)
	switch key {
	case RangeToday:
		return today
	case Range7d:
		return today - 6*daySec
	case RangeAll:
		if firstJobAt > 0 && firstJobAt < today {
			return localMidnight(firstJobAt, tzSec)
		}
		return today
	default:
		return today - 29*daySec
	}
}

// localMidnight is the unix time of 00:00 (viewer local) of the day containing t.
func localMidnight(t int64, tzSec int) int64 {
	local := t + int64(tzSec)
	return local - floorMod(local, daySec) - int64(tzSec)
}

func floorMod(a, b int64) int64 {
	m := a % b
	if m < 0 {
		m += b
	}
	return m
}

// localDay renders the viewer-local date of t.
func localDay(t int64, tzSec int) string {
	return time.Unix(t+int64(tzSec), 0).UTC().Format("2006-01-02")
}

// heatWeeks is the heatmap width per range (today shows the same weeks as 7d).
func heatWeeks(key string) int {
	if key == RangeAll {
		return 26
	}
	return 6
}

// heatStart is the Monday 00:00 (local) that opens the heatmap window.
func heatStart(now int64, tzSec, weeks int) int64 {
	today := localMidnight(now, tzSec)
	wd := int64(time.Unix(today+int64(tzSec), 0).UTC().Weekday()) // 0 = Sunday
	sinceMonday := (wd + 6) % 7
	return today - sinceMonday*daySec - int64(weeks-1)*7*daySec
}

// load performs the store reads for one query.
func load(st *jobstore.Store, q Query, now int64) (inputs, error) {
	tzSec := q.TZMin * 60
	var in inputs
	var err error
	if in.firstJobAt, err = st.FirstJobAt(); err != nil {
		return in, err
	}
	in.from = rangeFrom(q.Range, now, tzSec, in.firstJobAt)
	if in.jobs, err = st.OverviewJobs(in.from); err != nil {
		return in, err
	}
	if in.byStatus, err = st.CountJobsByStatus(); err != nil {
		return in, err
	}
	if in.reviews, err = st.OverviewReviews(in.from); err != nil {
		return in, err
	}
	if in.plansDone, in.todosDone, err = st.OverviewPlans(in.from); err != nil {
		return in, err
	}
	dayFrom := heatStart(now, tzSec, heatWeeks(q.Range))
	if streak := localMidnight(now, tzSec) - streakLookbackDays*daySec; streak < dayFrom {
		dayFrom = streak
	}
	if in.doneByDay, err = st.DoneJobsByDay(dayFrom, tzSec); err != nil {
		return in, err
	}
	if in.sessions, in.sessionCount, err = st.OverviewSessionUsage(jobstore.SessionUsageDay(in.from)); err != nil {
		return in, err
	}
	in.titles = func(ids []string) map[string]string {
		m, terr := st.JobTitles(ids)
		if terr != nil {
			return nil
		}
		return m
	}
	return in, nil
}

// effectiveAgent is the agent that did the work: a resume carrier (Agent=exec with a
// resume agent) is attributed to the source agent.
func effectiveAgent(r jobstore.OverviewJobRow) string {
	if r.Agent == "exec" && r.ResumeAgent != "" {
		return r.ResumeAgent
	}
	return r.Agent
}

func isExec(r jobstore.OverviewJobRow) bool { return r.Agent == "exec" && r.ResumeAgent == "" }

func wallOf(r jobstore.OverviewJobRow) int64 {
	if r.StartedAt > 0 && r.EndedAt >= r.StartedAt {
		return r.EndedAt - r.StartedAt
	}
	return 0
}

func commitsOf(r jobstore.OverviewJobRow) int64 {
	if r.MCommits != nil {
		return *r.MCommits
	}
	return r.CommitsLen
}

func ratio(num, den int) *float64 {
	if den <= 0 {
		return nil
	}
	v := float64(num) / float64(den)
	return &v
}

func ptr64(v int64) *int64 { return &v }

func median(vals []int64) *int64 {
	if len(vals) == 0 {
		return nil
	}
	s := append([]int64(nil), vals...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	n := len(s)
	if n%2 == 1 {
		return ptr64(s[n/2])
	}
	return ptr64((s[n/2-1] + s[n/2]) / 2)
}

// successTally counts the success-rate parts of one status.
type successTally struct{ done, fail int }

func (t *successTally) add(status string) {
	switch status {
	case "done":
		t.done++
	case "failed", "timeout", "rejected":
		t.fail++
	}
}

func (t successTally) rate() *float64 { return ratio(t.done, t.done+t.fail) }

// aggregate folds the inputs into the payload (pure; the unit tests pin it).
func aggregate(in inputs, q Query, now int64) *Overview {
	tzSec := q.TZMin * 60
	ov := &Overview{
		Range:       RangeInfo{Key: q.Range, From: in.from, To: now, TZ: q.TZMin, FirstJobAt: in.firstJobAt},
		Notes:       []string{"session_usage_utc_day", "plans_done_by_updated_at"},
		GeneratedAt: now,
		Agents:      []AgentRow{},
		Projects:    []ProjectRow{},
	}

	for _, st := range inProgressStatuses {
		ov.Jobs.InProgress += in.byStatus[st]
	}
	ov.Review.PendingNow = in.byStatus["needs_review"]

	// Day series skeleton.
	dayIdx := map[string]int{}
	for t, n := in.from, 0; t < now && n < maxDailyDays; t, n = t+daySec, n+1 {
		d := localDay(t+daySec/2, tzSec) // mid-day: immune to a DST hour
		if _, dup := dayIdx[d]; dup {
			continue
		}
		dayIdx[d] = len(ov.Daily)
		ov.Daily = append(ov.Daily, DayRow{Day: d})
	}

	// Hour series (range=today): 24 local hours from the viewer's midnight. The tz is
	// a fixed offset, so hour i is [from + i h, from + (i+1) h).
	if q.Range == RangeToday {
		ov.Hourly = make([]HourRow, 24)
		for h := range ov.Hourly {
			ov.Hourly[h].Hour = h
		}
	}

	var (
		total                   successTally
		walls                   []int64
		activeSum, waitSum      int64
		haveTime                bool
		files, ins, del         int64
		turns, tools            int64
		sig                     SignalBlock
		haveSig                 bool
		agents                  = map[string]*agentAcc{}
		projects                = map[string]*ProjectRow{}
		usage                   UsageBlock
		models                  = map[string]*ModelRow{}
		haveJobUsage            bool
		briefs                  []candidate
		agentOrder, projectKeys []string
	)
	for _, r := range in.jobs {
		ov.Jobs.Total++
		switch r.Status {
		case "done":
			ov.Jobs.Done++
		case "failed", "timeout":
			ov.Jobs.Failed++
		case "cancelled":
			ov.Jobs.Cancelled++
		case "rejected":
			ov.Jobs.Rejected++
		case "needs_review":
			ov.Jobs.NeedsReview++
		}
		total.add(r.Status)
		wall := wallOf(r)
		ov.Time.WallSec += wall
		if wall > 0 {
			walls = append(walls, wall)
		}
		commits := commitsOf(r)
		ov.Git.Commits += commits
		if commits > 0 {
			ov.Git.JobsWithCommits++
		}

		if i, ok := dayIdx[localDay(r.EndedAt, tzSec)]; ok {
			row := &ov.Daily[i]
			switch r.Status {
			case "done":
				row.Done++
			case "failed", "timeout":
				row.Failed++
			}
			row.Commits += commits
			row.WallSec += wall
		}
		if ov.Hourly != nil && r.EndedAt >= in.from {
			if h := (r.EndedAt - in.from) / 3600; h < int64(len(ov.Hourly)) {
				row := &ov.Hourly[h]
				switch r.Status {
				case "done":
					row.Done++
				case "failed", "timeout":
					row.Failed++
				}
				row.Commits += commits
				row.WallSec += wall
			}
		}

		if r.MHumanWait != nil && r.MActive != nil {
			haveTime = true
			activeSum += *r.MActive
			waitSum += *r.MHumanWait
		}
		if r.MFiles != nil {
			ov.Git.GitJobs++
			files += *r.MFiles
			ins += deref(r.MIns)
			del += deref(r.MDel)
		}

		agent := effectiveAgent(r)
		if !isExec(r) {
			sig.DenominatorJobs++
			if r.HasMetrics {
				haveSig = true
				if r.MTurns != nil {
					sig.TurnJobs++
					turns += *r.MTurns
				}
				if r.MToolCalls != nil {
					sig.ToolJobs++
					tools += *r.MToolCalls
				}
				if h := deref(r.MHuman); h > 0 {
					sig.Human += h
					sig.JobsWithHuman++
				}
			}
		}

		a := agents[agent]
		if a == nil {
			a = &agentAcc{}
			agents[agent] = a
			agentOrder = append(agentOrder, agent)
		}
		a.jobs++
		a.tally.add(r.Status)
		if wall > 0 {
			a.wall += wall
			a.walled++
		}

		p := projects[r.Project]
		if p == nil {
			p = &ProjectRow{Project: r.Project}
			projects[r.Project] = p
			projectKeys = append(projectKeys, r.Project)
		}
		p.Jobs++
		p.WallSec += wall
		p.Commits += commits

		if r.HasUsage {
			haveJobUsage = true
			usage.JobsWithUsage++
			usage.JobCostUSD += r.UsageCost
			usage.InputTokens += r.UsageInput
			usage.OutputTokens += r.UsageOutput
			usage.CacheReadTokens += r.UsageCache
			model := r.MModel
			if model == "" {
				model = r.RequestModel
			}
			key, row := "m:"+model, ModelRow{Model: model, Source: "job"}
			if model == "" {
				key, row = "a:"+agent, ModelRow{Agent: agent, Source: "job"}
			}
			mr := models[key]
			if mr == nil {
				mr = &row
				models[key] = mr
			}
			mr.CostUSD += r.UsageCost
			mr.InputTokens += r.UsageInput
			mr.OutputTokens += r.UsageOutput
			mr.CacheReadTokens += r.UsageCache
		}

		briefs = append(briefs, candidate{row: r, agent: agent, wall: wall})
	}

	ov.Jobs.SuccessRate = total.rate()
	ov.Totals = Totals{Jobs: ov.Jobs.Total + ov.Jobs.InProgress, Sessions: in.sessionCount, WallSec: ov.Time.WallSec}
	if len(walls) > 0 {
		ov.Time.AvgSec = ptr64(ov.Time.WallSec / int64(len(walls)))
		ov.Time.MedianSec = median(walls)
	}
	if haveTime {
		ov.Time.ActiveSec, ov.Time.HumanWaitSec = ptr64(activeSum), ptr64(waitSum)
	}
	if ov.Git.GitJobs > 0 {
		ov.Git.Files, ov.Git.Insertions, ov.Git.Deletions = ptr64(files), ptr64(ins), ptr64(del)
	}
	if haveSig {
		if sig.TurnJobs > 0 {
			sig.Turns = ptr64(turns)
		}
		if sig.ToolJobs > 0 {
			sig.ToolCalls = ptr64(tools)
		}
		covered := 0
		for _, r := range in.jobs {
			if !isExec(r) && r.HasMetrics {
				covered++
			}
		}
		if sig.DenominatorJobs > 0 {
			sig.Coverage = float64(covered) / float64(sig.DenominatorJobs)
		}
		ov.Signal = &sig
	}

	for _, name := range agentOrder {
		a := agents[name]
		row := AgentRow{Agent: name, Jobs: a.jobs, Done: a.tally.done, SuccessRate: a.tally.rate()}
		if a.walled > 0 {
			row.AvgSec = ptr64(a.wall / int64(a.walled))
		}
		ov.Agents = append(ov.Agents, row)
	}
	sort.SliceStable(ov.Agents, func(i, j int) bool {
		if ov.Agents[i].Jobs != ov.Agents[j].Jobs {
			return ov.Agents[i].Jobs > ov.Agents[j].Jobs
		}
		return ov.Agents[i].Agent < ov.Agents[j].Agent
	})
	for _, k := range projectKeys {
		ov.Projects = append(ov.Projects, *projects[k])
	}
	sort.SliceStable(ov.Projects, func(i, j int) bool {
		if ov.Projects[i].Jobs != ov.Projects[j].Jobs {
			return ov.Projects[i].Jobs > ov.Projects[j].Jobs
		}
		return ov.Projects[i].Project < ov.Projects[j].Project
	})

	if q.Range == RangeToday {
		ov.Best = bestToday(ov.Hourly, in.doneByDay, now, tzSec)
	} else {
		ov.Best = best(ov.Daily, in.doneByDay, q.Range, now, tzSec)
	}
	ov.Heatmap = heatmap(in.doneByDay, q.Range, now, tzSec)
	ov.Review = review(in, ov.Review.PendingNow)
	ov.Workload = workload(briefs, in.titles)
	ov.Usage = usageBlock(usage, haveJobUsage, models, in.sessions, in.sessionCount, turns, sig.TurnJobs > 0, ov.Jobs.Total)
	return ov
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

type agentAcc struct {
	jobs, walled int
	wall         int64
	tally        successTally
}

type candidate struct {
	row   jobstore.OverviewJobRow
	agent string
	wall  int64
}

// key is the workload ranking value: active time when the metrics know it, else wall.
func (c candidate) key() int64 {
	if c.row.MActive != nil {
		return *c.row.MActive
	}
	return c.wall
}

// best computes the 最高产 panel from the range's day series; the streak walks the
// longer done-by-day window back from today (a today without output yet does not
// break it: the walk then starts at yesterday).
func best(daily []DayRow, doneByDay map[string]int, key string, now int64, tzSec int) Best {
	var out Best
	var dowDone, dowDays [7]int
	totalDone := 0
	months := map[string]int{}
	var monthOrder []string
	for _, d := range daily {
		t, err := time.Parse("2006-01-02", d.Day)
		if err != nil {
			continue
		}
		wd := int(t.Weekday())
		dowDone[wd] += d.Done
		dowDays[wd]++
		totalDone += d.Done
		if d.Done > 0 && (out.Day == nil || d.Done > out.Day.Done) {
			out.Day = &BestDay{Day: d.Day, Done: d.Done}
		}
		m := d.Day[:7]
		if _, ok := months[m]; !ok {
			monthOrder = append(monthOrder, m)
		}
		months[m] += d.Done
	}
	bestAvg := -1.0
	for wd := 0; wd < 7; wd++ {
		if dowDays[wd] == 0 {
			continue
		}
		avg := float64(dowDone[wd]) / float64(dowDays[wd])
		if avg > bestAvg {
			bestAvg = avg
			out.Weekday = &BestWeekday{Dow: wd, Avg: avg}
		}
	}
	if out.Weekday != nil && out.Weekday.Avg == 0 {
		out.Weekday = nil
	}
	if key == RangeAll {
		for _, m := range monthOrder {
			if months[m] > 0 && (out.Month == nil || months[m] > out.Month.Done) {
				out.Month = &BestMonth{Month: m, Done: months[m]}
			}
		}
	} else if len(daily) > 0 {
		avg := float64(totalDone) / float64(len(daily))
		out.DailyAvg = &avg
	}

	out.StreakDays = streak(doneByDay, now, tzSec)
	return out
}

// bestToday is the 最高产 panel of range=today: the per-day fields stay nil (a single,
// partial day has no best weekday / day / average); the best hour and the streak fill it.
func bestToday(hourly []HourRow, doneByDay map[string]int, now int64, tzSec int) Best {
	out := Best{StreakDays: streak(doneByDay, now, tzSec)}
	for _, h := range hourly {
		if h.Done > 0 && (out.Hour == nil || h.Done > out.Hour.Done) {
			out.Hour = &BestHour{Hour: h.Hour, Done: h.Done}
		}
	}
	return out
}

// streak counts the consecutive producing days back from today (or from yesterday when
// today has no output yet).
func streak(doneByDay map[string]int, now int64, tzSec int) int {
	n := 0
	t := localMidnight(now, tzSec)
	if doneByDay[localDay(t+daySec/2, tzSec)] == 0 {
		t -= daySec
	}
	for i := 0; i < streakLookbackDays; i++ {
		if doneByDay[localDay(t+daySec/2, tzSec)] == 0 {
			break
		}
		n++
		t -= daySec
	}
	return n
}

// heatmap lists the producing days of the heatmap window and the shade thresholds.
func heatmap(doneByDay map[string]int, key string, now int64, tzSec int) Heatmap {
	weeks := heatWeeks(key)
	start := localDay(heatStart(now, tzSec, weeks)+daySec/2, tzSec)
	today := localDay(now, tzSec)
	out := Heatmap{Weeks: weeks, Levels: []int{}, Days: []HeatDay{}}
	var vals []int
	for d, n := range doneByDay {
		if n <= 0 || d < start || d > today {
			continue
		}
		out.Days = append(out.Days, HeatDay{Day: d, Done: n})
		vals = append(vals, n)
	}
	sort.Slice(out.Days, func(i, j int) bool { return out.Days[i].Day < out.Days[j].Day })
	if len(vals) > 0 {
		sort.Ints(vals)
		q := func(p float64) int { return vals[int(float64(len(vals))*p)] }
		out.Levels = []int{q(0.25), q(0.5), q(0.8)}
	}
	return out
}

// review folds the reviewed jobs: a verdict is the job's status after the ruling.
func review(in inputs, pending int) ReviewBlock {
	out := ReviewBlock{PendingNow: pending, PlansDone: in.plansDone, TodosDone: in.todosDone}
	var waits []int64
	var waitSum int64
	for _, r := range in.reviews {
		switch r.Status {
		case "done":
			out.Accepted++
		case "rejected":
			out.Rejected++
			if r.Rerun {
				out.Rerun++
			}
		default:
			continue
		}
		if r.EndedAt > 0 && r.ReviewedAt >= r.EndedAt {
			w := r.ReviewedAt - r.EndedAt
			waits = append(waits, w)
			waitSum += w
		}
	}
	out.Reviewed = out.Accepted + out.Rejected
	out.AcceptRate = ratio(out.Accepted, out.Reviewed)
	out.RejectRate = ratio(out.Rejected, out.Reviewed)
	if len(waits) > 0 {
		out.WaitAvgSec = ptr64(waitSum / int64(len(waits)))
		out.WaitMedianSec = median(waits)
	}
	return out
}

// workload picks the three longest jobs (any outcome) and the three quickest done,
// non-exec jobs, by active time (wall when the job has no metrics row).
func workload(cands []candidate, titles func([]string) map[string]string) Workload {
	longest := append([]candidate(nil), cands...)
	sort.SliceStable(longest, func(i, j int) bool { return longest[i].key() > longest[j].key() })
	if len(longest) > workloadN {
		longest = longest[:workloadN]
	}
	quick := make([]candidate, 0, util.CapSum(len(cands)))
	for _, c := range cands {
		if c.row.Status == "done" && !isExec(c.row) && c.key() > 0 {
			quick = append(quick, c)
		}
	}
	sort.SliceStable(quick, func(i, j int) bool { return quick[i].key() < quick[j].key() })
	if len(quick) > workloadN {
		quick = quick[:workloadN]
	}
	ids := make([]string, 0, util.CapSum(len(longest), len(quick)))
	for _, c := range longest {
		ids = append(ids, c.row.ID)
	}
	for _, c := range quick {
		ids = append(ids, c.row.ID)
	}
	var names map[string]string
	if titles != nil && len(ids) > 0 {
		names = titles(ids)
	}
	brief := func(c candidate) JobBrief {
		return JobBrief{ID: c.row.ID, Title: truncate(names[c.row.ID], titleMaxRunes), Agent: c.agent,
			Project: c.row.Project, Status: c.row.Status, Turns: c.row.MTurns, ActiveSec: c.row.MActive, WallSec: c.wall}
	}
	out := Workload{Longest: []JobBrief{}, Quickest: []JobBrief{}}
	for _, c := range longest {
		out.Longest = append(out.Longest, brief(c))
	}
	for _, c := range quick {
		out.Quickest = append(out.Quickest, brief(c))
	}
	return out
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// usageBlock merges the job side and the terminal-session side per model.
func usageBlock(u UsageBlock, haveJob bool, models map[string]*ModelRow, sessions []jobstore.SessionModelUsage,
	sessionCount int, turns int64, haveTurns bool, jobs int) *UsageBlock {
	if !haveJob && len(sessions) == 0 {
		return nil
	}
	for _, s := range sessions {
		u.SessionCostUSD += s.CostUSD
		u.InputTokens += s.InputTokens
		u.OutputTokens += s.OutputTokens
		u.CacheReadTokens += s.CacheRead
		key := "m:" + s.Model
		mr := models[key]
		if mr == nil {
			mr = &ModelRow{Model: s.Model, Source: "session"}
			models[key] = mr
		} else if mr.Source == "job" {
			mr.Source = "job+session"
		}
		mr.CostUSD += s.CostUSD
		mr.InputTokens += s.InputTokens
		mr.OutputTokens += s.OutputTokens
		mr.CacheReadTokens += s.CacheRead
	}
	u.Sessions = sessionCount
	u.CostUSD = u.JobCostUSD + u.SessionCostUSD
	if haveTurns && turns > 0 && u.JobsWithUsage > 0 {
		v := u.JobCostUSD / float64(turns)
		u.PerTurnUSD = &v
	}
	if jobs > 0 && u.JobsWithUsage > 0 {
		v := u.JobCostUSD / float64(jobs)
		u.PerJobUSD = &v
	}
	u.ByModel = make([]ModelRow, 0, util.CapSum(len(models)))
	for _, m := range models {
		u.ByModel = append(u.ByModel, *m)
	}
	sort.Slice(u.ByModel, func(i, j int) bool {
		a, b := u.ByModel[i], u.ByModel[j]
		if a.CostUSD != b.CostUSD {
			return a.CostUSD > b.CostUSD
		}
		ta, tb := a.InputTokens+a.OutputTokens+a.CacheReadTokens, b.InputTokens+b.OutputTokens+b.CacheReadTokens
		if ta != tb {
			return ta > tb
		}
		return a.Model+a.Agent < b.Model+b.Agent
	})
	return &u
}
