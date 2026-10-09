package jobstore

import (
	"fmt"
	"strings"
)

// Journal levels (WORK-06, design 2026-10-09 §3.3). A milestone is a line worth seeing
// on the lane / the item's default timeline ("状态变了、汇报了、job 有结果、人拍板了"); a
// detail is the bookkeeping around it (auto-derived status, links, tidy-up flow).
const (
	WorkLevelMilestone = "milestone"
	WorkLevelDetail    = "detail"
)

// ValidWorkLevel reports whether s is a journal level ("" is not one).
func ValidWorkLevel(s string) bool { return s == WorkLevelMilestone || s == WorkLevelDetail }

// isHumanBy reports whether a speaker label is a person (`human` / `human:<caller>`).
// It mirrors work.ActorKind for the one class the store needs (the store must not
// import work).
func isHumanBy(by string) bool {
	by = strings.TrimSpace(by)
	return by == "human" || strings.HasPrefix(by, "human:")
}

// DefaultWorkJournalLevel is the level a journal line gets when its writer does not
// name one: a session report is a milestone; a status line is one unless gofer wrote
// it itself (auto-derived status, reminders — `system`); a note is one when a person
// wrote it; steward / link lines are details. Writers that know better (a field-only
// edit, a job outcome, the summarizer's `milestone`) pass the level explicitly.
func DefaultWorkJournalLevel(kind, by string) string {
	switch kind {
	case WorkJournalReport:
		return WorkLevelMilestone
	case WorkJournalStatus:
		if b := strings.TrimSpace(by); b != "" && b != "system" {
			return WorkLevelMilestone
		}
	case WorkJournalNote:
		if isHumanBy(by) {
			return WorkLevelMilestone
		}
	}
	return WorkLevelDetail
}

