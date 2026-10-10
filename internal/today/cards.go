package today

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/sessionrelay"
	"github.com/inhere/gofer/internal/workbench"
)

// summaryRunes caps the one-line summary (design §2.1: review ≤120 字).
const (
	summaryRunes = 120
	titleRunes   = 60
)

// suggestionFields are the only tidy-up suggestions the home page lists (§2.1, §9.3);
// the rest stay in Works.
var suggestionFields = []string{jobstore.SuggestStatusHint, jobstore.SuggestGoal}

// Decisions builds the ordered card queue.
func (s *Service) Decisions(includeExec bool) ([]Card, error) {
	if s == nil || s.d.Store == nil {
		return nil, errors.New("today: no store")
	}
	b := &builder{s: s, now: s.d.Now().Unix(), cards: []Card{}, todos: map[string][]jobstore.PlanTodo{}, jobs: map[string]*jobstore.JobRecord{}}
	steps := []func() error{
		b.interactions,
		b.decisions,
		func() error { return b.reviews(includeExec) },
		b.approvals,
		b.work,
		b.suggestions,
		b.merges,
		b.blockedPlans,
		b.memoryCards,
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return nil, err
		}
	}
	for i := range b.cards {
		b.cards[i].Urgency = urgencyOf(b.cards[i], b.now)
		if b.cards[i].Actions == nil {
			b.cards[i].Actions = []Action{}
		}
	}
	SortCards(b.cards)
	return b.cards, nil
}

type builder struct {
	s     *Service
	now   int64
	cards []Card
	// workCards indexes the work cards by item id so a suggestion can fold into one.
	workCards map[string]int
	todos     map[string][]jobstore.PlanTodo
	jobs      map[string]*jobstore.JobRecord
}

func (b *builder) store() *jobstore.Store { return b.s.d.Store }

// job reads (and caches) one job row; nil when it is gone.
func (b *builder) job(id string) (*jobstore.JobRecord, error) {
	if id == "" {
		return nil, nil
	}
	if rec, ok := b.jobs[id]; ok {
		return rec, nil
	}
	rec, ok, err := b.store().GetJob(id)
	if err != nil {
		return nil, err
	}
	if !ok {
		b.jobs[id] = nil
		return nil, nil
	}
	b.jobs[id] = &rec
	return &rec, nil
}

// planTodos reads (and caches) one plan's todos.
func (b *builder) planTodos(planID string) ([]jobstore.PlanTodo, error) {
	if planID == "" {
		return nil, nil
	}
	if t, ok := b.todos[planID]; ok {
		return t, nil
	}
	t, err := b.store().ListTodosByPlan(planID)
	if err != nil {
		return nil, err
	}
	b.todos[planID] = t
	return t, nil
}

// ---------------------------------------------------------------- interactions

func (b *builder) interactions() error {
	pending, err := b.store().ListPendingInteractions()
	if err != nil {
		return err
	}
	for _, it := range pending {
		rec, err := b.job(it.JobID)
		if err != nil {
			return err
		}
		c := Card{
			Key: KindInteraction + ":" + it.JobID + "/" + it.ID, Kind: KindInteraction, Tag: "提问",
			WaitingSince: it.CreatedAt, ExpiresAt: it.ExpiresAt, ActivityAt: it.CreatedAt,
			Summary: oneLine(it.Prompt, summaryRunes),
			Refs:    Refs{JobID: it.JobID, InteractionID: it.ID},
		}
		var tc job.InteractionToolCall
		hasTool := it.ToolCallJSON != "" && json.Unmarshal([]byte(it.ToolCallJSON), &tc) == nil
		if rec != nil {
			c.ProjectKey, c.Agent = rec.ProjectKey, jobAgent(*rec)
			c.Refs.PlanID, c.Refs.TodoID = rec.PlanID, rec.TodoID
			if rec.SessionID != "" || rec.Interactive || rec.SessionStateJSON != "" {
				c.Refs.ThreadID = workbench.JobThreadID(rec.ID, rec.SessionID)
			}
		}
		if it.Type == job.InteractionTypePermission {
			c.Tag = "工具审批"
			what := "授权"
			if hasTool && strings.TrimSpace(tc.Title) != "" {
				what = strings.TrimSpace(tc.Title)
			}
			c.Title = oneLine(strings.TrimSpace(c.Agent+" 请求"+what), titleRunes)
			if hasTool && tc.RawInputSummary != "" && c.Summary == "" {
				c.Summary = oneLine(tc.RawInputSummary, summaryRunes)
			}
		} else if rec != nil {
			c.Title = jobTitle(*rec)
		}
		if c.Title == "" {
			c.Title = oneLine(it.Prompt, titleRunes)
		}
		c.Actions = interactionActions(it)
		blk := blockSet{}
		if rec != nil {
			blk.agent(c.Agent, "会话", b.now-it.CreatedAt)
			blk.locks(*rec)
			if err := b.addSuccessors(&blk, rec.PlanID, rec.TodoID); err != nil {
				return err
			}
		}
		c.Blocks = blk.result()
		b.cards = append(b.cards, c)
	}
	return nil
}

