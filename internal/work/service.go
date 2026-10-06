// Package work is the work-item domain service (W1, design 2026-10-05): the layer
// above terminal sessions that records "what the human is doing", derives its status
// from the sessions, tracks reminders and produces the daily digest. It depends only on
// jobstore/config/notify; the entry layers (httpapi, mcpserver, commands) consume it
// and never the other way round (G022).
package work

import (
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/notify"
)

// Notifier is the outbound seam (job.Service implements it): the service knows WHEN a
// reminder is due, not about webhooks or config.
type Notifier interface {
	NotifyWork(eventType, projectKey, title, text, link, linkLabel string) int
	WebURL(path string) string
}

// JobState is what the status mapping needs to know about a linked job.
type JobState struct {
	Status             string
	PendingInteraction bool
}

// JobProbe looks up linked jobs' states in one call (job.Service via an adapter).
type JobProbe interface {
	JobStates(ids []string) map[string]JobState
}

const (
	// statusNeedsReview mirrors job.StatusNeedsReview (work must not import job).
	statusNeedsReview = "needs_review"

	// ParkedStaleSec is the "搁置超过 7 天" digest threshold.
	ParkedStaleSec = 7 * 24 * 3600
	// digestWindow keeps a server that comes up late in the day from sending a stale
	// "morning" digest hours after it was due.
	digestWindow = 6 * time.Hour
	// digestKVKey remembers the last date a digest was sent.
	digestKVKey = "digest_last_date"

	syncDebounce = 300 * time.Millisecond
	tickEvery    = 30 * time.Second
)

// Service owns the work-item rules on top of the store.
type Service struct {
	store    *jobstore.Store
	notifier Notifier
	probe    JobProbe
	cfgFn    func() config.WorkConfig
	nowFn    func() time.Time

	dirty chan struct{}

	mu sync.Mutex // serialises Tick (sync + reminders + digest)
}

// New builds the service over the shared job store.
func New(store *jobstore.Store) *Service {
	return &Service{store: store, nowFn: time.Now, dirty: make(chan struct{}, 1)}
}

// SetNotifier injects the notification seam (nil = no notifications).
func (s *Service) SetNotifier(n Notifier) { s.notifier = n }

// SetJobProbe injects the linked-job lookup (nil = jobs never influence the status).
func (s *Service) SetJobProbe(p JobProbe) { s.probe = p }

// SetConfigFn supplies the live work: config block (read on every tick, so a hot
// reload applies to the next one). nil keeps the defaults.
func (s *Service) SetConfigFn(fn func() config.WorkConfig) { s.cfgFn = fn }

// SetNow overrides the clock (tests).
func (s *Service) SetNow(fn func() time.Time) {
	if fn != nil {
		s.nowFn = fn
	}
}

// Store exposes the underlying store for the entry layers' simple reads.
func (s *Service) Store() *jobstore.Store { return s.store }

func (s *Service) cfg() config.WorkConfig {
	if s.cfgFn != nil {
		return s.cfgFn()
	}
	return config.WorkConfig{}
}

// ---------------------------------------------------------------- session hooks

// OnHumanPrompt is called when a HUMAN typed a prompt in a session (the hook's title
// is the prompt's first line): the first one creates the auto draft work item. Errors
// are logged, never returned — the session relay must not fail because of work items.
func (s *Service) OnHumanPrompt(a jobstore.AgentSession, prompt string) {
	if s == nil || s.store == nil {
		return
	}
	w, created, err := s.store.EnsureDraftWorkItemForSession(a, prompt)
	if err != nil {
		slog.Warn("work.draft_failed", "event", "work.draft_failed", "session", a.SessionID, "err", err)
		return
	}
	if created {
		s.SyncItem(w.ID)
	}
}

// MarkDirty asks for a status re-sync of every open work item. It never blocks and
// coalesces bursts: serve calls it for every session / job / decision write.
func (s *Service) MarkDirty() {
	if s == nil {
		return
	}
	select {
	case s.dirty <- struct{}{}:
	default:
	}
}

