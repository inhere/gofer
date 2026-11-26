package jobstore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// The stage flow's storage (design 2026-11-18 §6, plan 2026-11-26-flow-progression-plan.md):
// one row of work_stages per work item, the template's steps snapshotted into steps_json
// when the flow starts. Every step change and gate result is written to work_journal as a
// `stage` line in the same transaction as the change; there is no event table of its own.

// Stage statuses (work_stages.status).
const (
	StageActive  = "active"
	StageBlocked = "blocked"
	StageDone    = "done"
	StageDropped = "dropped"
)

// Stage autonomy levels (work_stages.level): record only notes what it would do, remind also
// sends the session a "continue" turn when the next step is the agent's.
const StageLevelRecord = "record"

// WorkJournalStage is the journal kind of step changes and gate results. It is written by
// the stage store only, so it is not accepted from the journal write route.
const WorkJournalStage = "stage"

// Roles of a doc link and of a job link.
const (
	WorkDocRoleDesign = "design"
	WorkDocRolePlan   = "plan"
	WorkJobRoleReview = "review"
)

// Stage errors surfaced to the entry layers.
var (
	ErrStageNotFound = errors.New("work item has no stage flow")
	ErrStageActive   = errors.New("work item already has a stage flow in progress")
	ErrStageClosed   = errors.New("stage flow is already finished")
)

// WorkStage is the flow of one work item. Step indexes Steps. ApprovedStep is the step a
// person approved (-1 = none); it is reset whenever the flow enters a step.
type WorkStage struct {
	WorkItemID    string   `json:"work_item_id"`
	Template      string   `json:"template"`
	Steps         []string `json:"steps"`
	Step          int      `json:"step"`
	StepEnteredAt int64    `json:"step_entered_at"`
	Level         string   `json:"level"`
	Status        string   `json:"status"`
	BlockedReason string   `json:"blocked_reason,omitempty"`
	DoingDoneSHA  string   `json:"doing_done_sha,omitempty"`
	ApprovedStep  int      `json:"approved_step"`
	// ReviewMax is the review round cap and PlanDoc whether the plan step needs a committed
	// plan document; both are snapshotted from the template like the steps.
	ReviewMax int   `json:"review_max"`
	PlanDoc   bool  `json:"plan_doc"`
	CreatedAt int64 `json:"created_at"`
	UpdatedAt int64 `json:"updated_at"`
}

// StepName is the name of the current step ("" when Step is out of range).
func (w WorkStage) StepName() string {
	if w.Step < 0 || w.Step >= len(w.Steps) {
		return ""
	}
	return w.Steps[w.Step]
}

// Open reports whether the flow is still in progress (active or blocked).
func (w WorkStage) Open() bool { return w.Status == StageActive || w.Status == StageBlocked }

const selectStageCols = `SELECT work_item_id, template, steps_json, step, step_entered_at, level, status, blocked_reason,
  doing_done_sha, approved_step, review_max, plan_doc, created_at, updated_at FROM work_stages`

func scanWorkStage(sc rowScanner) (WorkStage, error) {
	var w WorkStage
	var stepsJSON string
	var planDoc int
	if err := sc.Scan(&w.WorkItemID, &w.Template, &stepsJSON, &w.Step, &w.StepEnteredAt, &w.Level, &w.Status,
		&w.BlockedReason, &w.DoingDoneSHA, &w.ApprovedStep, &w.ReviewMax, &planDoc, &w.CreatedAt, &w.UpdatedAt); err != nil {
		return WorkStage{}, err
	}
	w.PlanDoc = planDoc != 0
	if err := json.Unmarshal([]byte(stepsJSON), &w.Steps); err != nil || w.Steps == nil {
		w.Steps = []string{}
	}
	return w, nil
}