func interactionActions(it jobstore.InteractionRecord) []Action {
	var options []job.InteractionOption
	if it.OptionsJSON != "" {
		_ = json.Unmarshal([]byte(it.OptionsJSON), &options)
	}
	if len(options) == 0 && it.Type == job.InteractionTypeConfirmation {
		options = []job.InteractionOption{{Value: "yes", Label: "确认"}, {Value: "no", Label: "取消"}}
	}
	// No "hand to the steward" action: the only API for that (POST …/punt) marks the
	// interaction needs_human — "leave it for a person" — and the card is already in
	// front of the person.
	out := make([]Action, 0, 3)
	if len(options) == 0 {
		out = append(out, Action{ID: "reply", Label: "回复", Style: "primary", NeedsText: true})
	} else {
		for _, opt := range pickOptions(options, 3) {
			label := opt.Label
			if label == "" {
				label = opt.Value
			}
			out = append(out, Action{ID: "answer", Label: label, Value: opt.Value, Style: optionStyle(opt)})
		}
	}
	return out
}

// pickOptions keeps the most common answers first: allow_once / reject_once of a
// permission request, otherwise the options in their own order.
func pickOptions(options []job.InteractionOption, limit int) []job.InteractionOption {
	ordered := make([]job.InteractionOption, 0, len(options))
	for _, kind := range []string{"allow_once", "reject_once"} {
		for _, o := range options {
			if o.Kind == kind {
				ordered = append(ordered, o)
			}
		}
	}
	for _, o := range options {
		if o.Kind != "allow_once" && o.Kind != "reject_once" {
			ordered = append(ordered, o)
		}
	}
	if len(ordered) > limit {
		ordered = ordered[:limit]
	}
	return ordered
}

func optionStyle(o job.InteractionOption) string {
	switch {
	case strings.HasPrefix(o.Kind, "allow"):
		return "ok"
	case strings.HasPrefix(o.Kind, "reject"):
		return "bad"
	}
	return ""
}

// ---------------------------------------------------------------- decisions / relay

func (b *builder) decisions() error {
	if err := b.store().ExpireDueDecisions(); err != nil {
		return err
	}
	open, err := b.store().ListDecisions(jobstore.DecisionOpen, "")
	if err != nil {
		return err
	}
	relays := map[string][]*jobstore.PlanDecision{}
	var relayOrder []string
	for _, d := range open {
		if d.Kind == jobstore.DecisionKindPermission {
			if d.SessionID != "" {
				if err := b.permission(d); err != nil {
					return err
				}
			}
			continue
		}
		if d.Kind == jobstore.DecisionKindRelay {
			// Read-but-unanswered ("已读") relay turns stay OPEN but are not pending.
			if d.SessionID == "" || d.AckedAt != 0 {
				continue
			}
			if _, ok := relays[d.SessionID]; !ok {
				relayOrder = append(relayOrder, d.SessionID)
			}
			relays[d.SessionID] = append(relays[d.SessionID], d)
			continue
		}
		c := Card{
			Key: KindDecision + ":" + d.ID, Kind: KindDecision, Tag: "提问",
			Title: firstNonEmpty(oneLine(d.Title, titleRunes), oneLine(d.Question, titleRunes)), WaitingSince: d.AskedAt,
			ActivityAt: d.AskedAt, ExpiresAt: deadline(d.AskedAt, d.TimeoutSec), Summary: oneLine(d.Question, summaryRunes),
			Refs: Refs{DecisionID: d.ID, PlanID: d.PlanID},
		}
		blk := blockSet{}
		if d.PlanID != "" {
			c.Tag = "计划提问"
			plan, ok, err := b.store().GetPlan(d.PlanID)
			if err != nil {
				return err
			}
			if ok {
				c.ProjectKey = plan.ProjectKey
				// A plan question has no todo of its own: what waits is the plan's
				// not-yet-started work.
				if err := b.addWaitingTodos(&blk, plan); err != nil {
					return err
				}
			}
		}
		var options []string
		if d.OptionsJSON != "" {
			_ = json.Unmarshal([]byte(d.OptionsJSON), &options)
		}
		if len(options) == 0 {
			c.Actions = []Action{{ID: "reply", Label: "回复", Style: "primary", NeedsText: true}}
		} else {
			for i, opt := range options {
				if i >= 3 {
					break
				}
				c.Actions = append(c.Actions, Action{ID: "answer", Label: opt, Value: opt})
			}
		}
		c.Blocks = blk.result()
		b.cards = append(b.cards, c)
	}
	for _, sid := range relayOrder {
		if err := b.relay(sid, relays[sid]); err != nil {
			return err
		}
	}
	return nil
}