// Run drives the debounced sync and the periodic Tick until stop closes.
func (s *Service) Run(stop <-chan struct{}) {
	t := time.NewTicker(tickEvery)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-s.dirty:
			select {
			case <-time.After(syncDebounce):
			case <-stop:
				return
			}
			select { // swallow the burst that arrived during the debounce
			case <-s.dirty:
			default:
			}
			s.SyncAll()
		case <-t.C:
			s.Tick(s.nowFn())
		}
	}
}

// ---------------------------------------------------------------- status mapping

// derivedStatus maps the current sessions (and linked jobs) onto a work status.
// ok=false means "no opinion" — an idle / offline / ended session leaves the status
// where it is (design §5: offline only labels the card).
func derivedStatus(sessions []jobstore.AgentSession, jobs map[string]JobState) (status, reason string, ok bool) {
	running := false
	for _, a := range sessions {
		switch a.State {
		case jobstore.SessionWaitingReply:
			return jobstore.WorkNeedsMe, "会话等待回复", true
		case jobstore.SessionNeedsAttention:
			return jobstore.WorkNeedsMe, "会话需要处理（终端对话框）", true
		case jobstore.SessionRunning, jobstore.SessionHandedOff:
			running = true
		}
	}
	for _, j := range jobs {
		if j.PendingInteraction {
			return jobstore.WorkNeedsMe, "关联 job 有待应答交互", true
		}
	}
	for _, j := range jobs {
		if j.Status == statusNeedsReview {
			return jobstore.WorkReview, "关联 job 待验收", true
		}
	}
	if running {
		return jobstore.WorkActive, "会话运行中", true
	}
	return "", "", false
}

// linkedJobIDs gathers the jobs a work item should react to: what its current sessions
// watch plus explicit job links.
func (s *Service) linkedJobIDs(id string, sessionIDs []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(j string) {
		if j != "" && !seen[j] {
			seen[j] = true
			out = append(out, j)
		}
	}
	for _, sid := range sessionIDs {
		if ws, err := s.store.ListSessionJobWatches(sid); err == nil {
			for _, w := range ws {
				add(w.JobID)
			}
		}
	}
	if links, err := s.store.ListWorkLinks(id); err == nil {
		for _, l := range links {
			if l.Kind == jobstore.WorkLinkJob {
				add(l.Ref)
			}
		}
	}
	return out
}

func (s *Service) currentSessions(id string) ([]jobstore.AgentSession, []string, error) {
	rows, err := s.store.ListWorkItemSessions(id)
	if err != nil {
		return nil, nil, err
	}
	var sess []jobstore.AgentSession
	var ids []string
	for _, r := range rows {
		if r.Role != jobstore.WorkSessionCurrent {
			continue
		}
		ids = append(ids, r.SessionID)
		if a, ok, err := s.store.GetAgentSession(r.SessionID); err == nil && ok {
			sess = append(sess, a)
		}
	}
	return sess, ids, nil
}

// SyncItem re-derives one item's status from its sessions. Only an item whose status
// is still owned by `auto` moves (a human's or a report's status wins).
func (s *Service) SyncItem(id string) {
	w, ok, err := s.store.GetWorkItem(id)
	if err != nil || !ok || w.StatusSource != jobstore.WorkSourceAuto || jobstore.WorkStatusFinal(w.Status) || w.MergedInto != "" {
		return
	}
	sess, ids, err := s.currentSessions(id)
	if err != nil {
		return
	}
	var jobs map[string]JobState
	if s.probe != nil {
		if jids := s.linkedJobIDs(id, ids); len(jids) > 0 {
			jobs = s.probe.JobStates(jids)
		}
	}
	status, reason, ok := derivedStatus(sess, jobs)
	if !ok {
		return
	}
	if _, err := s.store.SetWorkItemAutoStatus(id, status, reason); err != nil {
		slog.Warn("work.sync_failed", "event", "work.sync_failed", "id", id, "err", err)
	}
}

