package today

import (
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/inhere/gofer/internal/i18n"
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

// Decisions builds the ordered card queue.
func (s *Service) Decisions(includeExec bool) ([]Card, error) {
	return s.DecisionsIn("", includeExec)
}

// DecisionsIn is Decisions with the card text in lang ("" = the server's language).
func (s *Service) DecisionsIn(lang i18n.Lang, includeExec bool) ([]Card, error) {
	cards, _, err := s.decisionsIn(lang, includeExec)
	return cards, err
}

// decisionsIn is DecisionsIn plus the recheck_at of the build (0 = none): the moment a
// relay turn held back only by the grace period starts to qualify.
func (s *Service) decisionsIn(lang i18n.Lang, includeExec bool) ([]Card, int64, error) {
	if s == nil || s.d.Store == nil {
		return nil, 0, errors.New("today: no store")
	}
	b := &builder{s: s, lang: s.lang(lang), now: s.d.Now().Unix(), cards: []Card{}, todos: map[string][]jobstore.PlanTodo{}, jobs: map[string]*jobstore.JobRecord{}}
	steps := []func() error{
		b.interactions,
		b.decisions,
		func() error { return b.reviews(includeExec) },
		b.approvals,
		b.mailHeld,
		b.blockedPlans,
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return nil, 0, err
		}
	}
	for i := range b.cards {
		b.cards[i].Urgency = urgencyOf(b.cards[i], b.now)
		if b.cards[i].Actions == nil {
			b.cards[i].Actions = []Action{}
		}
	}
	SortCards(b.cards)
	return b.cards, b.recheckAt, nil
}

type builder struct {
	s     *Service
	lang  i18n.Lang
	now   int64
	cards []Card
	todos map[string][]jobstore.PlanTodo
	jobs  map[string]*jobstore.JobRecord
	// recheckAt is the earliest unix second a grace-held relay turn qualifies (0 = none).
	recheckAt int64
}

func (b *builder) store() *jobstore.Store { return b.s.d.Store }

// blocks starts a blocking-sources accumulator whose text is in the card language.
func (b *builder) blocks() blockSet { return blockSet{lang: b.lang} }