func turnIDs(turns []*jobstore.PlanDecision) []string {
	out := make([]string, 0, len(turns))
	for _, d := range turns {
		out = append(out, d.ID)
	}
	return out
}

// maxPermissionAlways caps the 「总是允许」 buttons (one per permission suggestion).
const maxPermissionAlways = 2

// permission is one pending terminal permission prompt: 允许 / 总是允许（each
// suggestion Claude Code offered）/ 拒绝 / 附原因拒绝.
func (b *builder) permission(d *jobstore.PlanDecision) error {
	det := sessionrelay.ParsePermissionDetail(d.Detail)
	what := firstNonEmpty(oneLine(det.Summary, summaryRunes), oneLine(d.Question, summaryRunes))
	c := Card{
		Key: KindPermission + ":" + d.ID, Kind: KindPermission, Tag: "需要授权",
		WaitingSince: d.AskedAt, ActivityAt: d.AskedAt, ExpiresAt: deadline(d.AskedAt, d.TimeoutSec),
		Title: oneLine(d.Title, titleRunes), Summary: what,
		Refs: Refs{SessionID: d.SessionID, DecisionID: d.ID, ThreadID: "r:" + d.SessionID},
	}
	c.Actions = append(c.Actions, Action{ID: "answer", Label: "允许", Value: sessionrelay.PermissionAllow, Style: "ok"})
	for i, sg := range det.Suggestions {
		if i >= maxPermissionAlways {
			break
		}
		label := "总是允许"
		if sg.Label != "" {
			label += "：" + oneLine(sg.Label, 40)
		}
		c.Actions = append(c.Actions, Action{ID: "answer", Label: label,
			Value: sessionrelay.PermissionAlwaysPrefix + strconv.Itoa(i), Style: "ok"})
	}
	c.Actions = append(c.Actions,
		Action{ID: "answer", Label: "拒绝", Value: sessionrelay.PermissionDeny, Style: "bad"},
		Action{ID: "deny_note", Label: "附原因拒绝", NeedsText: true})
	sess, ok, err := b.store().GetAgentSession(d.SessionID)
	if err != nil {
		return err
	}
	if ok {
		c.ProjectKey, c.Agent = sess.ProjectKey, sess.Agent
		c.Title = firstNonEmpty(oneLine(sess.Title, titleRunes), c.Title, sess.Agent)
	}
	if c.Title == "" {
		c.Title = d.SessionID
	}
	blk := blockSet{}
	blk.agent(c.Agent, "会话", b.now-d.AskedAt)
	c.Blocks = blk.result()
	b.cards = append(b.cards, c)
	return nil
}