// SyncAll re-syncs every open auto-status item.
func (s *Service) SyncAll() {
	items, err := s.store.ListWorkItems(WorkListOptsOpen())
	if err != nil {
		return
	}
	for _, w := range items {
		if w.StatusSource == jobstore.WorkSourceAuto {
			s.SyncItem(w.ID)
		}
	}
}

// WorkListOptsOpen lists every open (not done/dropped/merged) item.
func WorkListOptsOpen() jobstore.WorkListOpts { return jobstore.WorkListOpts{Limit: 2000} }

// ---------------------------------------------------------------- writes

var errEmptyReport = errors.New("report needs at least one of goal, status, blocker, next, summary")

// ErrEmptyReport is returned for a report that says nothing.
var ErrEmptyReport = errEmptyReport

// ReportInput is a session's self-report (`gofer work report`, gofer_work_report).
type ReportInput struct {
	Goal    string
	Status  string
	Blocker string
	Next    string
	Summary string
	// By is the journal author ("session:<sid>", "job:<id>", "human:<caller>").
	By string
}

// Report applies a session's self-report: free-text fields are stored, the status is
// honoured under the human-priority rule (a person's status only yields to an explicit
// `active` — "the blocker is gone"), and one `report` journal line records it.
func (s *Service) Report(id string, in ReportInput) (jobstore.WorkItem, error) {
	in.Goal, in.Status, in.Blocker = strings.TrimSpace(in.Goal), strings.TrimSpace(in.Status), strings.TrimSpace(in.Blocker)
	in.Next, in.Summary = strings.TrimSpace(in.Next), strings.TrimSpace(in.Summary)
	if in.Goal == "" && in.Status == "" && in.Blocker == "" && in.Next == "" && in.Summary == "" {
		return jobstore.WorkItem{}, ErrEmptyReport
	}
	if in.Status != "" && !jobstore.ValidWorkStatus(in.Status) {
		return jobstore.WorkItem{}, fmt.Errorf("%w: invalid status %q", jobstore.ErrWorkInvalid, in.Status)
	}
	cur, ok, err := s.store.GetWorkItem(id)
	if err != nil {
		return jobstore.WorkItem{}, err
	}
	if !ok {
		return jobstore.WorkItem{}, jobstore.ErrWorkItemNotFound
	}
	var p jobstore.WorkItemPatch
	p.Quiet = true
	var notes []string
	if in.Goal != "" {
		p.Goal = &in.Goal
		notes = append(notes, "目标："+in.Goal)
		if cur.Unsorted {
			f := false
			p.Unsorted = &f
		}
	}
	if in.Blocker != "" {
		p.BlockerText = &in.Blocker
		notes = append(notes, "阻塞："+in.Blocker)
	}
	if in.Next != "" {
		p.NextStep = &in.Next
		notes = append(notes, "下一步："+in.Next)
	}
	if in.Summary != "" {
		p.Summary = &in.Summary
		notes = append(notes, "摘要："+in.Summary)
	}
	resync := false
	if in.Status != "" {
		switch {
		case jobstore.WorkStatusFinal(in.Status):
			notes = append(notes, "状态：想标为 "+in.Status+"（完成/放弃由人确认，未采纳）")
		case in.Status == jobstore.WorkActive:
			// An explicit "unblocked": the human lock is released and the session
			// mapping takes over again.
			a := jobstore.WorkSourceAuto
			p.StatusSource = &a
			if cur.Status != jobstore.WorkActive && cur.StatusSource != jobstore.WorkSourceAuto {
				st := jobstore.WorkActive
				p.Status = &st
			}
			notes = append(notes, "状态：进行中（阻塞已解除）")
			resync = true
		case cur.StatusSource == jobstore.WorkSourceHuman:
			notes = append(notes, "状态：想标为 "+in.Status+"（已被人手动设置的状态优先，未采纳）")
		default:
			st, src := in.Status, jobstore.WorkSourceReport
			p.Status, p.StatusSource = &st, &src
			notes = append(notes, "状态："+in.Status)
		}
	}
	by := strings.TrimSpace(in.By)
	if by == "" {
		by = "session"
	}
	w, _, err := s.store.UpdateWorkItem(id, p, 0, by)
	if err != nil {
		return jobstore.WorkItem{}, err
	}
	if _, err := s.store.AppendWorkJournal(id, jobstore.WorkJournalReport, strings.Join(notes, "\n"), by); err != nil {
		return jobstore.WorkItem{}, err
	}
	if resync {
		s.SyncItem(id)
		w, _, _ = s.store.GetWorkItem(id)
	}
	return w, nil
}