// migrateWorkJournalLevel adds work_journal.level (WORK-06) to a database written by an
// older binary and classifies the existing lines once: reports, status lines a person /
// a report / the steward wrote, and notes a person wrote become milestones; everything
// else (auto status, reminders, links, tidy-up flow) stays detail. ALTER and backfill
// share one transaction, so an interrupted run retries and a finished one never scans
// the journal again (the column then exists).
func (s *Store) migrateWorkJournalLevel() error {
	cols, err := s.tableColumns("work_journal")
	if err != nil {
		return err
	}
	if _, ok := cols["level"]; !ok {
		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("jobstore: begin work_journal level migration: %w", err)
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := tx.Exec(`ALTER TABLE work_journal ADD COLUMN level TEXT NOT NULL DEFAULT 'detail'`); err != nil {
			return fmt.Errorf("jobstore: migrate work_journal add level: %w", err)
		}
		if _, err := tx.Exec(`UPDATE work_journal SET level = 'milestone' WHERE kind = 'report'
  OR (kind = 'status' AND by NOT IN ('', 'system'))
  OR (kind = 'note' AND (by = 'human' OR by LIKE 'human:%'))`); err != nil {
			return fmt.Errorf("jobstore: migrate work_journal backfill level: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("jobstore: commit work_journal level migration: %w", err)
		}
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_work_journal_level ON work_journal(work_item_id, level, id)`); err != nil {
		return fmt.Errorf("jobstore: migrate work_journal level index: %w", err)
	}
	return nil
}

// AppendWorkJournalLevel is AppendWorkJournal with an explicit level ("" = the default
// for the kind and author, see DefaultWorkJournalLevel).
func (s *Store) AppendWorkJournalLevel(id, kind, text, by, level string) (WorkJournalEntry, error) {
	if level != "" && !ValidWorkLevel(level) {
		return WorkJournalEntry{}, fmt.Errorf("%w: invalid journal level %q", ErrWorkInvalid, level)
	}
	return s.appendWorkJournal(id, kind, text, by, level)
}

// ListWorkJournalLevel is ListWorkJournal restricted to one level ("" = every line).
func (s *Store) ListWorkJournalLevel(id string, limit int, before int64, level string) ([]WorkJournalEntry, error) {
	if level != "" && !ValidWorkLevel(level) {
		return nil, fmt.Errorf("%w: invalid journal level %q", ErrWorkInvalid, level)
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	q := `SELECT id, work_item_id, kind, text, by, at, origin_item, level FROM work_journal WHERE work_item_id = ?`
	args := []any{strings.TrimSpace(id)}
	if level != "" {
		q += " AND level = ?"
		args = append(args, level)
	}
	if before > 0 {
		q += " AND id < ?"
		args = append(args, before)
	}
	q += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list work journal: %w", err)
	}
	defer rows.Close()
	out := make([]WorkJournalEntry, 0)
	for rows.Next() {
		var e WorkJournalEntry
		if err := rows.Scan(&e.ID, &e.WorkItemID, &e.Kind, &e.Text, &e.By, &e.At, &e.OriginItem, &e.Level); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// LatestWorkJournalAt is the time of an item's newest journal line (0 = none).
func (s *Store) LatestWorkJournalAt(id string) (int64, error) {
	var at int64
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(at), 0) FROM work_journal WHERE work_item_id = ?`, strings.TrimSpace(id)).Scan(&at); err != nil {
		return 0, fmt.Errorf("jobstore: latest work journal: %w", err)
	}
	return at, nil
}

// WorkRefs names what a job / decision / interaction belongs to, to find the open work
// items it concerns.
type WorkRefs struct {
	JobID     string
	PlanID    string
	SessionID string
}

// OpenWorkItemsFor returns the open (not done / dropped / merged) work items a job, a
// plan or a session belongs to: an explicit job / plan link, a current session that IS
// the job (an ACP / pty session job) or the session itself, or a current session that
// watches the job (a job a terminal session submitted).
func (s *Store) OpenWorkItemsFor(r WorkRefs) ([]string, error) {
	return s.openWorkItemsForOn(s.db, r)
}

func (s *Store) openWorkItemsForOn(q execer, r WorkRefs) ([]string, error) {
	var conds []string
	var args []any
	if j := strings.TrimSpace(r.JobID); j != "" {
		conds = append(conds,
			`id IN (SELECT work_item_id FROM work_links WHERE kind = 'job' AND ref = ?)`,
			`id IN (SELECT work_item_id FROM work_item_sessions WHERE role = 'current' AND session_id = ?)`,
			`id IN (SELECT ws.work_item_id FROM work_item_sessions ws JOIN session_job_watches jw ON jw.session_id = ws.session_id
  WHERE ws.role = 'current' AND jw.job_id = ?)`)
		args = append(args, j, j, j)
	}
	if p := strings.TrimSpace(r.PlanID); p != "" {
		conds = append(conds, `id IN (SELECT work_item_id FROM work_links WHERE kind = 'plan' AND ref = ?)`)
		args = append(args, p)
	}
	if sid := strings.TrimSpace(r.SessionID); sid != "" {
		conds = append(conds, `id IN (SELECT work_item_id FROM work_item_sessions WHERE role = 'current' AND session_id = ?)`)
		args = append(args, sid)
	}
	if len(conds) == 0 {
		return []string{}, nil
	}
	rows, err := q.Query(`SELECT id FROM work_items WHERE status NOT IN ('done','dropped') AND merged_into = '' AND (`+
		strings.Join(conds, " OR ")+`) ORDER BY id`, args...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: work items for refs: %w", err)
	}
	defer rows.Close()
	out := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// noteWorkItemsLocked appends one milestone line to every open work item the refs
// belong to and touches their activity time. Called with writeMu held; it reports
// whether anything was written (the caller then announces a work change).
func (s *Store) noteWorkItemsLocked(r WorkRefs, text, by string) bool {
	ids, err := s.openWorkItemsForOn(s.db, r)
	if err != nil || len(ids) == 0 {
		return false
	}
	now := s.unixNow()
	wrote := false
	for _, id := range ids {
		if _, err := s.appendWorkJournalLvOn(s.db, id, WorkJournalNote, WorkLevelMilestone, text, by, now, ""); err != nil {
			continue
		}
		_, _ = s.db.Exec(`UPDATE work_items SET last_activity_at = ? WHERE id = ?`, now, id)
		wrote = true
	}
	return wrote
}

// answeredByLabel turns a decision / interaction responder into a journal speaker
// label: an empty / bare `human` stays `human`, a bare caller id (a web / CLI answer)
// becomes `human:<caller>`, anything already qualified (`agent:…`, `auto:…`, `human:…`,
// `steward(…)`) is kept.
func answeredByLabel(by string) string {
	by = strings.TrimSpace(by)
	switch {
	case by == "" || by == "human":
		return "human"
	case strings.ContainsAny(by, ":("):
		return by
	}
	return "human:" + by
}

// noteDecisionAnsweredLocked journals an answered plan decision on the work items its
// plan / session belongs to (WORK-06: a decision answered is a milestone). Relay turns
// (a person replying to a terminal session) are conversation, not decisions, and are
// skipped. Called with writeMu held, right after the answer was stored.
func (s *Store) noteDecisionAnsweredLocked(id, answer, answeredBy string) bool {
	d, err := scanDecision(s.db.QueryRow(selectDecisionCols+" WHERE id = ?", id))
	if err != nil || d.Kind == DecisionKindRelay {
		return false
	}
	what := d.Title
	if strings.TrimSpace(what) == "" {
		what = d.Question
	}
	text := "已回答 decision「" + clipLine(what, 60) + "」：" + clipLine(answer, 120)
	return s.noteWorkItemsLocked(WorkRefs{PlanID: d.PlanID, SessionID: d.SessionID}, text, answeredByLabel(answeredBy))
}

// noteInteractionAnsweredLocked journals a job interaction that just moved from pending
// to answered on the work items the job belongs to. Called with writeMu held.
func (s *Store) noteInteractionAnsweredLocked(rec InteractionRecord) bool {
	var planID string
	_ = s.db.QueryRow(`SELECT COALESCE(plan_id,'') FROM jobs WHERE id = ?`, rec.JobID).Scan(&planID)
	text := "已应答 job " + shortSID(rec.JobID) + " 的交互「" + clipLine(rec.Prompt, 60) + "」：" + clipLine(rec.Answer, 120)
	return s.noteWorkItemsLocked(WorkRefs{JobID: rec.JobID, PlanID: planID}, text, answeredByLabel(rec.AnsweredBy))
}

func clipLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