// relay folds a session's open turns into one card (design §2.1: 同会话只出一张).
func (b *builder) relay(sid string, turns []*jobstore.PlanDecision) error {
	sort.SliceStable(turns, func(i, j int) bool { return turns[i].AskedAt < turns[j].AskedAt })
	first := turns[0]
	c := Card{
		Key: KindRelay + ":" + sid, Kind: KindRelay, Tag: "会话等回复",
		// ActivityAt is the newest turn: session heartbeats must not wake a snoozed card (T3).
		WaitingSince: first.AskedAt, ActivityAt: turns[len(turns)-1].AskedAt, ExpiresAt: deadline(first.AskedAt, first.TimeoutSec),
		Title: oneLine(first.Title, titleRunes), Summary: oneLine(first.Question, summaryRunes),
		Refs: Refs{SessionID: sid, DecisionID: first.ID, DecisionIDs: turnIDs(turns), ThreadID: "r:" + sid},
		Actions: []Action{
			{ID: "reply", Label: "回复", Style: "primary", NeedsText: true},
			{ID: "ack", Label: "已读"},
		},
	}
	sess, ok, err := b.store().GetAgentSession(sid)
	if err != nil {
		return err
	}
	if ok {
		c.ProjectKey, c.Agent = sess.ProjectKey, sess.Agent
		c.Title = firstNonEmpty(oneLine(sess.Title, titleRunes), c.Title, sess.Agent)
		if msg := oneLine(sess.LastMessage, summaryRunes); msg != "" {
			c.Summary = msg
		}
	}
	if c.Title == "" {
		c.Title = sid
	}
	blk := blockSet{}
	blk.agent(c.Agent, "会话", b.now-first.AskedAt)
	c.Blocks = blk.result()
	b.cards = append(b.cards, c)
	return nil
}

// ---------------------------------------------------------------- review

var diffStatRe = regexp.MustCompile(`(\d+) insertions?\(\+\)|(\d+) deletions?\(-\)`)

func (b *builder) reviews(includeExec bool) error {
	rows, err := b.store().ListJobs(jobstore.ListQuery{Status: job.StatusNeedsReview, Limit: 500})
	if err != nil {
		return err
	}
	for i := range rows {
		rec := rows[i]
		if rec.Agent == "exec" && !includeExec {
			continue
		}
		b.jobs[rec.ID] = &rec
		waited := rec.EndedAt
		if waited == 0 {
			waited = rec.UpdatedAt
		}
		c := Card{
			Key: KindReview + ":" + rec.ID, Kind: KindReview, Tag: "待验收", Title: jobTitle(rec),
			ProjectKey: rec.ProjectKey, Agent: jobAgent(rec), WaitingSince: waited, ActivityAt: rec.UpdatedAt,
			Summary: reviewSummary(rec), Review: reviewFacts(rec),
			Refs: Refs{JobID: rec.ID, PlanID: rec.PlanID, TodoID: rec.TodoID},
			Actions: []Action{
				{ID: "accept", Label: "通过", Style: "ok"},
				{ID: "rerun", Label: "附意见重跑"},
				{ID: "diff", Label: "看 diff", Style: "link"},
			},
		}
		if rec.SessionID != "" || rec.Interactive || rec.SessionStateJSON != "" {
			c.Refs.ThreadID = workbench.JobThreadID(rec.ID, rec.SessionID)
		}
		blk := blockSet{}
		if rec.WorktreePath != "" {
			blk.add(1, 1, "占着 worktree")
		}
		if err := b.addSuccessors(&blk, rec.PlanID, rec.TodoID); err != nil {
			return err
		}
		c.Blocks = blk.result()
		b.cards = append(b.cards, c)
	}
	return nil
}

// ---------------------------------------------------------------- approval