// Park parks an item: status parked (a person's status), an optional wake-up time and
// a free-text condition ("到货后继续").
func (s *Service) Park(id string, until int64, note, by string) (jobstore.WorkItem, error) {
	st := jobstore.WorkParked
	p := jobstore.WorkItemPatch{Status: &st, ParkUntil: &until, ParkNote: &note}
	w, _, err := s.store.UpdateWorkItem(id, p, 0, by)
	return w, err
}

// Update applies a patch with the cross-field rules the store does not know: filling
// in the goal of a draft marks it sorted.
func (s *Service) Update(id string, p jobstore.WorkItemPatch, expectedRev int64, by string) (jobstore.WorkItem, error) {
	if p.Goal != nil && strings.TrimSpace(*p.Goal) != "" && p.Unsorted == nil {
		if cur, ok, _ := s.store.GetWorkItem(id); ok && cur.Unsorted {
			f := false
			p.Unsorted = &f
		}
	}
	w, _, err := s.store.UpdateWorkItem(id, p, expectedRev, by)
	if err == nil && p.StatusSource != nil && *p.StatusSource == jobstore.WorkSourceAuto {
		s.SyncItem(id)
		w, _, _ = s.store.GetWorkItem(id)
	}
	return w, err
}

// ---------------------------------------------------------------- views

// SessionBrief is the slice of a session a work card shows.
type SessionBrief struct {
	SessionID   string `json:"session_id"`
	Role        string `json:"role"`
	Agent       string `json:"agent,omitempty"`
	Runner      string `json:"runner,omitempty"`
	ProjectKey  string `json:"project_key,omitempty"`
	Cwd         string `json:"cwd,omitempty"`
	Title       string `json:"title,omitempty"`
	State       string `json:"state,omitempty"`
	RelayMode   string `json:"relay_mode,omitempty"`
	LastMessage string `json:"last_message,omitempty"`
	LastSeenAt  int64  `json:"last_seen_at,omitempty"`
	// Offline is true when the session's process is gone (offline / ended) or the row no
	// longer exists (Missing).
	Offline bool `json:"offline"`
	Missing bool `json:"missing,omitempty"`
}

// ItemView is a work item as the entry layers return it.
type ItemView struct {
	jobstore.WorkItem
	// LastActivityAt shadows the stored value with max(stored, current sessions' last
	// seen), so the card's "last activity" follows the terminals too.
	LastActivityAt int64               `json:"last_activity_at"`
	Due            bool                `json:"due"`
	Sessions       []SessionBrief      `json:"sessions"`
	SessionIDs     []string            `json:"session_ids"`
	SessionOffline bool                `json:"session_offline"`
	Links          []jobstore.WorkLink `json:"links"`
}

// DetailView adds the journal to the item view; Sessions then holds current AND past.
type DetailView struct {
	ItemView
	Journal []jobstore.WorkJournalEntry `json:"journal"`
}

