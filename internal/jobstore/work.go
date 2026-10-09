package jobstore

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Work-item statuses (design §5). active/needs_me/review can be derived from the
// sessions (status_source=auto); the rest are set by a person (or a session report).
const (
	WorkActive          = "active"
	WorkNeedsMe         = "needs_me"
	WorkWaitingResource = "waiting_resource"
	WorkNeedsOnsite     = "needs_onsite"
	WorkReview          = "review"
	WorkParked          = "parked"
	WorkDone            = "done"
	WorkDropped         = "dropped"
)

// WorkStatuses lists the valid statuses in board order.
var WorkStatuses = []string{WorkActive, WorkNeedsMe, WorkWaitingResource, WorkNeedsOnsite, WorkReview, WorkParked, WorkDone, WorkDropped}

// ValidWorkStatus reports whether s is a work-item status.
func ValidWorkStatus(s string) bool {
	for _, v := range WorkStatuses {
		if v == s {
			return true
		}
	}
	return false
}

// WorkStatusFinal reports whether s ends the work item.
func WorkStatusFinal(s string) bool { return s == WorkDone || s == WorkDropped }

// Who decided the current status (work_items.status_source). Only `auto` is
// overwritten by the session-derived mapping.
const (
	WorkSourceAuto   = "auto"
	WorkSourceHuman  = "human"
	WorkSourceReport = "report"
)

// Work-item origins (work_items.source).
const (
	WorkOriginAuto    = "auto"
	WorkOriginHuman   = "human"
	WorkOriginSteward = "steward"
)

// Journal kinds.
const (
	WorkJournalReport  = "report"
	WorkJournalNote    = "note"
	WorkJournalStatus  = "status"
	WorkJournalSteward = "steward"
	WorkJournalLink    = "link"
)

// Session roles on a work item.
const (
	WorkSessionCurrent = "current"
	WorkSessionPast    = "past"
)

// Link kinds.
const (
	WorkLinkIssue = "issue"
	WorkLinkPlan  = "plan"
	WorkLinkTodo  = "todo"
	WorkLinkJob   = "job"
)

// Errors surfaced to the entry layers.
var (
	ErrWorkItemNotFound = errors.New("work item not found")
	ErrWorkItemConflict = errors.New("work item was changed by someone else (rev mismatch)")
	ErrWorkInvalid      = errors.New("invalid work item input")
)

// Text caps keep a runaway agent from bloating the row.
const (
	maxWorkTitle   = 200
	maxWorkField   = 4000
	maxWorkJournal = 16000
)

// WorkItem is one thing the human is working on (design §4.1). Timestamps are unix
// seconds; Rev is the optimistic-lock counter (incremented on every field write).
type WorkItem struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Goal           string `json:"goal"`
	Status         string `json:"status"`
	StatusSource   string `json:"status_source"`
	BlockerKind    string `json:"blocker_kind,omitempty"`
	BlockerText    string `json:"blocker_text,omitempty"`
	NextStep       string `json:"next_step,omitempty"`
	Summary        string `json:"summary,omitempty"`
	ProjectKey     string `json:"project_key,omitempty"`
	Workspace      string `json:"workspace,omitempty"`
	Priority       int    `json:"priority,omitempty"`
	ParkUntil      int64  `json:"park_until,omitempty"`
	ParkNote       string `json:"park_note,omitempty"`
	RemindAt       int64  `json:"remind_at,omitempty"`
	RemindedAt     int64  `json:"reminded_at,omitempty"`
	Source         string `json:"source"`
	Unsorted       bool   `json:"unsorted"`
	MergedInto     string `json:"merged_into,omitempty"`
	Rev            int64  `json:"rev"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`
	UpdatedBy      string `json:"updated_by,omitempty"`
	ClosedAt       int64  `json:"closed_at,omitempty"`
	LastActivityAt int64  `json:"last_activity_at"`
	StatusAt       int64  `json:"status_at,omitempty"`
}

// WorkItemSession is one session <-> work item relation.
type WorkItemSession struct {
	WorkItemID string `json:"work_item_id"`
	SessionID  string `json:"session_id"`
	Role       string `json:"role"`
	AttachedAt int64  `json:"attached_at"`
	DetachedAt int64  `json:"detached_at,omitempty"`
}

// WorkJournalEntry is one append-only journal line.
type WorkJournalEntry struct {
	ID         int64  `json:"id"`
	WorkItemID string `json:"work_item_id"`
	Kind       string `json:"kind"`
	Text       string `json:"text"`
	By         string `json:"by"`
	At         int64  `json:"at"`
	OriginItem string `json:"origin_item,omitempty"`
	// Level is milestone | detail (WORK-06, see DefaultWorkJournalLevel).
	Level string `json:"level"`
}

// WorkLink points a work item at an issue / plan / todo / job.
type WorkLink struct {
	WorkItemID string `json:"work_item_id,omitempty"`
	Kind       string `json:"kind"`
	Ref        string `json:"ref"`
	CreatedAt  int64  `json:"created_at,omitempty"`
}

const selectWorkCols = `SELECT id, title, goal, status, status_source, blocker_kind, blocker_text, next_step,
  summary, project_key, workspace, priority, park_until, park_note, remind_at, reminded_at, source, unsorted,
  merged_into, rev, created_at, updated_at, updated_by, closed_at, last_activity_at, status_at FROM work_items`

func scanWorkItem(sc rowScanner) (WorkItem, error) {
	var w WorkItem
	var unsorted int
	err := sc.Scan(&w.ID, &w.Title, &w.Goal, &w.Status, &w.StatusSource, &w.BlockerKind, &w.BlockerText,
		&w.NextStep, &w.Summary, &w.ProjectKey, &w.Workspace, &w.Priority, &w.ParkUntil, &w.ParkNote,
		&w.RemindAt, &w.RemindedAt, &w.Source, &unsorted, &w.MergedInto, &w.Rev, &w.CreatedAt, &w.UpdatedAt,
		&w.UpdatedBy, &w.ClosedAt, &w.LastActivityAt, &w.StatusAt)
	w.Unsorted = unsorted != 0
	return w, err
}