// approvals lists the jobs held for a person's approval (gofer-9b1b). The deadline is
// the hold's expiry — past it the job is cancelled — so a hold about to lapse sorts
// first (urgency now). Not an advised kind: whether to let a job run is not something
// the steward suggests.
func (b *builder) approvals() error {
	rows, err := b.store().ListJobs(jobstore.ListQuery{Status: job.StatusAwaitingApproval, Limit: 500})
	if err != nil {
		return err
	}
	for i := range rows {
		rec := rows[i]
		b.jobs[rec.ID] = &rec
		var hold job.HoldState
		if rec.HoldJSON != "" {
			_ = json.Unmarshal([]byte(rec.HoldJSON), &hold)
		}
		expires := rec.HoldExpiresAt
		if expires == 0 {
			expires = hold.ExpiresAt
		}
		c := Card{
			Key: KindApproval + ":" + rec.ID, Kind: KindApproval, Tag: "待批准", Title: jobTitle(rec),
			ProjectKey: rec.ProjectKey, Agent: jobAgent(rec), WaitingSince: rec.StartedAt, ExpiresAt: expires,
			ActivityAt: rec.UpdatedAt, Summary: approvalSummary(hold),
			Approval: &Approval{Reason: hold.Reason, Origin: hold.Origin, Command: hold.Command,
				PromptPreview: hold.PromptPreview, TimeoutSec: hold.TimeoutSec},
			Refs: Refs{JobID: rec.ID, PlanID: rec.PlanID, TodoID: rec.TodoID},
			Actions: []Action{
				{ID: "approve", Label: "批准", Style: "ok"},
				{ID: "reject", Label: "拒绝", Style: "bad", OptionalText: true},
				{ID: "open", Label: "看详情", Style: "link"},
			},
		}
		blk := blockSet{}
		if err := b.addSuccessors(&blk, rec.PlanID, rec.TodoID); err != nil {
			return err
		}
		c.Blocks = blk.result()
		b.cards = append(b.cards, c)
	}
	return nil
}

// approvalSummary is the one line of an approval card: the reason, then what would run.
func approvalSummary(h job.HoldState) string {
	what := strings.Join(h.Command, " ")
	if what == "" {
		what = oneLine(h.PromptPreview, summaryRunes)
	}
	switch {
	case h.Reason != "" && what != "":
		return capRunes(oneLine(h.Reason, summaryRunes)+" · "+what, summaryRunes)
	case h.Reason != "":
		return oneLine(h.Reason, summaryRunes)
	}
	return capRunes(what, summaryRunes)
}

func reviewFacts(rec jobstore.JobRecord) *Review {
	r := &Review{}
	var commits []json.RawMessage
	if rec.CommitsJSON != "" && json.Unmarshal([]byte(rec.CommitsJSON), &commits) == nil {
		r.Commits = len(commits)
	}
	r.Adds, r.Dels = parseDiffStat(rec.DiffSummary)
	if rec.VerifyJSON != "" {
		var v struct {
			Status string `json:"status"`
		}
		if json.Unmarshal([]byte(rec.VerifyJSON), &v) == nil {
			r.Verify = v.Status
		}
	}
	return r
}

// parseDiffStat reads the totals line of `git diff --stat`.
func parseDiffStat(stat string) (adds, dels int) {
	for _, m := range diffStatRe.FindAllStringSubmatch(stat, -1) {
		if m[1] != "" {
			n, _ := strconv.Atoi(m[1])
			adds += n
		}
		if m[2] != "" {
			n, _ := strconv.Atoi(m[2])
			dels += n
		}
	}
	return adds, dels
}

// reviewSummary is the first paragraph of the job's own report (result.json summary /
// report / message), else its title.
func reviewSummary(rec jobstore.JobRecord) string {
	if rec.ResultJSON != "" {
		var m map[string]any
		if json.Unmarshal([]byte(rec.ResultJSON), &m) == nil {
			for _, k := range []string{"summary", "report", "message", "result"} {
				if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
					return firstParagraph(v, summaryRunes)
				}
			}
		}
	}
	if rec.Error != "" {
		return oneLine(rec.Error, summaryRunes)
	}
	return jobTitle(rec)
}

// ---------------------------------------------------------------- work