func brief(a jobstore.AgentSession, role string) SessionBrief {
	msg := a.LastMessage
	if r := []rune(msg); len(r) > 300 {
		msg = string(r[:300]) + "…"
	}
	return SessionBrief{
		SessionID: a.SessionID, Role: role, Agent: a.Agent, Runner: a.Runner, ProjectKey: a.ProjectKey,
		Cwd: a.Cwd, Title: a.Title, State: a.State, RelayMode: a.RelayMode, LastMessage: msg, LastSeenAt: a.LastSeenAt,
		Offline: a.State == jobstore.SessionOffline || a.State == jobstore.SessionEnded,
	}
}

func (s *Service) view(w jobstore.WorkItem, now int64, includePast bool) (ItemView, error) {
	rows, err := s.store.ListWorkItemSessions(w.ID)
	if err != nil {
		return ItemView{}, err
	}
	v := ItemView{WorkItem: w, LastActivityAt: w.LastActivityAt, Sessions: []SessionBrief{}, SessionIDs: []string{}}
	curTotal, curOffline := 0, 0
	for _, r := range rows {
		current := r.Role == jobstore.WorkSessionCurrent
		if !current && !includePast {
			continue
		}
		a, ok, err := s.store.GetAgentSession(r.SessionID)
		if err != nil {
			return ItemView{}, err
		}
		var b SessionBrief
		if ok {
			b = brief(a, r.Role)
		} else {
			b = SessionBrief{SessionID: r.SessionID, Role: r.Role, Missing: true, Offline: true}
		}
		if current {
			v.SessionIDs = append(v.SessionIDs, r.SessionID)
			curTotal++
			if b.Offline {
				curOffline++
			}
			if b.LastSeenAt > v.LastActivityAt {
				v.LastActivityAt = b.LastSeenAt
			}
		}
		v.Sessions = append(v.Sessions, b)
	}
	v.SessionOffline = curTotal > 0 && curOffline == curTotal && !jobstore.WorkStatusFinal(w.Status)
	v.Due = !jobstore.WorkStatusFinal(w.Status) && ((w.RemindAt > 0 && w.RemindAt <= now) ||
		(w.Status == jobstore.WorkParked && w.ParkUntil > 0 && w.ParkUntil <= now))
	links, err := s.store.ListWorkLinks(w.ID)
	if err != nil {
		return ItemView{}, err
	}
	v.Links = links
	for i := range v.Links {
		v.Links[i].WorkItemID = ""
	}
	return v, nil
}

// List returns the item views for the filter, most recently active first.
func (s *Service) List(o jobstore.WorkListOpts) ([]ItemView, error) {
	now := s.nowFn().Unix()
	if o.Now == 0 {
		o.Now = now
	}
	items, err := s.store.ListWorkItems(o)
	if err != nil {
		return nil, err
	}
	out := make([]ItemView, 0, len(items))
	for _, w := range items {
		v, err := s.view(w, now, false)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastActivityAt > out[j].LastActivityAt })
	return out, nil
}

// Detail returns one item with its full journal and all sessions.
func (s *Service) Detail(id string, journalLimit int) (DetailView, error) {
	w, ok, err := s.store.GetWorkItem(id)
	if err != nil {
		return DetailView{}, err
	}
	if !ok {
		return DetailView{}, jobstore.ErrWorkItemNotFound
	}
	v, err := s.view(w, s.nowFn().Unix(), true)
	if err != nil {
		return DetailView{}, err
	}
	j, err := s.store.ListWorkJournal(id, journalLimit, 0)
	if err != nil {
		return DetailView{}, err
	}
	return DetailView{ItemView: v, Journal: j}, nil
}

// Summary counts for the page header and the badge.
type Summary struct {
	NeedsMe int `json:"needs_me"`
	Due     int `json:"due"`
	Open    int `json:"open"`
}

// Summarize returns the header counts.
func (s *Service) Summarize() (Summary, error) {
	counts, err := s.store.WorkCounts()
	if err != nil {
		return Summary{}, err
	}
	due, err := s.store.ListWorkItems(jobstore.WorkListOpts{Due: true, Now: s.nowFn().Unix()})
	if err != nil {
		return Summary{}, err
	}
	open := 0
	for st, n := range counts {
		if !jobstore.WorkStatusFinal(st) {
			open += n
		}
	}
	return Summary{NeedsMe: counts[jobstore.WorkNeedsMe], Due: len(due), Open: open}, nil
}