// GetWorkStage reads the flow of a work item; ok=false when it has none.
func (s *Store) GetWorkStage(id string) (WorkStage, bool, error) {
	w, err := scanWorkStage(s.db.QueryRow(selectStageCols+` WHERE work_item_id = ?`, strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return WorkStage{}, false, nil
	}
	if err != nil {
		return WorkStage{}, false, fmt.Errorf("jobstore: get work stage %q: %w", id, err)
	}
	return w, true, nil
}

// StartWorkStage begins a flow on a work item (Steps / Template / ReviewMax / PlanDoc set by
// the caller). A finished (done / dropped) flow is replaced; one in progress is
// ErrStageActive. The first step is entered now.
func (s *Store) StartWorkStage(in WorkStage, by string) (WorkStage, error) {
	id := strings.TrimSpace(in.WorkItemID)
	if id == "" || len(in.Steps) == 0 || strings.TrimSpace(in.Template) == "" {
		return WorkStage{}, fmt.Errorf("%w: a stage flow needs a work item, a template and steps", ErrWorkInvalid)
	}
	stepsJSON, err := json.Marshal(in.Steps)
	if err != nil {
		return WorkStage{}, fmt.Errorf("jobstore: encode stage steps: %w", err)
	}
	defer s.emit(Change{Kind: ChangeWork, ID: id})
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, ok, err := s.getWorkItemOn(s.db, id); err != nil {
		return WorkStage{}, err
	} else if !ok {
		return WorkStage{}, ErrWorkItemNotFound
	}
	tx, err := s.db.Begin()
	if err != nil {
		return WorkStage{}, fmt.Errorf("jobstore: start work stage: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	prev, err := scanWorkStage(tx.QueryRow(selectStageCols+` WHERE work_item_id = ?`, id))
	switch {
	case err == nil && prev.Open():
		return WorkStage{}, ErrStageActive
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return WorkStage{}, fmt.Errorf("jobstore: start work stage: %w", err)
	}
	now := s.unixNow()
	level := in.Level
	if level == "" {
		level = StageLevelRecord
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO work_stages(work_item_id, template, steps_json, step, step_entered_at, level,
  status, blocked_reason, doing_done_sha, approved_step, review_max, plan_doc, created_at, updated_at)
  VALUES (?,?,?,0,?,?,?, '', '', -1, ?,?,?,?)`,
		id, in.Template, string(stepsJSON), now, level, StageActive, in.ReviewMax, boolInt(in.PlanDoc), now, now); err != nil {
		return WorkStage{}, fmt.Errorf("jobstore: start work stage: %w", err)
	}
	text := fmt.Sprintf("stage start: %s [%s] at %s", in.Template, strings.Join(in.Steps, " > "), in.Steps[0])
	if err := s.stageJournalTx(tx, id, text, by, WorkLevelMilestone, now); err != nil {
		return WorkStage{}, err
	}
	if err := tx.Commit(); err != nil {
		return WorkStage{}, fmt.Errorf("jobstore: start work stage: commit: %w", err)
	}
	w, _, err := s.GetWorkStage(id)
	return w, err
}

// StageTransition is one conditional change of a flow. It applies only while the flow is
// still at FromStep and in one of Statuses (default: active or blocked), which is what makes
// two concurrent `done` calls advance the flow once: the loser's update matches no row.
// Optional fields (pointers) are left alone when nil.
type StageTransition struct {
	WorkItemID string
	FromStep   int
	Statuses   []string
	// ToStep != FromStep enters a new step (step_entered_at is reset, the approval cleared).
	ToStep int
	// ToStatus is the status after the change.
	ToStatus      string
	BlockedReason string
	DoingDoneSHA  *string
	ApprovedStep  *int
	// Journal is the `stage` line written in the same transaction; Level "" = milestone.
	Journal string
	Level   string
	By      string
	// Action, when set, is the "would do" line (WorkJournalStageAction) recorded with the
	// change in the same transaction: what the autonomy level would do about the new step.
	Action string
}

// TransitionWorkStage applies t. applied=false means the flow was no longer at FromStep (or
// no longer open): someone else moved it first, and nothing was written.
func (s *Store) TransitionWorkStage(t StageTransition) (applied bool, err error) {
	id := strings.TrimSpace(t.WorkItemID)
	defer s.emit(Change{Kind: ChangeWork, ID: id})
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	statuses := t.Statuses
	if len(statuses) == 0 {
		statuses = []string{StageActive, StageBlocked}
	}
	now := s.unixNow()
	set := []string{"status = ?", "blocked_reason = ?", "updated_at = ?"}
	args := []any{t.ToStatus, t.BlockedReason, now}
	if t.ToStep != t.FromStep {
		set = append(set, "step = ?", "step_entered_at = ?", "approved_step = -1")
		args = append(args, t.ToStep, now)
	}
	if t.DoingDoneSHA != nil {
		set = append(set, "doing_done_sha = ?")
		args = append(args, *t.DoingDoneSHA)
	}
	if t.ApprovedStep != nil {
		set = append(set, "approved_step = ?")
		args = append(args, *t.ApprovedStep)
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(statuses)), ",")
	args = append(args, id, t.FromStep)
	for _, st := range statuses {
		args = append(args, st)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return false, fmt.Errorf("jobstore: stage transition: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.Exec(`UPDATE work_stages SET `+strings.Join(set, ", ")+` WHERE work_item_id = ? AND step = ? AND status IN (`+marks+`)`, args...)
	if err != nil {
		return false, fmt.Errorf("jobstore: stage transition: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	level := t.Level
	if level == "" {
		level = WorkLevelMilestone
	}
	if err := s.stageJournalTx(tx, id, t.Journal, t.By, level, now); err != nil {
		return false, err
	}
	if err := s.stageActionTx(tx, id, t.Action, t.By, now); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("jobstore: stage transition: commit: %w", err)
	}
	return true, nil
}

// AppendStageJournal writes a `stage` line that changes no state (a gate result).
func (s *Store) AppendStageJournal(id, text, by, level string) error {
	id = strings.TrimSpace(id)
	defer s.emit(Change{Kind: ChangeWork, ID: id})
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if level == "" {
		level = WorkLevelDetail
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("jobstore: stage journal: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.stageJournalTx(tx, id, text, by, level, s.unixNow()); err != nil {
		return err
	}
	return tx.Commit()
}

// stageJournalTx writes one stage line and touches the item's activity time.
func (s *Store) stageJournalTx(tx *sql.Tx, id, text, by, level string, now int64) error {
	if _, err := s.appendWorkJournalLvOn(tx, id, WorkJournalStage, level, text, by, now, ""); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE work_items SET last_activity_at=?, rev=rev+1 WHERE id=?`, now, id); err != nil {
		return fmt.Errorf("jobstore: touch work item: %w", err)
	}
	return nil
}

// WorkStagesForSession returns the open flows of the work items the session is currently on,
// newest change first.
func (s *Store) WorkStagesForSession(sid string) ([]WorkStage, error) {
	return s.queryWorkStages(selectStageCols+` WHERE status IN ('active','blocked') AND work_item_id IN
  (SELECT work_item_id FROM work_item_sessions WHERE session_id = ? AND role = 'current') ORDER BY updated_at DESC, work_item_id`, strings.TrimSpace(sid))
}

// ListOpenWorkStages returns the flows in progress, newest change first; project != ""
// keeps the ones whose work item belongs to that project.
func (s *Store) ListOpenWorkStages(project string) ([]WorkStage, error) {
	q := selectStageCols + ` WHERE status IN ('active','blocked')`
	var args []any
	if project = strings.TrimSpace(project); project != "" {
		q += ` AND work_item_id IN (SELECT id FROM work_items WHERE project_key = ?)`
		args = append(args, project)
	}
	return s.queryWorkStages(q+` ORDER BY updated_at DESC, work_item_id`, args...)
}

func (s *Store) queryWorkStages(q string, args ...any) ([]WorkStage, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list work stages: %w", err)
	}
	defer rows.Close()
	out := make([]WorkStage, 0)
	for rows.Next() {
		w, err := scanWorkStage(rows)
		if err != nil {
			return nil, fmt.Errorf("jobstore: scan work stage: %w", err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// migrateWorkLinkRole adds work_links.role to a database written by an older binary. The
// base schema does not mention the column (G057): only this migration may.
func (s *Store) migrateWorkLinkRole() error {
	cols, err := s.tableColumns("work_links")
	if err != nil {
		return err
	}
	if _, ok := cols["role"]; ok {
		return nil
	}
	if _, err := s.db.Exec(`ALTER TABLE work_links ADD COLUMN role TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("jobstore: migrate work_links add role: %w", err)
	}
	return nil
}

// ValidWorkLinkRole reports whether role is acceptable for a link of kind: a doc link needs
// design or plan, a job link may carry review, every other kind carries none.
func ValidWorkLinkRole(kind, role string) bool {
	switch kind {
	case WorkLinkDoc:
		return role == WorkDocRoleDesign || role == WorkDocRolePlan
	case WorkLinkJob:
		return role == "" || role == WorkJobRoleReview
	}
	return role == ""
}

// AddWorkLinkRole is AddWorkLink for a link that carries a role (doc: design | plan, job:
// review). The role is part of the link, not of its key: linking the same (kind, ref) again
// with another role changes the role.
func (s *Store) AddWorkLinkRole(id, kind, ref, role, by string) (WorkLink, error) {
	id, kind, ref, role = strings.TrimSpace(id), strings.TrimSpace(kind), strings.TrimSpace(ref), strings.TrimSpace(role)
	if !validWorkLinkKind(kind) || ref == "" {
		return WorkLink{}, fmt.Errorf("%w: link needs kind issue|plan|todo|job|doc and a ref", ErrWorkInvalid)
	}
	if !ValidWorkLinkRole(kind, role) {
		return WorkLink{}, fmt.Errorf("%w: a %s link cannot have role %q (doc: design|plan, job: review)", ErrWorkInvalid, kind, role)
	}
	if role == "" {
		return s.AddWorkLink(id, kind, ref, by)
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
	var prev string
	err := s.db.QueryRow(`SELECT role FROM work_links WHERE work_item_id=? AND kind=? AND ref=?`, id, kind, ref).Scan(&prev)
	switch {
	case err == nil && prev == role:
		return WorkLink{WorkItemID: id, Kind: kind, Ref: ref, Role: role}, nil
	case err == nil:
		if _, err := s.db.Exec(`UPDATE work_links SET role=? WHERE work_item_id=? AND kind=? AND ref=?`, role, id, kind, ref); err != nil {
			return WorkLink{}, fmt.Errorf("jobstore: set work link role: %w", err)
		}
	case errors.Is(err, sql.ErrNoRows):
		if _, err := s.db.Exec(`INSERT INTO work_links(work_item_id, kind, ref, created_at, role) VALUES (?,?,?,?,?)`, id, kind, ref, now, role); err != nil {
			return WorkLink{}, fmt.Errorf("jobstore: add work link: %w", err)
		}
	default:
		return WorkLink{}, fmt.Errorf("jobstore: add work link: %w", err)
	}
	if _, err := s.appendWorkJournalOn(s.db, id, WorkJournalLink, fmt.Sprintf("Linked %s (%s) %s", kind, role, ref), by, now, ""); err != nil {
		return WorkLink{}, err
	}
	if _, err := s.db.Exec(`UPDATE work_items SET last_activity_at=?, rev=rev+1 WHERE id=?`, now, id); err != nil {
		return WorkLink{}, err
	}
	return WorkLink{WorkItemID: id, Kind: kind, Ref: ref, CreatedAt: now, Role: role}, nil
}

// ListWorkLinksRole is ListWorkLinks with each link's role (and, optionally, only links of
// one kind: kind "" = every kind).
func (s *Store) ListWorkLinksRole(id, kind string) ([]WorkLink, error) {
	q := `SELECT work_item_id, kind, ref, created_at, role FROM work_links WHERE work_item_id=?`
	args := []any{strings.TrimSpace(id)}
	if kind != "" {
		q += ` AND kind=?`
		args = append(args, kind)
	}
	rows, err := s.db.Query(q+` ORDER BY created_at, kind, ref`, args...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list work links: %w", err)
	}
	defer rows.Close()
	out := make([]WorkLink, 0)
	for rows.Next() {
		var l WorkLink
		if err := rows.Scan(&l.WorkItemID, &l.Kind, &l.Ref, &l.CreatedAt, &l.Role); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// WorkItemsLinkedToIssue returns the open work items an issue is linked to, newest
// activity first (merged-away and finished items are skipped).
func (s *Store) WorkItemsLinkedToIssue(issueID string) ([]WorkItem, error) {
	rows, err := s.db.Query(selectWorkCols+` WHERE id IN (SELECT work_item_id FROM work_links WHERE kind=? AND ref=?)
  AND status NOT IN ('done','dropped') AND merged_into = '' ORDER BY last_activity_at DESC, id`, WorkLinkIssue, strings.TrimSpace(issueID))
	if err != nil {
		return nil, fmt.Errorf("jobstore: work items of issue: %w", err)
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