func (b *builder) tr(key string, args ...any) string { return i18n.T(b.lang, "today."+key, args...) }

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
			Key: KindInteraction + ":" + it.JobID + "/" + it.ID, Kind: KindInteraction, Tag: b.tr("tag.interaction"),
			WaitingSince: it.CreatedAt, ExpiresAt: it.ExpiresAt, ActivityAt: it.CreatedAt,
			Summary: oneLine(it.Prompt, summaryRunes),
			Refs:    Refs{JobID: it.JobID, InteractionID: it.ID},
			Subtype: it.Type,
		}
		var tc job.InteractionToolCall
		hasTool := it.ToolCallJSON != "" && json.Unmarshal([]byte(it.ToolCallJSON), &tc) == nil
		if rec != nil {
			c.ProjectKey, c.Agent = rec.ProjectKey, rec.Agent
			c.Refs.PlanID, c.Refs.TodoID = rec.PlanID, rec.TodoID
			if rec.SessionID != "" || rec.Interactive || rec.SessionStateJSON != "" {
				c.Refs.ThreadID = workbench.JobThreadID(rec.ID, rec.SessionID)
			}
		}
		if it.Type == job.InteractionTypePermission {
			c.Tag = b.tr("tag.tool_approval")
			what := b.tr("card.permission_what")
			if hasTool && strings.TrimSpace(tc.Title) != "" {
				what = strings.TrimSpace(tc.Title)
			}
			c.Title = oneLine(strings.TrimSpace(b.tr("card.interaction_title", "agent", c.Agent, "what", what)), titleRunes)
			if hasTool && tc.RawInputSummary != "" && c.Summary == "" {
				c.Summary = oneLine(tc.RawInputSummary, summaryRunes)
			}
		} else if rec != nil {
			c.Title = jobTitle(*rec)
		}
		if c.Title == "" {
			c.Title = oneLine(it.Prompt, titleRunes)
		}
		c.Actions = interactionActions(b.lang, it)
		blk := b.blocks()
		if rec != nil {
			blk.agent(c.Agent, b.now-it.CreatedAt)
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

func interactionActions(lang i18n.Lang, it jobstore.InteractionRecord) []Action {
	var options []job.InteractionOption
	if it.OptionsJSON != "" {
		_ = json.Unmarshal([]byte(it.OptionsJSON), &options)
	}
	if len(options) == 0 && it.Type == job.InteractionTypeConfirmation {
		options = []job.InteractionOption{{Value: "yes", Label: i18n.T(lang, "today.action.confirm")}, {Value: "no", Label: i18n.T(lang, "today.action.cancel")}}
	}
	// No "hand to the steward" action: the only API for that (POST …/punt) marks the
	// interaction needs_human — "leave it for a person" — and the card is already in
	// front of the person.
	out := make([]Action, 0, 3)
	if len(options) == 0 {
		out = append(out, Action{ID: "reply", Label: i18n.T(lang, "today.action.reply"), Style: "primary", NeedsText: true})
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
			Key: KindDecision + ":" + d.ID, Kind: KindDecision, Tag: b.tr("tag.interaction"),
			Title: firstNonEmpty(oneLine(d.Title, titleRunes), oneLine(d.Question, titleRunes)), WaitingSince: d.AskedAt,
			ActivityAt: d.AskedAt, ExpiresAt: deadline(d.AskedAt, d.TimeoutSec), Summary: oneLine(d.Question, summaryRunes),
			Refs: Refs{DecisionID: d.ID, PlanID: d.PlanID},
		}
		blk := b.blocks()
		if d.PlanID != "" {
			c.Tag = b.tr("tag.plan_question")
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
			c.Actions = []Action{{ID: "reply", Label: b.tr("action.reply"), Style: "primary", NeedsText: true}}
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

// PermissionSubtypeQuestion is the Card.Subtype of a terminal permission card that carries an
// AskUserQuestion (an answer is wanted, not an approval).
const PermissionSubtypeQuestion = "question"

// maxPermissionAlways caps the 「总是允许」 buttons (one per permission suggestion).
const maxPermissionAlways = 2

// permission is one pending terminal permission prompt: 允许 / 总是允许（each
// suggestion Claude Code offered）/ 拒绝 / 附原因拒绝.
func (b *builder) permission(d *jobstore.PlanDecision) error {
	det := sessionrelay.ParsePermissionDetail(d.Detail)
	what := firstNonEmpty(oneLine(det.Summary, summaryRunes), oneLine(d.Question, summaryRunes))
	c := Card{
		Key: KindPermission + ":" + d.ID, Kind: KindPermission, Tag: b.tr("tag.permission"),
		WaitingSince: d.AskedAt, ActivityAt: d.AskedAt, ExpiresAt: deadline(d.AskedAt, d.TimeoutSec),
		Title: oneLine(d.Title, titleRunes), Summary: what,
		Refs: Refs{SessionID: d.SessionID, DecisionID: d.ID, ThreadID: "r:" + d.SessionID},
	}
	if len(det.Questions) > 0 {
		// gofer-02fu: an AskUserQuestion relayed to the web. One single-choice question
		// is answered right here (one button per option); anything richer opens the
		// session, whose card has the full form.
		c.Tag = b.tr("tag.question")
		c.Subtype = PermissionSubtypeQuestion
		if q := det.Questions[0]; len(det.Questions) == 1 && !q.MultiSelect {
			for _, o := range q.Options {
				raw, _ := json.Marshal(map[string]string{q.Question: o.Label})
				c.Actions = append(c.Actions, Action{ID: "answer", Label: oneLine(o.Label, 40),
					Value: sessionrelay.QuestionAnswerPrefix + string(raw)})
			}
		}
		return b.addPermissionCard(c, d)
	}
	c.Actions = append(c.Actions, Action{ID: "answer", Label: b.tr("action.allow"), Value: sessionrelay.PermissionAllow, Style: "ok"})
	for i, sg := range det.Suggestions {
		if i >= maxPermissionAlways {
			break
		}
		label := b.tr("action.allow_always")
		if sg.Label != "" {
			label = b.tr("action.allow_always_label", "label", oneLine(sg.Label, 40))
		}
		c.Actions = append(c.Actions, Action{ID: "answer", Label: label,
			Value: sessionrelay.PermissionAlwaysPrefix + strconv.Itoa(i), Style: "ok"})
	}
	c.Actions = append(c.Actions,
		Action{ID: "answer", Label: b.tr("action.deny"), Value: sessionrelay.PermissionDeny, Style: "bad"},
		Action{ID: "deny_note", Label: b.tr("action.deny_note"), NeedsText: true})
	return b.addPermissionCard(c, d)
}

// addPermissionCard fills a permission / question card's session facts and adds it.
func (b *builder) addPermissionCard(c Card, d *jobstore.PlanDecision) error {
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
	blk := b.blocks()
	blk.agent(c.Agent, b.now-d.AskedAt)
	c.Blocks = blk.result()
	b.cards = append(b.cards, c)
	return nil
}

// relay folds a session's open turns into one card (design §2.1: 同会话只出一张).
func (b *builder) relay(sid string, turns []*jobstore.PlanDecision) error {
	sess, ok, err := b.store().GetAgentSession(sid)
	if err != nil {
		return err
	}
	var sp *jobstore.AgentSession
	if ok {
		sp = &sess
	}
	if turns = b.gateRelayTurns(sp, turns); len(turns) == 0 {
		return nil
	}
	sort.SliceStable(turns, func(i, j int) bool { return turns[i].AskedAt < turns[j].AskedAt })
	first := turns[0]
	c := Card{
		Key: KindRelay + ":" + sid, Kind: KindRelay, Tag: b.tr("tag.relay"),
		// ActivityAt is the newest turn: session heartbeats must not wake a snoozed card (T3).
		WaitingSince: first.AskedAt, ActivityAt: turns[len(turns)-1].AskedAt, ExpiresAt: deadline(first.AskedAt, first.TimeoutSec),
		Title: oneLine(first.Title, titleRunes), Summary: oneLine(first.Question, summaryRunes),
		Refs: Refs{SessionID: sid, DecisionID: first.ID, DecisionIDs: turnIDs(turns), ThreadID: "r:" + sid},
		Actions: []Action{
			{ID: "reply", Label: b.tr("action.reply"), Style: "primary", NeedsText: true},
			{ID: "ack", Label: b.tr("action.ack")},
		},
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
	blk := b.blocks()
	blk.agent(c.Agent, b.now-first.AskedAt)
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
			Key: KindReview + ":" + rec.ID, Kind: KindReview, Tag: b.tr("tag.review"), Title: jobTitle(rec),
			ProjectKey: rec.ProjectKey, Agent: rec.Agent, WaitingSince: waited, ActivityAt: rec.UpdatedAt,
			Summary: reviewSummary(rec), Review: reviewFacts(rec),
			Refs: Refs{JobID: rec.ID, PlanID: rec.PlanID, TodoID: rec.TodoID},
			Actions: []Action{
				{ID: "accept", Label: b.tr("action.accept"), Style: "ok"},
				{ID: "rerun", Label: b.tr("action.rerun")},
				{ID: "diff", Label: b.tr("action.diff"), Style: "link"},
			},
		}
		if rec.SessionID != "" || rec.Interactive || rec.SessionStateJSON != "" {
			c.Refs.ThreadID = workbench.JobThreadID(rec.ID, rec.SessionID)
		}
		blk := b.blocks()
		if rec.WorktreePath != "" {
			blk.add(1, 1, b.tr("blocks.lock_worktree"))
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
			Key: KindApproval + ":" + rec.ID, Kind: KindApproval, Tag: b.tr("tag.approval"), Title: jobTitle(rec),
			ProjectKey: rec.ProjectKey, Agent: rec.Agent, WaitingSince: rec.StartedAt, ExpiresAt: expires,
			ActivityAt: rec.UpdatedAt, Summary: approvalSummary(hold),
			Approval: &Approval{Reason: hold.Reason, Origin: hold.Origin, Command: hold.Command,
				PromptPreview: hold.PromptPreview, TimeoutSec: hold.TimeoutSec},
			Refs: Refs{JobID: rec.ID, PlanID: rec.PlanID, TodoID: rec.TodoID},
			Actions: []Action{
				{ID: "approve", Label: b.tr("action.approve"), Style: "ok"},
				{ID: "reject", Label: b.tr("action.deny"), Style: "bad", OptionalText: true},
				{ID: "open", Label: b.tr("action.details"), Style: "link"},
			},
		}
		blk := b.blocks()
		// gofer-5wvc: the held job itself is what waits — approving it is the only way
		// it starts, so it outranks the cards that merely suggest something.
		blk.add(scoreApprovalBase, 1, b.tr("blocks.approval_wait"))
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

// mailHeldSummaryRunes bounds the body excerpt in the held-mail card's summary line.
const mailHeldSummaryRunes = 40

// mailHeld adds the one summary card 「信件 N 封待指派」 (design 2026-10-16「二、gofer 信箱」,
// plan M7): letters whose address matched nobody — or whose delivery deadline passed — wait as
// held until somebody readdresses or cancels them. The mailbox page's 待指派 tab is where that
// happens, so the card only counts and points there.
func (b *builder) mailHeld() error {
	rows, err := b.store().ListMail(jobstore.ListMailOpts{States: []string{jobstore.MailHeld}, Limit: jobstore.MailListMaxLimit})
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	// ListMail is newest first: rows[0] is the letter the summary line shows.
	oldest, newest := rows[0].CreatedAt, rows[0].CreatedAt
	for _, m := range rows {
		if m.CreatedAt < oldest {
			oldest = m.CreatedAt
		}
		if m.CreatedAt > newest {
			newest = m.CreatedAt
		}
	}
	b.cards = append(b.cards, Card{
		Key: MailHeldCardKey, Kind: KindMailHeld, Tag: b.tr("tag.mail"),
		Title:        b.tr("card.mail_title", "n", len(rows)),
		WaitingSince: oldest, ActivityAt: newest,
		Summary: oneLine(mailHeldSummary(b.lang, rows[0]), summaryRunes),
		Actions: []Action{{ID: "open", Label: b.tr("action.assign"), Style: "link", Value: MailHeldLink}},
	})
	return nil
}

// mailHeldSummary is the line under the card: who sent the newest held letter, the address it
// could not reach and the head of its body.
func mailHeldSummary(lang i18n.Lang, m jobstore.Mail) string {
	to := m.ToAddress
	if to == "" {
		to = i18n.T(lang, "today.card.mail_no_address")
	}
	return i18n.T(lang, "today.card.mail_summary", "origin", mailOriginText(lang, m), "to", to, "body", oneLine(m.Body, mailHeldSummaryRunes))
}

// mailOriginText names a letter's sender without reaching into internal/mail (today only counts
// letters; the mailbox's own OriginOf answers the same question for its views).
func mailOriginText(lang i18n.Lang, m jobstore.Mail) string {
	switch m.FromKind {
	case jobstore.MailKindSession:
		return i18n.T(lang, "today.card.mail_from_session", "id", shortMailID(m.FromID))
	case jobstore.MailKindJob:
		return i18n.T(lang, "today.card.mail_from_job", "id", shortMailID(m.FromID))
	case jobstore.MailKindCaller:
		if m.FromCaller != "" {
			return i18n.T(lang, "today.card.mail_from_caller", "caller", m.FromCaller)
		}
	}
	return i18n.T(lang, "today.card.mail_from_unknown")
}

// shortMailID shortens a session / job id the way the console shows them.
func shortMailID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
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
			Key: KindPlanBlocked + ":" + p.PlanID, Kind: KindPlanBlocked, Tag: b.tr("tag.plan_blocked"),
			ProjectKey: p.ProjectKey, WaitingSince: p.UpdatedAt, ActivityAt: p.UpdatedAt,
			Refs: Refs{PlanID: p.PlanID, TodoID: p.BlockedTodo},
			Actions: []Action{
				{ID: "resume", Label: b.tr("action.resume"), Style: "primary"},
				{ID: "open", Label: b.tr("action.open_plan"), Style: "link"},
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
		c.Summary = oneLine(firstNonEmpty(summary, b.tr("card.plan_stuck")), summaryRunes)
		blk := b.blocks()
		if err := b.addSuccessors(&blk, p.PlanID, p.BlockedTodo); err != nil {
			return err
		}
		c.Blocks = blk.result()
		b.cards = append(b.cards, c)
	}
	return nil
}

// ---------------------------------------------------------------- helpers

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