// ---------------------------------------------------------------- tick: reminders + digest

// Tick runs one scan: status sync, due reminders, and the daily digest when it is time.
func (s *Service) Tick(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.SyncAll()
	s.fireReminders(now)
	s.maybeDigest(now)
}

func (s *Service) fireReminders(now time.Time) {
	due, err := s.store.ListDueWorkItems(now.Unix())
	if err != nil {
		slog.Warn("work.remind_scan_failed", "event", "work.remind_scan_failed", "err", err)
		return
	}
	for _, w := range due {
		nowUnix := now.Unix()
		remindDue := w.RemindAt > 0 && w.RemindAt <= nowUnix && w.RemindedAt < w.RemindAt
		parkDue := w.Status == jobstore.WorkParked && w.ParkUntil > 0 && w.ParkUntil <= nowUnix && w.RemindedAt < w.ParkUntil
		// Both can be due at once; announce once and record the LATER deadline so
		// neither fires again.
		reason, at := "到了设置的提醒时间", w.RemindAt
		if parkDue && (!remindDue || w.ParkUntil >= w.RemindAt) {
			reason, at = "搁置到期", w.ParkUntil
		}
		if err := s.store.MarkWorkItemReminded(w.ID, at, "已发送到期提醒（"+reason+"）"); err != nil {
			slog.Warn("work.remind_mark_failed", "event", "work.remind_mark_failed", "id", w.ID, "err", err)
			continue
		}
		if s.notifier == nil {
			continue
		}
		var b strings.Builder
		b.WriteString(reason)
		if w.ParkNote != "" && w.Status == jobstore.WorkParked {
			b.WriteString("\n搁置条件：" + w.ParkNote)
		}
		if w.Goal != "" {
			b.WriteString("\n目标：" + w.Goal)
		}
		if w.BlockerText != "" {
			b.WriteString("\n阻塞：" + w.BlockerText)
		}
		if w.NextStep != "" {
			b.WriteString("\n下一步：" + w.NextStep)
		}
		s.notifier.NotifyWork(notify.EventWorkRemind, w.ProjectKey, "工作项提醒 · "+w.Title, b.String(),
			s.notifier.WebURL("/work?id="+w.ID), "打开工作项")
	}
}

func (s *Service) maybeDigest(now time.Time) {
	c := s.cfg()
	if !c.WorkDigestEnabled() {
		return
	}
	h, m := c.DigestClock()
	todayAt := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
	if now.Before(todayAt) || !now.Before(todayAt.Add(digestWindow)) {
		return
	}
	date := now.Format("2006-01-02")
	if last, _ := s.store.GetWorkKV(digestKVKey); last == date {
		return
	}
	if err := s.store.SetWorkKV(digestKVKey, date); err != nil {
		slog.Warn("work.digest_mark_failed", "event", "work.digest_mark_failed", "err", err)
		return
	}
	s.sendDigest(now)
}

// Digest is the deterministic daily summary, computed from the database only.
type Digest struct {
	Title      string `json:"title"`
	Text       string `json:"text"`
	NeedsMe    int    `json:"needs_me"`
	Waiting    int    `json:"waiting_resource"`
	Onsite     int    `json:"needs_onsite"`
	Review     int    `json:"review"`
	ParkedOver int    `json:"parked_over_7d"`
	Yesterday  int    `json:"yesterday"`
}