func (b *builder) work() error {
	b.workCards = map[string]int{}
	if b.s.d.Work == nil {
		return nil
	}
	items, err := b.s.d.Work.List(jobstore.WorkListOpts{Statuses: []string{jobstore.WorkNeedsMe, jobstore.WorkNeedsOnsite}, Limit: 500})
	if err != nil {
		return err
	}
	due, err := b.s.d.Work.List(jobstore.WorkListOpts{Due: true, Now: b.now, Limit: 500})
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, v := range append(items, due...) {
		if seen[v.ID] {
			continue
		}
		seen[v.ID] = true
		tag := "等我"
		switch {
		case v.Status == jobstore.WorkNeedsOnsite:
			tag = "需现场"
		case v.Status != jobstore.WorkNeedsMe && v.Due:
			tag = "到期"
		}
		waited := v.StatusAt
		if v.Due && v.Status != jobstore.WorkNeedsMe && v.Status != jobstore.WorkNeedsOnsite {
			waited = v.RemindAt
			if v.Status == jobstore.WorkParked && v.ParkUntil > 0 {
				waited = v.ParkUntil
			}
		}
		if waited == 0 {
			waited = v.UpdatedAt
		}
		c := Card{
			Key: KindWork + ":" + v.ID, Kind: KindWork, Tag: tag, Title: oneLine(v.Title, titleRunes),
			// The stored activity (journal / status / links), not ItemView's session-heartbeat
			// shadow: a heartbeat must not wake a snoozed card (T3).
			ProjectKey: v.ProjectKey, WaitingSince: waited, ActivityAt: v.WorkItem.LastActivityAt,
			Summary: oneLine(firstNonEmpty(v.BlockerText, v.Summary, v.NextStep, v.Goal), summaryRunes),
			Refs:    Refs{WorkItemID: v.ID},
			Actions: []Action{
				{ID: "reply", Label: "回复", Style: "primary", NeedsText: true},
				{ID: "report", Label: "请求汇报"},
				{ID: "park", Label: "搁置"},
			},
		}
		online := ""
		for _, sb := range v.Sessions {
			if sb.Role == jobstore.WorkSessionCurrent && !sb.Offline && !sb.Missing {
				online = sb.SessionID
				if c.Agent == "" {
					c.Agent = sb.Agent
				}
				break
			}
		}
		c.Refs.SessionID = online
		blk := blockSet{}
		if v.Status == jobstore.WorkNeedsMe && online != "" {
			blk.add(2, 1, strings.TrimSpace(c.Agent+" 会话在线等你答复"))
		}
		c.Blocks = blk.result()
		b.workCards[v.ID] = len(b.cards)
		b.cards = append(b.cards, c)
	}
	return nil
}

// ---------------------------------------------------------------- suggestions / merges

func (b *builder) suggestions() error {
	rows, err := b.store().ListPendingWorkSuggestionsFor(suggestionFields)
	if err != nil {
		return err
	}
	for _, sg := range rows {
		text := suggestionText(sg)
		if i, ok := b.workCards[sg.WorkItemID]; ok {
			// Same work item: one work card (design §2.1), the suggestion lives in its 「详情」.
			b.cards[i].Suggestions = append(b.cards[i].Suggestions, Suggestion{Field: sg.Field, Value: sg.Value, Text: text})
			continue
		}
		w, ok, err := b.store().GetWorkItem(sg.WorkItemID)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		b.cards = append(b.cards, Card{
			Key: KindSuggestion + ":" + sg.WorkItemID + ":" + sg.Field, Kind: KindSuggestion, Tag: "整理建议",
			Title: oneLine(w.Title, titleRunes), ProjectKey: w.ProjectKey, WaitingSince: sg.At, ActivityAt: sg.At,
			Summary: oneLine(text, summaryRunes), Refs: Refs{WorkItemID: sg.WorkItemID, Field: sg.Field},
			Actions: []Action{{ID: "adopt", Label: "采纳", Style: "ok"}, {ID: "dismiss", Label: "忽略"}},
		})
	}
	return nil
}

func suggestionText(sg jobstore.WorkSuggestion) string {
	switch sg.Field {
	case jobstore.SuggestStatusHint:
		return "建议把状态改为「" + workStatusLabel(sg.Value) + "」"
	case jobstore.SuggestGoal:
		return "建议目标：" + sg.Value
	}
	return sg.Value
}

func (b *builder) merges() error {
	rows, err := b.store().ListWorkMergeSuggestions(jobstore.MergeSuggestPending)
	if err != nil {
		return err
	}
	for _, g := range rows {
		target, tok, err := b.store().GetWorkItem(g.TargetID)
		if err != nil {
			return err
		}
		source, sok, err := b.store().GetWorkItem(g.SourceID)
		if err != nil {
			return err
		}
		if !tok || !sok {
			continue
		}
		b.cards = append(b.cards, Card{
			Key: fmt.Sprintf("%s:%d", KindMerge, g.ID), Kind: KindMerge, Tag: "合并建议",
			Title:      oneLine("把「"+source.Title+"」并入「"+target.Title+"」", titleRunes),
			ProjectKey: target.ProjectKey, WaitingSince: g.At, ActivityAt: g.At,
			Summary: oneLine(firstNonEmpty(g.Reason, "两张工作卡像是同一件事"), summaryRunes),
			Refs:    Refs{WorkItemID: g.TargetID, MergeID: g.ID},
			Actions: []Action{{ID: "adopt", Label: "采纳", Style: "ok"}, {ID: "dismiss", Label: "忽略"}},
		})
	}
	return nil
}

