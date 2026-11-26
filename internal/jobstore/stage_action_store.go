package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// The autonomy level and the "would do" record of the stage flow (plan
// 2026-11-26-flow-progression-plan.md, F4). Both levels write, at every step change, one
// WorkJournalStageAction line naming what the level would do next; `stage report` later
// compares those lines with what actually happened. No table of its own: the lines live in
// work_journal like every other stage event.

// StageLevelRemind sends the current session a "continue" turn when the next step is the
// agent's (see StageLevelRecord for the other level).
const StageLevelRemind = "remind"

// WorkJournalStageAction is the journal kind of a "would do" line. Like WorkJournalStage it
// is written by the stage store only.
const WorkJournalStageAction = "stage_action"

// ValidStageLevel reports whether level is a stage autonomy level.
func ValidStageLevel(level string) bool {
	return level == StageLevelRecord || level == StageLevelRemind
}

// stageActionTx writes the "would do" line of a step change ("" writes nothing).
func (s *Store) stageActionTx(tx *sql.Tx, id, text, by string, now int64) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if _, err := s.appendWorkJournalLvOn(tx, id, WorkJournalStageAction, WorkLevelDetail, text, by, now, ""); err != nil {
		return err
	}
	return nil
}

// SetWorkStageLevel sets the autonomy level of a flow in progress and journals the change.
// ErrStageNotFound when the item has no flow, ErrStageClosed when it is finished.
func (s *Store) SetWorkStageLevel(id, level, by string) (WorkStage, error) {
	id, level = strings.TrimSpace(id), strings.TrimSpace(level)
	if !ValidStageLevel(level) {
		return WorkStage{}, fmt.Errorf("%w: stage level must be %s or %s (got %q)", ErrWorkInvalid, StageLevelRecord, StageLevelRemind, level)
	}
	defer s.emit(Change{Kind: ChangeWork, ID: id})
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return WorkStage{}, fmt.Errorf("jobstore: set stage level: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	prev, err := scanWorkStage(tx.QueryRow(selectStageCols+` WHERE work_item_id = ?`, id))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return WorkStage{}, ErrStageNotFound
	case err != nil:
		return WorkStage{}, fmt.Errorf("jobstore: set stage level: %w", err)
	case !prev.Open():
		return WorkStage{}, ErrStageClosed
	}
	if prev.Level != level {
		now := s.unixNow()
		if _, err := tx.Exec(`UPDATE work_stages SET level = ?, updated_at = ? WHERE work_item_id = ?`, level, now, id); err != nil {
			return WorkStage{}, fmt.Errorf("jobstore: set stage level: %w", err)
		}
		if err := s.stageJournalTx(tx, id, fmt.Sprintf("stage level: %s > %s", prev.Level, level), by, WorkLevelMilestone, now); err != nil {
			return WorkStage{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return WorkStage{}, fmt.Errorf("jobstore: set stage level: commit: %w", err)
	}
	w, _, err := s.GetWorkStage(id)
	return w, err
}

// StageJournalLine is one `stage` / `stage_action` journal line, with the project of its
// work item, as the report reads them.
type StageJournalLine struct {
	ID         int64
	WorkItemID string
	ProjectKey string
	Kind       string
	Text       string
	By         string
	At         int64
}

// ListStageJournal returns the stage and stage_action lines of one work item (workItemID !=
// "") or of every work item of a project (project != ""; both "" = every item), grouped by
// item and oldest first inside an item. Only items that have a stage_action line are read.
func (s *Store) ListStageJournal(workItemID, project string) ([]StageJournalLine, error) {
	q := `SELECT j.id, j.work_item_id, w.project_key, j.kind, j.text, j.by, j.at FROM work_journal j
  JOIN work_items w ON w.id = j.work_item_id
  WHERE j.kind IN ('stage','stage_action')
  AND j.work_item_id IN (SELECT work_item_id FROM work_journal WHERE kind = 'stage_action')`
	var args []any
	if workItemID = strings.TrimSpace(workItemID); workItemID != "" {
		q += ` AND j.work_item_id = ?`
		args = append(args, workItemID)
	}
	if project = strings.TrimSpace(project); project != "" {
		q += ` AND w.project_key = ?`
		args = append(args, project)
	}
	rows, err := s.db.Query(q+` ORDER BY j.work_item_id, j.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list stage journal: %w", err)
	}
	defer rows.Close()
	out := make([]StageJournalLine, 0)
	for rows.Next() {
		var l StageJournalLine
		if err := rows.Scan(&l.ID, &l.WorkItemID, &l.ProjectKey, &l.Kind, &l.Text, &l.By, &l.At); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// StagePendingPrefix starts the schedule_id of a seat_pending_messages row that holds a stage
// reminder instead of a seat schedule's text: "stage:<work item id>". The row's
// schedule_name is the step the reminder is for. One live row per (seat, work item), so only
// the latest reminder of a flow waits.
const StagePendingPrefix = "stage:"

// StagePendingID is the schedule_id of the queued reminder of a work item.
func StagePendingID(workItemID string) string { return StagePendingPrefix + workItemID }

// StagePendingWorkItem returns the work item a queued row's schedule_id names; ok=false for
// the row of an ordinary schedule.
func StagePendingWorkItem(scheduleID string) (string, bool) {
	id, ok := strings.CutPrefix(scheduleID, StagePendingPrefix)
	return id, ok && id != ""
}