// BuildDigest renders the digest for now (no side effects).
func (s *Service) BuildDigest(now time.Time) (Digest, error) {
	items, err := s.store.ListWorkItems(jobstore.WorkListOpts{IncludeClosed: true, Limit: 2000})
	if err != nil {
		return Digest{}, err
	}
	d := Digest{Title: "工作摘要 · " + now.Format("2006-01-02")}
	startToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	startYesterday := startToday.AddDate(0, 0, -1)
	link := func(w jobstore.WorkItem) string {
		title := w.Title
		if u := s.webURL("/work?id=" + w.ID); u != "" {
			return fmt.Sprintf("[%s](%s)", title, u)
		}
		return title
	}
	var parked, yest []jobstore.WorkItem
	for _, w := range items {
		switch w.Status {
		case jobstore.WorkNeedsMe:
			d.NeedsMe++
		case jobstore.WorkWaitingResource:
			d.Waiting++
		case jobstore.WorkNeedsOnsite:
			d.Onsite++
		case jobstore.WorkReview:
			d.Review++
		case jobstore.WorkParked:
			since := w.StatusAt
			if since == 0 {
				since = w.UpdatedAt
			}
			if now.Unix()-since > ParkedStaleSec {
				parked = append(parked, w)
			}
		}
		if w.LastActivityAt >= startYesterday.Unix() && w.LastActivityAt < startToday.Unix() {
			yest = append(yest, w)
		}
	}
	d.ParkedOver, d.Yesterday = len(parked), len(yest)
	sort.SliceStable(parked, func(i, j int) bool { return parked[i].ID < parked[j].ID })
	sort.SliceStable(yest, func(i, j int) bool { return yest[i].ID < yest[j].ID })
	var b strings.Builder
	fmt.Fprintf(&b, "等我 %d · 等资源 %d · 需现场 %d · 待验收 %d", d.NeedsMe, d.Waiting, d.Onsite, d.Review)
	if d.ParkedOver > 0 {
		fmt.Fprintf(&b, "\n\n搁置超过 7 天（%d）：", d.ParkedOver)
		for i, w := range parked {
			if i >= 10 {
				fmt.Fprintf(&b, "\n- …另外 %d 项", len(parked)-i)
				break
			}
			b.WriteString("\n- " + link(w))
		}
	}
	if d.Yesterday > 0 {
		fmt.Fprintf(&b, "\n\n昨日有进展（%d）：", d.Yesterday)
		for i, w := range yest {
			if i >= 10 {
				fmt.Fprintf(&b, "\n- …另外 %d 项", len(yest)-i)
				break
			}
			b.WriteString("\n- " + link(w) + "（" + statusLabel(w.Status) + "）")
		}
	}
	if d.NeedsMe+d.Waiting+d.Onsite+d.Review+d.ParkedOver+d.Yesterday == 0 {
		b.WriteString("\n\n今天没有需要关注的工作项。")
	}
	d.Text = b.String()
	return d, nil
}

func (s *Service) webURL(path string) string {
	if s.notifier == nil {
		return ""
	}
	return s.notifier.WebURL(path)
}

// SendDigest builds and sends the digest now (the manual trigger); it reports how many
// webhooks it was queued for.
func (s *Service) SendDigest(now time.Time) (Digest, int, error) {
	d, err := s.BuildDigest(now)
	if err != nil {
		return Digest{}, 0, err
	}
	n := 0
	if s.notifier != nil {
		n = s.notifier.NotifyWork(notify.EventWorkDigest, "", d.Title, d.Text, s.notifier.WebURL("/work"), "打开工作页")
	}
	return d, n, nil
}

func (s *Service) sendDigest(now time.Time) {
	if _, _, err := s.SendDigest(now); err != nil {
		slog.Warn("work.digest_failed", "event", "work.digest_failed", "err", err)
	}
}

var statusLabels = map[string]string{
	jobstore.WorkActive: "进行中", jobstore.WorkNeedsMe: "等我", jobstore.WorkWaitingResource: "等资源",
	jobstore.WorkNeedsOnsite: "需现场", jobstore.WorkReview: "待验收", jobstore.WorkParked: "已搁置",
	jobstore.WorkDone: "已完成", jobstore.WorkDropped: "已放弃",
}

func statusLabel(st string) string {
	if l, ok := statusLabels[st]; ok {
		return l
	}
	return st
}