// ---------------------------------------------------------------- blocked plans

func (b *builder) blockedPlans() error {
	plans, err := b.store().ListPlans(jobstore.PlanFilter{Status: jobstore.PlanBlocked})
	if err != nil {
		return err
	}
	for _, p := range plans {
		title := firstNonEmpty(p.Title, p.PlanID)
		c := Card{
			Key: KindPlanBlocked + ":" + p.PlanID, Kind: KindPlanBlocked, Tag: "plan 阻塞",
			ProjectKey: p.ProjectKey, WaitingSince: p.UpdatedAt, ActivityAt: p.UpdatedAt,
			Refs: Refs{PlanID: p.PlanID, TodoID: p.BlockedTodo},
			Actions: []Action{
				{ID: "resume", Label: "继续", Style: "primary"},
				{ID: "open", Label: "打开 plan", Style: "link"},
			},
		}
		summary := ""
		todos, err := b.planTodos(p.PlanID)
		if err != nil {
			return err
		}
		for _, t := range todos {
			if t.TodoID != p.BlockedTodo {
				continue
			}
			title += " · " + firstNonEmpty(t.Title, t.TodoID)
			c.Refs.JobID = t.JobID
			summary = firstNonEmpty(t.DispatchError, t.Note)
			if summary == "" && t.JobID != "" {
				rec, err := b.job(t.JobID)
				if err != nil {
					return err
				}
				if rec != nil {
					summary = firstNonEmpty(rec.Error, rec.Status)
				}
			}
		}
		c.Title = oneLine(title, titleRunes)
		c.Summary = oneLine(firstNonEmpty(summary, "plan 停在失败的一项上，等你决定继续或改 todo"), summaryRunes)
		blk := blockSet{}
		if err := b.addSuccessors(&blk, p.PlanID, p.BlockedTodo); err != nil {
			return err
		}
		c.Blocks = blk.result()
		b.cards = append(b.cards, c)
	}
	return nil
}

// ---------------------------------------------------------------- helpers

func jobAgent(rec jobstore.JobRecord) string {
	if rec.Agent == "exec" && strings.TrimSpace(rec.OriginAgent) != "" {
		return rec.OriginAgent
	}
	return rec.Agent
}

func jobTitle(rec jobstore.JobRecord) string {
	var req job.JobRequest
	_ = json.Unmarshal([]byte(rec.RequestJSON), &req)
	return firstNonEmpty(oneLine(req.Title, titleRunes), oneLine(req.Prompt, titleRunes), rec.ID)
}

func deadline(askedAt, timeoutSec int64) int64 {
	if timeoutSec <= 0 || askedAt <= 0 {
		return 0
	}
	return askedAt + timeoutSec
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// oneLine is the first non-empty line of s, capped at limit runes (with an ellipsis).
func oneLine(s string, limit int) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return capRunes(line, limit)
		}
	}
	return ""
}

// firstParagraph joins the first paragraph's lines, capped at limit runes.
func firstParagraph(s string, limit int) string {
	var parts []string
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			if len(parts) > 0 {
				break
			}
			continue
		}
		parts = append(parts, strings.TrimLeft(line, "#*- "))
	}
	return capRunes(strings.Join(parts, " "), limit)
}

func capRunes(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…"
}

var workStatusLabels = map[string]string{
	jobstore.WorkActive: "进行中", jobstore.WorkNeedsMe: "等我", jobstore.WorkWaitingResource: "等资源",
	jobstore.WorkNeedsOnsite: "需现场", jobstore.WorkReview: "待验收", jobstore.WorkParked: "搁置",
	jobstore.WorkDone: "完成", jobstore.WorkDropped: "放弃",
}

func workStatusLabel(st string) string {
	if l, ok := workStatusLabels[st]; ok {
		return l
	}
	return st
}
