package today

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/util"
	"github.com/inhere/gofer/internal/work"
)

// Parallel lanes of the Today page (N3 design 2026-10-09 §3): one row per piece of
// work running in parallel — an open, non-parked work item, or a running / blocked plan
// that no work item holds (a plan linked to a work item folds into that item's lane).
// Everything is read from what the store already knows; nothing here writes.

// Lane kinds.
const (
	LaneWork = "work"
	LanePlan = "plan"
)

// Agent states on a lane.
const (
	AgentRunning       = "running"
	AgentAwaitingInput = "awaiting_input"
	AgentIdle          = "idle"
)

// Progress pip states (a plan's todo nodes).
const (
	PipDone        = "done"
	PipRunning     = "running"
	PipNeedsReview = "needs_review"
	PipFailed      = "failed"
	PipPending     = "pending"
)

// planListLimit bounds the plans scanned for lanes (the store's page ceiling).
const planListLimit = jobstore.PlanListMaxLimit

// LaneAgent is one agent working on a lane: a terminal session or a job.
type LaneAgent struct {
	Agent string `json:"agent"`
	// State is running | awaiting_input | idle.
	State string `json:"state"`
	// Kind is "session" (a terminal relay session) or "job"; Ref is its id.
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

// LanePip is one todo node of a plan, in dependency order.
type LanePip struct {
	TodoID string `json:"todo_id"`
	Title  string `json:"title"`
	// Status is done | running | needs_review | failed | pending.
	Status string `json:"status"`
}

// LaneProgress is a plan's todo pips plus its current step, or (without a plan) the
// latest milestone.
type LaneProgress struct {
	Pips []LanePip `json:"pips,omitempty"`
	// Current is the current step's title (plan) or the latest milestone's text.
	Current   string `json:"current,omitempty"`
	CurrentAt int64  `json:"current_at,omitempty"`
}

// LaneUsage is the token / cost total behind a lane.
type LaneUsage struct {
	TotalTokens int64   `json:"total_tokens"`
	CostUSD     float64 `json:"cost_usd,omitempty"`
}

// LaneLinks are the jump targets of a lane.
type LaneLinks struct {
	WorkItemID string   `json:"work_item_id,omitempty"`
	PlanID     string   `json:"plan_id,omitempty"`
	JobIDs     []string `json:"job_ids,omitempty"`
	SessionIDs []string `json:"session_ids,omitempty"`
}

// Lane is one row of 「并行中」.
type Lane struct {
	// ID is the work item id or the plan id.
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	ProjectKey string `json:"project_key,omitempty"`
	// Status is the work item status, or the plan status (open | blocked).
	Status       string       `json:"status"`
	Agents       []LaneAgent  `json:"agents"`
	Progress     LaneProgress `json:"progress"`
	Health       string       `json:"health"`
	HealthReason string       `json:"health_reason,omitempty"`
	StartedAt    int64        `json:"started_at"`
	ElapsedSec   int64        `json:"elapsed_sec"`
	// ActivityAt is the newest activity seen (the secondary sort key).
	ActivityAt int64      `json:"activity_at"`
	Usage      *LaneUsage `json:"usage,omitempty"`
	Links      LaneLinks  `json:"links"`
}

// LanesSummary is the header line: total lanes, agents running, lanes needing attention
// (health not ok).
type LanesSummary struct {
	Total         int `json:"total"`
	AgentsRunning int `json:"agents_running"`
	Attention     int `json:"attention"`
}

// LanesView is the GET /v1/today/lanes body.
type LanesView struct {
	Lanes       []Lane       `json:"lanes"`
	Summary     LanesSummary `json:"summary"`
	GeneratedAt int64        `json:"generated_at"`
}

// LanesBuilder assembles the lanes from the work service and its store.
type LanesBuilder struct {
	work  *work.Service
	store *jobstore.Store
	now   func() time.Time
}

// NewLanesBuilder builds over the work service (its store is the data source).
func NewLanesBuilder(w *work.Service) *LanesBuilder {
	return &LanesBuilder{work: w, store: w.Store(), now: time.Now}
}

// SetNow overrides the clock (tests).
func (b *LanesBuilder) SetNow(fn func() time.Time) {
	if fn != nil {
		b.now = fn
	}
}

// Build reads every lane, sorts them (blocked > stalled > at_risk > a running agent >
// the rest, then most recent activity) and sums the header.
func (b *LanesBuilder) Build() (LanesView, error) {
	now := b.now().Unix()
	items, err := b.work.List(jobstore.WorkListOpts{Limit: 2000, Now: now})
	if err != nil {
		return LanesView{}, err
	}
	lanes := make([]Lane, 0, len(items))
	folded := map[string]bool{}
	for i := range items {
		v := &items[i]
		for _, p := range v.Plans {
			folded[p.PlanID] = true
		}
		if v.Status == jobstore.WorkParked || jobstore.WorkStatusFinal(v.Status) || v.MergedInto != "" {
			continue
		}
		lanes = append(lanes, b.workLane(v, now))
	}
	plans, err := b.store.ListPlans(jobstore.PlanFilter{
		Statuses: []string{jobstore.PlanOpen, jobstore.PlanBlocked}, Limit: planListLimit,
	})
	if err != nil {
		return LanesView{}, err
	}
	for _, p := range plans {
		if folded[p.PlanID] {
			continue
		}
		if l, ok := b.planLane(p, now); ok {
			lanes = append(lanes, l)
		}
	}
	SortLanes(lanes)
	return LanesView{Lanes: lanes, Summary: Summarize(lanes), GeneratedAt: now}, nil
}

// SortLanes orders lanes worst health first, then a lane with a running agent before
// one without, then the most recent activity, then the id (stable).
func SortLanes(lanes []Lane) {
	sort.SliceStable(lanes, func(i, j int) bool {
		ri, rj := laneRank(lanes[i]), laneRank(lanes[j])
		if ri != rj {
			return ri < rj
		}
		if lanes[i].ActivityAt != lanes[j].ActivityAt {
			return lanes[i].ActivityAt > lanes[j].ActivityAt
		}
		return lanes[i].ID < lanes[j].ID
	})
}

func laneRank(l Lane) int {
	if l.Health != "" && l.Health != work.HealthOK {
		return work.HealthRank(l.Health)
	}
	if hasRunningAgent(l) {
		return work.HealthRank(work.HealthOK)
	}
	return work.HealthRank(work.HealthOK) + 1
}

func hasRunningAgent(l Lane) bool {
	for _, a := range l.Agents {
		if a.State == AgentRunning {
			return true
		}
	}
	return false
}

// Summarize counts the header line.
func Summarize(lanes []Lane) LanesSummary {
	s := LanesSummary{Total: len(lanes)}
	for _, l := range lanes {
		for _, a := range l.Agents {
			if a.State == AgentRunning {
				s.AgentsRunning++
			}
		}
		if l.Health != "" && l.Health != work.HealthOK {
			s.Attention++
		}
	}
	return s
}

// ---------------------------------------------------------------- work lanes

func (b *LanesBuilder) workLane(v *work.ItemView, now int64) Lane {
	l := Lane{
		ID: v.ID, Kind: LaneWork, Title: v.Title, ProjectKey: v.ProjectKey, Status: v.Status,
		Agents: []LaneAgent{}, Health: v.Health, HealthReason: v.HealthReason,
		StartedAt: v.CreatedAt, ActivityAt: v.LastActivityAt,
		Links: LaneLinks{WorkItemID: v.ID, SessionIDs: append([]string(nil), v.SessionIDs...)},
	}
	if l.Health == "" {
		l.Health = work.HealthOK
	}
	l.ElapsedSec = max(now-l.StartedAt, 0)
	seen := map[string]bool{}
	for _, s := range v.Sessions {
		if s.Role != jobstore.WorkSessionCurrent || s.Offline || s.Missing {
			continue
		}
		st, ok := sessionAgentState(s)
		if !ok || seen[s.SessionID] {
			continue
		}
		seen[s.SessionID] = true
		kind := "session"
		if s.Kind == work.KindJob {
			kind = "job"
		}
		l.Agents = append(l.Agents, LaneAgent{Agent: s.Agent, State: st, Kind: kind, Ref: s.SessionID})
	}
	planIDs := map[string]bool{}
	for _, p := range v.Plans {
		planIDs[p.PlanID] = true
	}
	var usage runner.Usage
	hasUsage := false
	if v.Usage != nil {
		usage = usage.Plus(*v.Usage)
		hasUsage = true
	}
	for _, j := range v.LinkedJobs {
		if at := work.JobActivityAt(j); at > l.ActivityAt {
			l.ActivityAt = at
		}
		if st, ok := jobAgentState(j.Status); ok && !seen[j.ID] {
			seen[j.ID] = true
			l.Agents = append(l.Agents, LaneAgent{Agent: j.Agent, State: st, Kind: "job", Ref: j.ID})
			l.Links.JobIDs = append(l.Links.JobIDs, j.ID)
		}
		// Plan jobs are counted through the plan's own roll-up below.
		if !planIDs[j.PlanID] {
			if u, ok := jobUsage(j.UsageJSON); ok {
				usage = usage.Plus(u)
				hasUsage = true
			}
		}
	}
	if len(v.LinkedJobs) > 0 && len(l.Links.JobIDs) == 0 {
		l.Links.JobIDs = []string{v.LinkedJobs[0].ID}
	}
	if p, ok := pickPlan(v.Plans); ok {
		l.Links.PlanID = p.PlanID
		l.Progress = b.planProgress(p)
		if p.UpdatedAt > l.ActivityAt {
			l.ActivityAt = p.UpdatedAt
		}
	}
	for _, pid := range sortedKeys(planIDs) {
		if pu, err := b.store.PlanUsage(pid); err == nil && pu.Jobs > 0 {
			usage.TotalTokens += pu.TotalTokens
			usage.CostUSD += pu.CostUSD
			hasUsage = hasUsage || pu.TotalTokens > 0 || pu.CostUSD > 0
		}
	}
	if l.Progress.Current == "" && len(v.Milestones) > 0 {
		m := v.Milestones[len(v.Milestones)-1]
		l.Progress.Current, l.Progress.CurrentAt = firstLine(m.Text), m.At
	}
	if hasUsage {
		l.Usage = &LaneUsage{TotalTokens: usage.TotalTokens, CostUSD: usage.CostUSD}
	}
	return l
}

// pickPlan chooses the plan a work lane shows: a blocked one first, then an open one.
func pickPlan(plans []jobstore.Plan) (jobstore.Plan, bool) {
	for _, want := range []string{jobstore.PlanBlocked, jobstore.PlanOpen} {
		for _, p := range plans {
			if p.Status == want {
				return p, true
			}
		}
	}
	return jobstore.Plan{}, false
}

// sessionAgentState maps a work item's session onto a lane agent state; ok=false for a
// session that is not doing anything worth listing.
func sessionAgentState(s work.SessionBrief) (string, bool) {
	if s.Kind == work.KindJob {
		return jobAgentState(s.State)
	}
	switch s.State {
	case jobstore.SessionRunning, jobstore.SessionHandedOff:
		return AgentRunning, true
	case jobstore.SessionWaitingReply, jobstore.SessionNeedsAttention:
		return AgentAwaitingInput, true
	case jobstore.SessionIdle:
		return AgentIdle, true
	}
	return "", false
}

// jobAgentState maps a job status onto a lane agent state; ok=false for a job that is
// not live (finished, waiting for review…).
func jobAgentState(st string) (string, bool) {
	switch st {
	case "queued", "running", "recovering", "waiting_dir":
		return AgentRunning, true
	case "pending_interaction":
		return AgentAwaitingInput, true
	case "awaiting_input":
		// A persistent session between turns: open, but nobody is working.
		return AgentIdle, true
	}
	return "", false
}

func jobUsage(raw string) (runner.Usage, bool) {
	if strings.TrimSpace(raw) == "" {
		return runner.Usage{}, false
	}
	var u runner.Usage
	if err := json.Unmarshal([]byte(raw), &u); err != nil || (u.TotalTokens == 0 && u.CostUSD == 0) {
		return runner.Usage{}, false
	}
	return u, true
}

// ---------------------------------------------------------------- plan lanes

// planLane builds a lane for a plan no work item holds. An open plan only counts as
// running when it is not paused and its checklist has started but not finished (or a
// job of it is live); a blocked plan always shows.
func (b *LanesBuilder) planLane(p jobstore.Plan, now int64) (Lane, bool) {
	todos, err := b.store.ListTodosByPlan(p.PlanID)
	if err != nil {
		return Lane{}, false
	}
	jobs, _ := b.store.ListJobs(jobstore.ListQuery{Plan: p.PlanID, Limit: 20})
	l := Lane{
		ID: p.PlanID, Kind: LanePlan, Title: p.Title, ProjectKey: p.ProjectKey, Status: p.Status,
		Agents: []LaneAgent{}, StartedAt: p.CreatedAt, ActivityAt: p.UpdatedAt,
		Links: LaneLinks{PlanID: p.PlanID},
	}
	if strings.TrimSpace(l.Title) == "" {
		l.Title = p.PlanID
	}
	live := false
	for _, j := range jobs {
		if at := work.JobActivityAt(j); at > l.ActivityAt {
			l.ActivityAt = at
		}
		if st, ok := jobAgentState(j.Status); ok {
			live = true
			l.Agents = append(l.Agents, LaneAgent{Agent: j.Agent, State: st, Kind: "job", Ref: j.ID})
			l.Links.JobIDs = append(l.Links.JobIDs, j.ID)
		}
	}
	for _, t := range todos {
		if t.UpdatedAt > l.ActivityAt {
			l.ActivityAt = t.UpdatedAt
		}
	}
	if p.Status != jobstore.PlanBlocked && !live && !(planStarted(todos) && !p.Paused) {
		return Lane{}, false
	}
	if len(jobs) > 0 && len(l.Links.JobIDs) == 0 {
		l.Links.JobIDs = []string{jobs[0].ID}
	}
	l.ElapsedSec = max(now-l.StartedAt, 0)
	l.Progress = b.progressOf(p, todos)
	in := work.HealthInput{InProgress: p.Status == jobstore.PlanOpen && !p.Paused, LastActivity: l.ActivityAt, AgentRunning: hasRunningAgent(l)}
	if p.Status == jobstore.PlanBlocked {
		in.BlockedPlan = p.PlanID
		if t := strings.TrimSpace(p.Title); t != "" {
			in.BlockedPlan = "「" + t + "」"
		}
	}
	if len(jobs) > 0 {
		in.LatestJob = &jobs[0]
	}
	l.Health, l.HealthReason = work.ComputeHealth(in, now, b.work.StallAfter())
	if p.Status == jobstore.PlanBlocked && p.BlockedTodo != "" {
		for _, t := range todos {
			if t.TodoID == p.BlockedTodo {
				l.HealthReason = "卡在「" + t.Title + "」"
			}
		}
	}
	if pu, err := b.store.PlanUsage(p.PlanID); err == nil && (pu.TotalTokens > 0 || pu.CostUSD > 0) {
		l.Usage = &LaneUsage{TotalTokens: pu.TotalTokens, CostUSD: pu.CostUSD}
	}
	return l, true
}

// planStarted: something on the checklist moved, and something is still left.
func planStarted(todos []jobstore.PlanTodo) bool {
	started, left := false, false
	for _, t := range todos {
		switch t.Status {
		case jobstore.TodoDone, jobstore.TodoSkipped:
			started = true
		case jobstore.TodoReady, jobstore.TodoDoing:
			started, left = true, true
		default:
			left = true
		}
	}
	return started && left
}

func (b *LanesBuilder) planProgress(p jobstore.Plan) LaneProgress {
	todos, err := b.store.ListTodosByPlan(p.PlanID)
	if err != nil {
		return LaneProgress{}
	}
	return b.progressOf(p, todos)
}

// progressOf renders the todo pips in dependency order and picks the current step: the
// item the plan is blocked on, else the first running, else the first waiting for
// review, else the first pending, else the last one.
func (b *LanesBuilder) progressOf(p jobstore.Plan, todos []jobstore.PlanTodo) LaneProgress {
	ordered := TopoTodos(todos)
	pips := make([]LanePip, 0, len(ordered))
	for _, t := range ordered {
		pips = append(pips, LanePip{TodoID: t.TodoID, Title: t.Title, Status: b.pipStatus(p, t)})
	}
	pg := LaneProgress{Pips: pips}
	pick := func(st string) bool {
		for i, pp := range pips {
			if pp.Status == st {
				pg.Current, pg.CurrentAt = pp.Title, ordered[i].UpdatedAt
				return true
			}
		}
		return false
	}
	if p.BlockedTodo != "" {
		for i, pp := range pips {
			if pp.TodoID == p.BlockedTodo {
				pg.Current, pg.CurrentAt = pp.Title, ordered[i].UpdatedAt
				return pg
			}
		}
	}
	for _, st := range []string{PipFailed, PipRunning, PipNeedsReview, PipPending} {
		if pick(st) {
			return pg
		}
	}
	if n := len(pips); n > 0 {
		pg.Current, pg.CurrentAt = pips[n-1].Title, ordered[n-1].UpdatedAt
	}
	return pg
}

func (b *LanesBuilder) pipStatus(p jobstore.Plan, t jobstore.PlanTodo) string {
	switch t.Status {
	case jobstore.TodoDone, jobstore.TodoSkipped:
		return PipDone
	}
	if p.BlockedTodo == t.TodoID {
		return PipFailed
	}
	if t.JobID != "" {
		if rec, ok, err := b.store.GetJob(t.JobID); err == nil && ok {
			switch rec.Status {
			case "needs_review":
				return PipNeedsReview
			case "failed", "timeout", "rejected":
				return PipFailed
			}
			if _, live := jobAgentState(rec.Status); live {
				return PipRunning
			}
		}
	}
	if t.Status == jobstore.TodoDoing {
		return PipRunning
	}
	return PipPending
}

// TopoTodos orders a plan's todos so every item comes after the items it waits for
// (`after`), keeping the display order among items that are free at the same time.
// Unknown dependencies are ignored; a cycle falls back to display order for the rest.
func TopoTodos(todos []jobstore.PlanTodo) []jobstore.PlanTodo {
	idx := make(map[string]int, len(todos))
	for i, t := range todos {
		idx[t.TodoID] = i
	}
	indeg := make([]int, len(todos))
	next := make([][]int, len(todos))
	for i, t := range todos {
		for _, dep := range t.After {
			if j, ok := idx[dep]; ok && j != i {
				indeg[i]++
				next[j] = append(next[j], i)
			}
		}
	}
	out := make([]jobstore.PlanTodo, 0, len(todos))
	done := make([]bool, len(todos))
	for len(out) < len(todos) {
		picked := -1
		for i := range todos { // the first free item in display order
			if !done[i] && indeg[i] == 0 {
				picked = i
				break
			}
		}
		if picked < 0 { // a cycle: take the first remaining item
			for i := range todos {
				if !done[i] {
					picked = i
					break
				}
			}
		}
		done[picked] = true
		out = append(out, todos[picked])
		for _, n := range next[picked] {
			indeg[n]--
		}
	}
	return out
}

func firstLine(s string) string {
	for _, ln := range strings.Split(s, "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			return ln
		}
	}
	return ""
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, util.CapSum(len(m)))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