func newWorkID() string {
	var b [5]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("w-%010x", time.Now().UnixNano()&0xffffffffff)
	}
	return "w-" + hex.EncodeToString(b[:])
}

func capText(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// FirstLine returns the first non-empty line of s, trimmed and capped to max runes.
func FirstLine(s string, max int) string {
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		r := []rune(ln)
		if len(r) > max {
			return string(r[:max]) + "…"
		}
		return ln
	}
	return ""
}

// execer is the *sql.DB / *sql.Tx common surface.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
	Query(query string, args ...any) (*sql.Rows, error)
}

func (s *Store) getWorkItemOn(q execer, id string) (WorkItem, bool, error) {
	w, err := scanWorkItem(q.QueryRow(selectWorkCols+" WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return WorkItem{}, false, nil
	}
	if err != nil {
		return WorkItem{}, false, fmt.Errorf("jobstore: get work item %q: %w", id, err)
	}
	return w, true, nil
}

// appendWorkJournalOn writes a line at the default level for its kind and author.
func (s *Store) appendWorkJournalOn(q execer, id, kind, text, by string, at int64, origin string) (WorkJournalEntry, error) {
	return s.appendWorkJournalLvOn(q, id, kind, "", text, by, at, origin)
}

// appendWorkJournalLvOn writes a line at an explicit level ("" = DefaultWorkJournalLevel).
func (s *Store) appendWorkJournalLvOn(q execer, id, kind, level, text, by string, at int64, origin string) (WorkJournalEntry, error) {
	text = capText(text, maxWorkJournal)
	if strings.TrimSpace(by) == "" {
		by = "system"
	}
	if level == "" {
		level = DefaultWorkJournalLevel(kind, by)
	}
	res, err := q.Exec(`INSERT INTO work_journal(work_item_id, kind, text, by, at, origin_item, level) VALUES (?,?,?,?,?,?,?)`,
		id, kind, text, by, at, origin, level)
	if err != nil {
		return WorkJournalEntry{}, fmt.Errorf("jobstore: append work journal %q: %w", id, err)
	}
	jid, _ := res.LastInsertId()
	return WorkJournalEntry{ID: jid, WorkItemID: id, Kind: kind, Text: text, By: by, At: at, OriginItem: origin, Level: level}, nil
}

func validJournalKind(k string) bool {
	switch k {
	case WorkJournalReport, WorkJournalNote, WorkJournalStatus, WorkJournalSteward, WorkJournalLink:
		return true
	}
	return false
}

// WorkItemInput is the creation request.
type WorkItemInput struct {
	Title      string
	Goal       string
	Status     string
	ProjectKey string
	Workspace  string
	NextStep   string
	Priority   int
	Source     string
	Unsorted   bool
	// SessionIDs are attached as `current` sessions.
	SessionIDs []string
	By         string
	// Note is the creation journal text ("" = a default line).
	Note string
}

// CreateWorkItem inserts a work item (rev 1) with its creation journal line and
// attaches the given sessions. An empty title is rejected.
func (s *Store) CreateWorkItem(in WorkItemInput) (WorkItem, error) {
	title := capText(in.Title, maxWorkTitle)
	if title == "" {
		return WorkItem{}, fmt.Errorf("%w: title required", ErrWorkInvalid)
	}
	status := in.Status
	if status == "" {
		status = WorkActive
	}
	if !ValidWorkStatus(status) {
		return WorkItem{}, fmt.Errorf("%w: invalid status %q", ErrWorkInvalid, in.Status)
	}
	source := in.Source
	if source == "" {
		source = WorkOriginHuman
	}
	defer s.emit(Change{Kind: ChangeWork})
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.createWorkItemLocked(s.db, in, title, status, source)
}

func (s *Store) createWorkItemLocked(q execer, in WorkItemInput, title, status, source string) (WorkItem, error) {
	now := s.unixNow()
	id := newWorkID()
	// status_source: a draft derives from its session (auto); a person-created item
	// with an explicit status is the person's call.
	statusSource := WorkSourceAuto
	if source != WorkOriginAuto && in.Status != "" && in.Status != WorkActive {
		statusSource = WorkSourceHuman
	}
	closed := int64(0)
	if WorkStatusFinal(status) {
		closed = now
	}
	_, err := q.Exec(`INSERT INTO work_items(id, title, goal, status, status_source, next_step, project_key,
  workspace, priority, source, unsorted, rev, created_at, updated_at, updated_by, closed_at, last_activity_at, status_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,1,?,?,?,?,?,?)`,
		id, title, capText(in.Goal, maxWorkField), status, statusSource, capText(in.NextStep, maxWorkField),
		strings.TrimSpace(in.ProjectKey), strings.TrimSpace(in.Workspace), in.Priority, source, boolInt(in.Unsorted),
		now, now, in.By, closed, now, now)
	if err != nil {
		return WorkItem{}, fmt.Errorf("jobstore: insert work item: %w", err)
	}
	creator := in.By
	if strings.TrimSpace(creator) == "" {
		creator = "system"
	}
	for f, v := range map[string]string{WorkFieldGoal: in.Goal, WorkFieldNext: in.NextStep} {
		if strings.TrimSpace(v) == "" {
			continue
		}
		if _, err := q.Exec(`INSERT INTO work_field_sources(work_item_id, field, by, at) VALUES (?,?,?,?)
  ON CONFLICT(work_item_id, field) DO UPDATE SET by=excluded.by, at=excluded.at`, id, f, creator, now); err != nil {
			return WorkItem{}, fmt.Errorf("jobstore: record work field source: %w", err)
		}
	}
	note := in.Note
	if note == "" {
		note = "创建工作项"
	}
	if _, err := s.appendWorkJournalOn(q, id, WorkJournalStatus, note, in.By, now, ""); err != nil {
		return WorkItem{}, err
	}
	for _, sid := range in.SessionIDs {
		sid = strings.TrimSpace(sid)
		if sid == "" {
			continue
		}
		if _, err := q.Exec(`INSERT INTO work_item_sessions(work_item_id, session_id, role, attached_at, detached_at)
  VALUES (?,?,?,?,0) ON CONFLICT(work_item_id, session_id) DO UPDATE SET role='current', detached_at=0`,
			id, sid, WorkSessionCurrent, now); err != nil {
			return WorkItem{}, fmt.Errorf("jobstore: attach work session: %w", err)
		}
	}
	w, _, err := s.getWorkItemOn(q, id)
	return w, err
}

// EnsureDraftWorkItemForSession creates the auto draft for a session's first human
// prompt (design §4: "新会话第一次有人工提问时，自动生成一张草稿工作项"). It does
// nothing (created=false) when the session already belongs to any work item — even a
// past or merged one — so a prompt never spawns duplicates and a merge/split decision
// is never undone. The check and the insert are one transaction under the writer lock.
func (s *Store) EnsureDraftWorkItemForSession(a AgentSession, prompt string) (WorkItem, bool, error) {
	sid := strings.TrimSpace(a.SessionID)
	title := FirstLine(prompt, 120)
	if sid == "" || title == "" {
		return WorkItem{}, false, nil
	}
	s.writeMu.Lock()
	var exists int
	err := s.db.QueryRow(`SELECT 1 FROM work_item_sessions WHERE session_id = ? LIMIT 1`, sid).Scan(&exists)
	if err == nil {
		s.writeMu.Unlock()
		return WorkItem{}, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		s.writeMu.Unlock()
		return WorkItem{}, false, fmt.Errorf("jobstore: check work item for session: %w", err)
	}
	w, err := s.createWorkItemLocked(s.db, WorkItemInput{
		Title: title, ProjectKey: a.ProjectKey, Workspace: a.Cwd, Source: WorkOriginAuto, Unsorted: true,
		SessionIDs: []string{sid}, By: "system",
		Note: "自动创建草稿（会话 " + shortSID(sid) + " 首次提问）",
	}, capText(title, maxWorkTitle), WorkActive, WorkOriginAuto)
	s.writeMu.Unlock()
	if err != nil {
		return WorkItem{}, false, err
	}
	s.emit(Change{Kind: ChangeWork, ID: w.ID})
	return w, true, nil
}

func shortSID(sid string) string {
	if len(sid) > 8 {
		return sid[:8]
	}
	return sid
}

// GetWorkItem reads one work item.
func (s *Store) GetWorkItem(id string) (WorkItem, bool, error) {
	return s.getWorkItemOn(s.db, strings.TrimSpace(id))
}

// WorkListOpts filters ListWorkItems.
type WorkListOpts struct {
	Statuses      []string
	Project       string
	Workspace     string
	Unsorted      *bool
	SessionID     string
	Query         string
	IncludeClosed bool
	IncludeMerged bool
	// Due restricts to items whose reminder / park deadline has passed at Now.
	Due   bool
	Now   int64
	Limit int
}

// ListWorkItems lists work items, most recently active first. Closed (done/dropped)
// and merged-away items are excluded unless asked for, or unless Statuses names a
// closed status explicitly.
func (s *Store) ListWorkItems(o WorkListOpts) ([]WorkItem, error) {
	var where []string
	var args []any
	if len(o.Statuses) > 0 {
		ph := make([]string, 0, len(o.Statuses))
		for _, st := range o.Statuses {
			if !ValidWorkStatus(st) {
				return nil, fmt.Errorf("%w: invalid status %q", ErrWorkInvalid, st)
			}
			ph = append(ph, "?")
			args = append(args, st)
		}
		where = append(where, "status IN ("+strings.Join(ph, ",")+")")
	} else if !o.IncludeClosed {
		where = append(where, "status NOT IN ('done','dropped')")
	}
	if !o.IncludeMerged {
		where = append(where, "merged_into = ''")
	}
	if o.Project != "" {
		where = append(where, "project_key = ?")
		args = append(args, o.Project)
	}
	if o.Workspace != "" {
		where = append(where, "workspace = ?")
		args = append(args, o.Workspace)
	}
	if o.Unsorted != nil {
		where = append(where, "unsorted = ?")
		args = append(args, boolInt(*o.Unsorted))
	}
	if o.SessionID != "" {
		where = append(where, "id IN (SELECT work_item_id FROM work_item_sessions WHERE session_id = ?)")
		args = append(args, o.SessionID)
	}
	if q := strings.TrimSpace(o.Query); q != "" {
		like := "%" + strings.ReplaceAll(strings.ReplaceAll(q, "%", `\%`), "_", `\_`) + "%"
		where = append(where, `(title LIKE ? ESCAPE '\' OR goal LIKE ? ESCAPE '\' OR blocker_text LIKE ? ESCAPE '\' OR next_step LIKE ? ESCAPE '\')`)
		args = append(args, like, like, like, like)
	}
	if o.Due {
		now := o.Now
		if now <= 0 {
			now = s.unixNow()
		}
		where = append(where, `status NOT IN ('done','dropped') AND ((remind_at > 0 AND remind_at <= ?) OR (status = 'parked' AND park_until > 0 AND park_until <= ?))`)
		args = append(args, now, now)
	}
	q := selectWorkCols
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	limit := o.Limit
	if limit <= 0 || limit > 2000 {
		limit = 1000
	}
	q += fmt.Sprintf(" ORDER BY last_activity_at DESC, id DESC LIMIT %d", limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list work items: %w", err)
	}
	defer rows.Close()
	out := make([]WorkItem, 0)
	for rows.Next() {
		w, err := scanWorkItem(rows)
		if err != nil {
			return nil, fmt.Errorf("jobstore: scan work item: %w", err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// WorkItemPatch is a partial update; nil fields are untouched.
type WorkItemPatch struct {
	Title       *string
	Goal        *string
	Status      *string
	BlockerKind *string
	BlockerText *string
	NextStep    *string
	Summary     *string
	ProjectKey  *string
	Workspace   *string
	Priority    *int
	ParkUntil   *int64
	ParkNote    *string
	RemindAt    *int64
	Unsorted    *bool
	// StatusSource overrides who owns the status (the work service sets it for session
	// reports and the auto mapping); with a Status change and no override, a patch is a
	// human edit.
	StatusSource *string
	// Quiet skips the automatic status journal line: the caller writes its own (a
	// session report is journaled as a `report` entry instead of a `status` one).
	Quiet bool
}

func fmtWorkTime(t int64) string {
	if t <= 0 {
		return "无"
	}
	return time.Unix(t, 0).Format("2006-01-02 15:04")
}

// UpdateWorkItem applies a patch under optimistic locking (expectedRev 0 = skip the
// check) and journals what changed. It returns the stored item and the one-line change
// summary ("" when nothing changed — then nothing is written and rev stays).
func (s *Store) UpdateWorkItem(id string, p WorkItemPatch, expectedRev int64, by string) (WorkItem, string, error) {
	id = strings.TrimSpace(id)
	defer s.emit(Change{Kind: ChangeWork, ID: id})
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	cur, ok, err := s.getWorkItemOn(s.db, id)
	if err != nil {
		return WorkItem{}, "", err
	}
	if !ok {
		return WorkItem{}, "", ErrWorkItemNotFound
	}
	if expectedRev > 0 && expectedRev != cur.Rev {
		return cur, "", fmt.Errorf("%w: expected %d, current %d", ErrWorkItemConflict, expectedRev, cur.Rev)
	}
	next := cur
	var changes []string
	now := s.unixNow()
	setStr := func(dst *string, v *string, label string, max int, show bool) {
		if v == nil {
			return
		}
		nv := capText(*v, max)
		if nv == *dst {
			return
		}
		old := *dst
		*dst = nv
		switch {
		case show && nv == "":
			changes = append(changes, label+"：已清除")
		case show && old != "":
			changes = append(changes, fmt.Sprintf("%s：%s → %s", label, capText(old, 60), capText(nv, 60)))
		case show:
			changes = append(changes, fmt.Sprintf("%s：%s", label, capText(nv, 60)))
		default:
			changes = append(changes, label+"已更新")
		}
	}
	if p.Title != nil && strings.TrimSpace(*p.Title) == "" {
		return cur, "", fmt.Errorf("%w: title must not be empty", ErrWorkInvalid)
	}
	setStr(&next.Title, p.Title, "标题", maxWorkTitle, true)
	setStr(&next.Goal, p.Goal, "目标", maxWorkField, false)
	setStr(&next.BlockerKind, p.BlockerKind, "阻塞类型", 100, true)
	setStr(&next.BlockerText, p.BlockerText, "阻塞", maxWorkField, true)
	setStr(&next.NextStep, p.NextStep, "下一步", maxWorkField, true)
	setStr(&next.Summary, p.Summary, "摘要", maxWorkField, false)
	setStr(&next.ProjectKey, p.ProjectKey, "项目", 200, true)
	setStr(&next.Workspace, p.Workspace, "工作区", 1000, true)
	setStr(&next.ParkNote, p.ParkNote, "搁置条件", maxWorkField, true)
	if p.Priority != nil && *p.Priority != next.Priority {
		next.Priority = *p.Priority
		changes = append(changes, fmt.Sprintf("优先级：%d", next.Priority))
	}
	if p.ParkUntil != nil && *p.ParkUntil != next.ParkUntil {
		next.ParkUntil = *p.ParkUntil
		changes = append(changes, "搁置至："+fmtWorkTime(next.ParkUntil))
	}
	if p.RemindAt != nil && *p.RemindAt != next.RemindAt {
		next.RemindAt = *p.RemindAt
		changes = append(changes, "提醒时间："+fmtWorkTime(next.RemindAt))
	}
	if p.Unsorted != nil && *p.Unsorted != next.Unsorted {
		next.Unsorted = *p.Unsorted
		if next.Unsorted {
			changes = append(changes, "标为未整理")
		} else {
			changes = append(changes, "已整理")
		}
	}
	if p.Status != nil {
		st := strings.TrimSpace(*p.Status)
		if !ValidWorkStatus(st) {
			return cur, "", fmt.Errorf("%w: invalid status %q", ErrWorkInvalid, *p.Status)
		}
		if st != next.Status {
			changes = append(changes, fmt.Sprintf("状态：%s → %s", next.Status, st))
			next.Status = st
			next.StatusAt = now
			if WorkStatusFinal(st) {
				next.ClosedAt = now
			} else {
				next.ClosedAt = 0
			}
			if p.StatusSource == nil {
				next.StatusSource = WorkSourceHuman
			}
		}
	}
	if p.StatusSource != nil && *p.StatusSource != next.StatusSource {
		switch *p.StatusSource {
		case WorkSourceAuto, WorkSourceHuman, WorkSourceReport:
			if p.Status == nil || *p.Status == cur.Status {
				changes = append(changes, "状态来源："+*p.StatusSource)
			}
			next.StatusSource = *p.StatusSource
		default:
			return cur, "", fmt.Errorf("%w: invalid status_source %q", ErrWorkInvalid, *p.StatusSource)
		}
	}
	if len(changes) == 0 {
		return cur, "", nil
	}
	next.Rev = cur.Rev + 1
	next.UpdatedAt = now
	next.UpdatedBy = by
	next.LastActivityAt = now
	_, err = s.db.Exec(`UPDATE work_items SET title=?, goal=?, status=?, status_source=?, blocker_kind=?, blocker_text=?,
  next_step=?, summary=?, project_key=?, workspace=?, priority=?, park_until=?, park_note=?, remind_at=?, unsorted=?,
  rev=?, updated_at=?, updated_by=?, closed_at=?, last_activity_at=?, status_at=? WHERE id=? AND rev=?`,
		next.Title, next.Goal, next.Status, next.StatusSource, next.BlockerKind, next.BlockerText, next.NextStep,
		next.Summary, next.ProjectKey, next.Workspace, next.Priority, next.ParkUntil, next.ParkNote, next.RemindAt,
		boolInt(next.Unsorted), next.Rev, next.UpdatedAt, next.UpdatedBy, next.ClosedAt, next.LastActivityAt,
		next.StatusAt, id, cur.Rev)
	if err != nil {
		return cur, "", fmt.Errorf("jobstore: update work item %q: %w", id, err)
	}
	if err := s.recordFieldSources(id, cur, next, by, now); err != nil {
		return cur, "", err
	}
	summary := strings.Join(changes, "；")
	if !p.Quiet {
		// WORK-06: a status a person (or the steward) changed is a milestone; a field-only
		// edit is a detail.
		level := WorkLevelDetail
		if next.Status != cur.Status && next.StatusSource != WorkSourceAuto {
			level = WorkLevelMilestone
		}
		if _, err := s.appendWorkJournalLvOn(s.db, id, WorkJournalStatus, level, summary, by, now, ""); err != nil {
			return cur, "", err
		}
	}
	return next, summary, nil
}

// AppendWorkJournal appends one journal line (at the default level for its kind and
// author) and refreshes the item's activity time.
func (s *Store) AppendWorkJournal(id, kind, text, by string) (WorkJournalEntry, error) {
	return s.appendWorkJournal(id, kind, text, by, "")
}

func (s *Store) appendWorkJournal(id, kind, text, by, level string) (WorkJournalEntry, error) {
	id = strings.TrimSpace(id)
	if !validJournalKind(kind) {
		return WorkJournalEntry{}, fmt.Errorf("%w: invalid journal kind %q", ErrWorkInvalid, kind)
	}
	if strings.TrimSpace(text) == "" {
		return WorkJournalEntry{}, fmt.Errorf("%w: text required", ErrWorkInvalid)
	}
	defer s.emit(Change{Kind: ChangeWork, ID: id})
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, ok, err := s.getWorkItemOn(s.db, id); err != nil {
		return WorkJournalEntry{}, err
	} else if !ok {
		return WorkJournalEntry{}, ErrWorkItemNotFound
	}
	now := s.unixNow()
	e, err := s.appendWorkJournalLvOn(s.db, id, kind, level, text, by, now, "")
	if err != nil {
		return WorkJournalEntry{}, err
	}
	if _, err := s.db.Exec(`UPDATE work_items SET last_activity_at = ? WHERE id = ?`, now, id); err != nil {
		return WorkJournalEntry{}, fmt.Errorf("jobstore: touch work item: %w", err)
	}
	return e, nil
}

// ListWorkJournal returns the newest `limit` entries (default 200), oldest first, so
// the timeline reads top to bottom. before > 0 pages backwards by entry id.
func (s *Store) ListWorkJournal(id string, limit int, before int64) ([]WorkJournalEntry, error) {
	return s.ListWorkJournalLevel(id, limit, before, "")
}

// ListWorkItemSessions returns an item's sessions, current first.
func (s *Store) ListWorkItemSessions(id string) ([]WorkItemSession, error) {
	rows, err := s.db.Query(`SELECT work_item_id, session_id, role, attached_at, detached_at FROM work_item_sessions
  WHERE work_item_id = ? ORDER BY CASE role WHEN 'current' THEN 0 ELSE 1 END, attached_at DESC, session_id`, strings.TrimSpace(id))
	if err != nil {
		return nil, fmt.Errorf("jobstore: list work item sessions: %w", err)
	}
	defer rows.Close()
	out := make([]WorkItemSession, 0)
	for rows.Next() {
		var r WorkItemSession
		if err := rows.Scan(&r.WorkItemID, &r.SessionID, &r.Role, &r.AttachedAt, &r.DetachedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CurrentWorkSessionIDs maps work item id -> its current session ids, for the items
// in ids (every item when ids is empty).
func (s *Store) CurrentWorkSessionIDs(ids []string) (map[string][]string, error) {
	rows, err := s.db.Query(`SELECT work_item_id, session_id FROM work_item_sessions WHERE role = 'current' ORDER BY attached_at, session_id`)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list current work sessions: %w", err)
	}
	defer rows.Close()
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	out := map[string][]string{}
	for rows.Next() {
		var wid, sid string
		if err := rows.Scan(&wid, &sid); err != nil {
			return nil, err
		}
		if len(want) > 0 && !want[wid] {
			continue
		}
		out[wid] = append(out[wid], sid)
	}
	return out, rows.Err()
}

// AttachWorkSession makes sid a current session of the item (idempotent; a past
// session comes back to current).
func (s *Store) AttachWorkSession(id, sid, by string) error {
	id, sid = strings.TrimSpace(id), strings.TrimSpace(sid)
	if sid == "" {
		return fmt.Errorf("%w: session id required", ErrWorkInvalid)
	}
	defer s.emit(Change{Kind: ChangeWork, ID: id})
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, ok, err := s.getWorkItemOn(s.db, id); err != nil {
		return err
	} else if !ok {
		return ErrWorkItemNotFound
	}
	now := s.unixNow()
	var prevRole string
	_ = s.db.QueryRow(`SELECT role FROM work_item_sessions WHERE work_item_id=? AND session_id=?`, id, sid).Scan(&prevRole)
	if prevRole == WorkSessionCurrent {
		return nil
	}
	if _, err := s.db.Exec(`INSERT INTO work_item_sessions(work_item_id, session_id, role, attached_at, detached_at)
  VALUES (?,?,?,?,0) ON CONFLICT(work_item_id, session_id) DO UPDATE SET role='current', attached_at=excluded.attached_at, detached_at=0`,
		id, sid, WorkSessionCurrent, now); err != nil {
		return fmt.Errorf("jobstore: attach work session: %w", err)
	}
	if _, err := s.appendWorkJournalOn(s.db, id, WorkJournalLink, "关联会话 "+shortSID(sid), by, now, ""); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE work_items SET last_activity_at=?, rev=rev+1 WHERE id=?`, now, id)
	return err
}

// DetachWorkSession moves a session to `past` (it stays in the history).
func (s *Store) DetachWorkSession(id, sid, by string) error {
	id, sid = strings.TrimSpace(id), strings.TrimSpace(sid)
	defer s.emit(Change{Kind: ChangeWork, ID: id})
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	now := s.unixNow()
	res, err := s.db.Exec(`UPDATE work_item_sessions SET role='past', detached_at=? WHERE work_item_id=? AND session_id=? AND role='current'`, now, id, sid)
	if err != nil {
		return fmt.Errorf("jobstore: detach work session: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, ok, e := s.getWorkItemOn(s.db, id); e != nil {
			return e
		} else if !ok {
			return ErrWorkItemNotFound
		}
		return nil
	}
	if _, err := s.appendWorkJournalOn(s.db, id, WorkJournalLink, "取消关联会话 "+shortSID(sid), by, now, ""); err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE work_items SET last_activity_at=?, rev=rev+1 WHERE id=?`, now, id)
	return err
}

func validWorkLinkKind(k string) bool {
	switch k {
	case WorkLinkIssue, WorkLinkPlan, WorkLinkTodo, WorkLinkJob:
		return true
	}
	return false
}

// AddWorkLink links an issue / plan / todo / job (idempotent).
func (s *Store) AddWorkLink(id, kind, ref, by string) (WorkLink, error) {
	id, kind, ref = strings.TrimSpace(id), strings.TrimSpace(kind), strings.TrimSpace(ref)
	if !validWorkLinkKind(kind) || ref == "" {
		return WorkLink{}, fmt.Errorf("%w: link needs kind issue|plan|todo|job and a ref", ErrWorkInvalid)
	}
	defer s.emit(Change{Kind: ChangeWork, ID: id})
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, ok, err := s.getWorkItemOn(s.db, id); err != nil {
		return WorkLink{}, err
	} else if !ok {
		return WorkLink{}, ErrWorkItemNotFound
	}
	now := s.unixNow()
	res, err := s.db.Exec(`INSERT INTO work_links(work_item_id, kind, ref, created_at) VALUES (?,?,?,?) ON CONFLICT DO NOTHING`, id, kind, ref, now)
	if err != nil {
		return WorkLink{}, fmt.Errorf("jobstore: add work link: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		if _, err := s.appendWorkJournalOn(s.db, id, WorkJournalLink, "关联 "+kind+" "+ref, by, now, ""); err != nil {
			return WorkLink{}, err
		}
		if _, err := s.db.Exec(`UPDATE work_items SET last_activity_at=?, rev=rev+1 WHERE id=?`, now, id); err != nil {
			return WorkLink{}, err
		}
	}
	return WorkLink{WorkItemID: id, Kind: kind, Ref: ref, CreatedAt: now}, nil
}

// RemoveWorkLink removes a link; ok=false when it was not there.
func (s *Store) RemoveWorkLink(id, kind, ref, by string) (bool, error) {
	id = strings.TrimSpace(id)
	defer s.emit(Change{Kind: ChangeWork, ID: id})
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`DELETE FROM work_links WHERE work_item_id=? AND kind=? AND ref=?`, id, kind, ref)
	if err != nil {
		return false, fmt.Errorf("jobstore: remove work link: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return false, nil
	}
	now := s.unixNow()
	if _, err := s.appendWorkJournalOn(s.db, id, WorkJournalLink, "取消关联 "+kind+" "+ref, by, now, ""); err != nil {
		return false, err
	}
	_, err = s.db.Exec(`UPDATE work_items SET last_activity_at=?, rev=rev+1 WHERE id=?`, now, id)
	return true, err
}

// ListWorkLinks returns an item's links.
func (s *Store) ListWorkLinks(id string) ([]WorkLink, error) {
	rows, err := s.db.Query(`SELECT work_item_id, kind, ref, created_at FROM work_links WHERE work_item_id=? ORDER BY created_at, kind, ref`, strings.TrimSpace(id))
	if err != nil {
		return nil, fmt.Errorf("jobstore: list work links: %w", err)
	}
	defer rows.Close()
	out := make([]WorkLink, 0)
	for rows.Next() {
		var l WorkLink
		if err := rows.Scan(&l.WorkItemID, &l.Kind, &l.Ref, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// MergeWorkItems folds the sources into target (design §4: 合并): sessions and links
// move over, the sources' journals are re-homed onto the target with their origin
// recorded, and each source ends as dropped + merged_into so it vanishes from the
// default list but stays resolvable. A source that is the target, already merged or
// unknown is an error before anything changes.
func (s *Store) MergeWorkItems(targetID string, sourceIDs []string, by string) (WorkItem, error) {
	targetID = strings.TrimSpace(targetID)
	defer s.emit(Change{Kind: ChangeWork, ID: targetID})
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	target, ok, err := s.getWorkItemOn(s.db, targetID)
	if err != nil {
		return WorkItem{}, err
	}
	if !ok {
		return WorkItem{}, ErrWorkItemNotFound
	}
	if target.MergedInto != "" {
		return WorkItem{}, fmt.Errorf("%w: target %s was itself merged into %s", ErrWorkInvalid, targetID, target.MergedInto)
	}
	seen := map[string]bool{targetID: true}
	var sources []WorkItem
	for _, sid := range sourceIDs {
		sid = strings.TrimSpace(sid)
		if sid == "" {
			continue
		}
		if seen[sid] {
			if sid == targetID {
				return WorkItem{}, fmt.Errorf("%w: cannot merge a work item into itself", ErrWorkInvalid)
			}
			continue
		}
		seen[sid] = true
		src, ok, err := s.getWorkItemOn(s.db, sid)
		if err != nil {
			return WorkItem{}, err
		}
		if !ok {
			return WorkItem{}, fmt.Errorf("%w: source %s", ErrWorkItemNotFound, sid)
		}
		if src.MergedInto != "" {
			return WorkItem{}, fmt.Errorf("%w: source %s is already merged into %s", ErrWorkInvalid, sid, src.MergedInto)
		}
		sources = append(sources, src)
	}
	if len(sources) == 0 {
		return WorkItem{}, fmt.Errorf("%w: no source work items to merge", ErrWorkInvalid)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return WorkItem{}, fmt.Errorf("jobstore: begin merge: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := s.unixNow()
	var parts []string
	for _, src := range sources {
		// Sessions: keep the role; if both items have the session, `current` wins.
		if _, err := tx.Exec(`INSERT INTO work_item_sessions(work_item_id, session_id, role, attached_at, detached_at)
  SELECT ?, session_id, role, attached_at, detached_at FROM work_item_sessions WHERE work_item_id = ?
  ON CONFLICT(work_item_id, session_id) DO UPDATE SET
    role = CASE WHEN excluded.role = 'current' THEN 'current' ELSE role END,
    detached_at = CASE WHEN excluded.role = 'current' THEN 0 ELSE detached_at END`, targetID, src.ID); err != nil {
			return WorkItem{}, fmt.Errorf("jobstore: merge sessions: %w", err)
		}
		var nSess int
		_ = tx.QueryRow(`SELECT COUNT(*) FROM work_item_sessions WHERE work_item_id = ?`, src.ID).Scan(&nSess)
		if _, err := tx.Exec(`DELETE FROM work_item_sessions WHERE work_item_id = ?`, src.ID); err != nil {
			return WorkItem{}, err
		}
		if _, err := tx.Exec(`INSERT INTO work_links(work_item_id, kind, ref, created_at)
  SELECT ?, kind, ref, created_at FROM work_links WHERE work_item_id = ? ON CONFLICT DO NOTHING`, targetID, src.ID); err != nil {
			return WorkItem{}, fmt.Errorf("jobstore: merge links: %w", err)
		}
		if _, err := tx.Exec(`DELETE FROM work_links WHERE work_item_id = ?`, src.ID); err != nil {
			return WorkItem{}, err
		}
		var nJournal int
		_ = tx.QueryRow(`SELECT COUNT(*) FROM work_journal WHERE work_item_id = ?`, src.ID).Scan(&nJournal)
		if _, err := tx.Exec(`UPDATE work_journal SET origin_item = CASE WHEN origin_item = '' THEN work_item_id ELSE origin_item END,
  work_item_id = ? WHERE work_item_id = ?`, targetID, src.ID); err != nil {
			return WorkItem{}, fmt.Errorf("jobstore: merge journal: %w", err)
		}
		if _, err := tx.Exec(`UPDATE work_items SET merged_into=?, status=?, status_source=?, closed_at=?, status_at=?,
  rev=rev+1, updated_at=?, updated_by=?, last_activity_at=?, unsorted=0, remind_at=0, park_until=0 WHERE id=?`,
			targetID, WorkDropped, WorkSourceHuman, now, now, now, by, now, src.ID); err != nil {
			return WorkItem{}, fmt.Errorf("jobstore: close merged source: %w", err)
		}
		if _, err := s.appendWorkJournalOn(tx, src.ID, WorkJournalStatus, "已合并到 "+targetID, by, now, ""); err != nil {
			return WorkItem{}, err
		}
		parts = append(parts, fmt.Sprintf("%s「%s」（%d 个会话，%d 条日志）", src.ID, capText(src.Title, 40), nSess, nJournal))
	}
	if _, err := s.appendWorkJournalOn(tx, targetID, WorkJournalStatus, "合并了 "+strings.Join(parts, "、"), by, now, ""); err != nil {
		return WorkItem{}, err
	}
	if _, err := tx.Exec(`UPDATE work_items SET rev=rev+1, updated_at=?, updated_by=?, last_activity_at=?, unsorted=0 WHERE id=?`, now, by, now, targetID); err != nil {
		return WorkItem{}, err
	}
	if err := tx.Commit(); err != nil {
		return WorkItem{}, fmt.Errorf("jobstore: commit merge: %w", err)
	}
	w, _, err := s.getWorkItemOn(s.db, targetID)
	return w, err
}

// SplitWorkItemInput describes a split.
type SplitWorkItemInput struct {
	Title string
	Goal  string
	// SessionIDs of the source item to hand to the new item.
	SessionIDs []string
	// KeepSessions leaves them attached to the source as well (one session that did
	// two things); false moves them (they become `past` on the source).
	KeepSessions bool
	By           string
}

// SplitWorkItem creates a new work item out of id (design §4: 拆分) and returns
// (source, new). The new item inherits project/workspace and starts active.
func (s *Store) SplitWorkItem(id string, in SplitWorkItemInput) (WorkItem, WorkItem, error) {
	id = strings.TrimSpace(id)
	title := capText(in.Title, maxWorkTitle)
	if title == "" {
		return WorkItem{}, WorkItem{}, fmt.Errorf("%w: title required", ErrWorkInvalid)
	}
	defer s.emit(Change{Kind: ChangeWork})
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	src, ok, err := s.getWorkItemOn(s.db, id)
	if err != nil {
		return WorkItem{}, WorkItem{}, err
	}
	if !ok {
		return WorkItem{}, WorkItem{}, ErrWorkItemNotFound
	}
	for _, sid := range in.SessionIDs {
		var role string
		if err := s.db.QueryRow(`SELECT role FROM work_item_sessions WHERE work_item_id=? AND session_id=?`, id, sid).Scan(&role); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return WorkItem{}, WorkItem{}, fmt.Errorf("%w: session %s is not attached to %s", ErrWorkInvalid, sid, id)
			}
			return WorkItem{}, WorkItem{}, err
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return WorkItem{}, WorkItem{}, fmt.Errorf("jobstore: begin split: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	nw, err := s.createWorkItemLocked(tx, WorkItemInput{
		Title: title, Goal: in.Goal, ProjectKey: src.ProjectKey, Workspace: src.Workspace,
		Source: WorkOriginHuman, SessionIDs: in.SessionIDs, By: in.By, Note: "从 " + id + "「" + capText(src.Title, 40) + "」拆分出",
	}, title, WorkActive, WorkOriginHuman)
	if err != nil {
		return WorkItem{}, WorkItem{}, err
	}
	now := s.unixNow()
	if !in.KeepSessions {
		for _, sid := range in.SessionIDs {
			if _, err := tx.Exec(`UPDATE work_item_sessions SET role='past', detached_at=? WHERE work_item_id=? AND session_id=?`, now, id, sid); err != nil {
				return WorkItem{}, WorkItem{}, err
			}
		}
	}
	note := "拆分出 " + nw.ID + "「" + capText(title, 40) + "」"
	if len(in.SessionIDs) > 0 {
		verb := "移走"
		if in.KeepSessions {
			verb = "同时保留"
		}
		note += fmt.Sprintf("（%d 个会话%s）", len(in.SessionIDs), verb)
	}
	if _, err := s.appendWorkJournalOn(tx, id, WorkJournalStatus, note, in.By, now, ""); err != nil {
		return WorkItem{}, WorkItem{}, err
	}
	if _, err := tx.Exec(`UPDATE work_items SET rev=rev+1, last_activity_at=?, updated_at=?, updated_by=? WHERE id=?`, now, now, in.By, id); err != nil {
		return WorkItem{}, WorkItem{}, err
	}
	if err := tx.Commit(); err != nil {
		return WorkItem{}, WorkItem{}, fmt.Errorf("jobstore: commit split: %w", err)
	}
	srcNow, _, _ := s.getWorkItemOn(s.db, id)
	return srcNow, nw, nil
}

// ListDueWorkItems returns the items whose reminder or park deadline has passed at
// now and that have not been announced for that deadline yet.
func (s *Store) ListDueWorkItems(now int64) ([]WorkItem, error) {
	rows, err := s.db.Query(selectWorkCols+` WHERE status NOT IN ('done','dropped') AND merged_into = '' AND
  ((remind_at > 0 AND remind_at <= ? AND reminded_at < remind_at) OR (status = 'parked' AND park_until > 0 AND park_until <= ? AND reminded_at < park_until))
  ORDER BY id`, now, now)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list due work items: %w", err)
	}
	defer rows.Close()
	out := make([]WorkItem, 0)
	for rows.Next() {
		w, err := scanWorkItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// MarkWorkItemReminded records that the deadline due at `due` was announced, so the
// next sweep stays quiet until a later remind_at / park_until is set.
func (s *Store) MarkWorkItemReminded(id string, due int64, text string) error {
	defer s.emit(Change{Kind: ChangeWork, ID: id})
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	now := s.unixNow()
	if _, err := s.db.Exec(`UPDATE work_items SET reminded_at = ?, last_activity_at = ? WHERE id = ?`, due, now, id); err != nil {
		return fmt.Errorf("jobstore: mark work item reminded: %w", err)
	}
	_, err := s.appendWorkJournalOn(s.db, id, WorkJournalStatus, text, "system", now, "")
	return err
}

// SetWorkItemAutoStatus applies the session-derived status to an item whose status is
// still owned by `auto` and is not finished. changed=false when nothing was written.
func (s *Store) SetWorkItemAutoStatus(id, status, reason string) (bool, error) {
	if !ValidWorkStatus(status) {
		return false, fmt.Errorf("%w: invalid status %q", ErrWorkInvalid, status)
	}
	defer s.emit(Change{Kind: ChangeWork, ID: id})
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	cur, ok, err := s.getWorkItemOn(s.db, id)
	if err != nil {
		return false, err
	}
	if !ok || cur.StatusSource != WorkSourceAuto || WorkStatusFinal(cur.Status) || cur.MergedInto != "" || cur.Status == status {
		return false, nil
	}
	now := s.unixNow()
	if _, err := s.db.Exec(`UPDATE work_items SET status=?, status_at=?, rev=rev+1, updated_at=?, updated_by='system', last_activity_at=? WHERE id=? AND rev=?`,
		status, now, now, now, id, cur.Rev); err != nil {
		return false, fmt.Errorf("jobstore: auto status: %w", err)
	}
	text := fmt.Sprintf("状态：%s → %s（自动：%s）", cur.Status, status, reason)
	_, err = s.appendWorkJournalOn(s.db, id, WorkJournalStatus, text, "system", now, "")
	return true, err
}

// WorkKV is a tiny key/value store for work bookkeeping (digest de-duplication).
func (s *Store) GetWorkKV(k string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT v FROM work_kv WHERE k = ?`, k).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetWorkKV upserts one key.
func (s *Store) SetWorkKV(k, v string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`INSERT INTO work_kv(k, v) VALUES (?,?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`, k, v)
	return err
}

// WorkCounts is the dashboard count per status for open items.
func (s *Store) WorkCounts() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT status, COUNT(*) FROM work_items WHERE merged_into = '' GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}
