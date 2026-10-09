package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// W2b storage for the steward (design §14.4): the job marker, the event inbox, the review
// log and the merge suggestions. The steward notes ride on plan_handoffs (see
// StewardNotesKey) and its small state on work_kv.

// StewardNotesKey is the plan_handoffs namespace the steward notes are versioned under:
// the same storage and optimistic lock as a plan handoff. The colon keeps it out of the
// plan id space.
const StewardNotesKey = "steward:notes"

// StewardNotesMaxBytes is the hard cap of one notes version (PlanHandoffMaxBytes).
const StewardNotesMaxBytes = PlanHandoffMaxBytes

// ---------------------------------------------------------------- job marker

// MarkStewardJob records that jobID is the steward's session job. It is stamped by the
// server before the job gets its credential, and read back when a resident session is
// recovered after a restart.
func (s *Store) MarkStewardJob(jobID string) error {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return errors.New("jobstore: mark steward job: empty id")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`INSERT INTO steward_jobs(job_id, created_at) VALUES (?, ?) ON CONFLICT(job_id) DO NOTHING`, jobID, s.unixNow())
	if err != nil {
		return fmt.Errorf("jobstore: mark steward job: %w", err)
	}
	return nil
}

// IsStewardJob reports whether jobID was started as the steward.
func (s *Store) IsStewardJob(jobID string) bool {
	if s == nil || strings.TrimSpace(jobID) == "" {
		return false
	}
	var one int
	err := s.db.QueryRow(`SELECT 1 FROM steward_jobs WHERE job_id = ?`, jobID).Scan(&one)
	return err == nil
}

// ---------------------------------------------------------------- events

// Steward event kinds.
const (
	StewardEventSession = "session" // a session of an open item went offline / ended
	StewardEventDue     = "due"     // a reminder / park deadline came due
	StewardEventDrafts  = "drafts"  // many unsorted drafts piled up
)

// StewardEvent is one thing the steward should look at: noted cheaply, handled by the next
// review (or an event-driven wake). (kind, ref) is unique, so the same occurrence is noted
// once.
type StewardEvent struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"`
	Ref       string `json:"ref"`
	Detail    string `json:"detail,omitempty"`
	At        int64  `json:"at"`
	HandledAt int64  `json:"handled_at,omitempty"`
}

// AddStewardEvent notes an event; added=false when the same (kind, ref) was noted before.
func (s *Store) AddStewardEvent(kind, ref, detail string) (bool, error) {
	kind, ref = strings.TrimSpace(kind), strings.TrimSpace(ref)
	if kind == "" || ref == "" {
		return false, errors.New("jobstore: steward event needs a kind and a ref")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`INSERT INTO steward_events(kind, ref, detail, at) VALUES (?,?,?,?) ON CONFLICT(kind, ref) DO NOTHING`,
		kind, ref, capText(detail, 500), s.unixNow())
	if err != nil {
		return false, fmt.Errorf("jobstore: add steward event: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// PendingStewardEvents lists the events nobody handled yet, oldest first.
func (s *Store) PendingStewardEvents(limit int) ([]StewardEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.Query(`SELECT id, kind, ref, detail, at, handled_at FROM steward_events WHERE handled_at = 0 ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("jobstore: pending steward events: %w", err)
	}
	defer rows.Close()
	out := make([]StewardEvent, 0)
	for rows.Next() {
		var e StewardEvent
		if err := rows.Scan(&e.ID, &e.Kind, &e.Ref, &e.Detail, &e.At, &e.HandledAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// MarkStewardEventsHandled stamps the given events as handled.
func (s *Store) MarkStewardEventsHandled(ids []int64, at int64) error {
	if len(ids) == 0 {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	for _, id := range ids {
		if _, err := s.db.Exec(`UPDATE steward_events SET handled_at = ? WHERE id = ? AND handled_at = 0`, at, id); err != nil {
			return fmt.Errorf("jobstore: mark steward event handled: %w", err)
		}
	}
	return nil
}

// ---------------------------------------------------------------- reviews

// Steward review states.
const (
	StewardReviewRunning = "running"
	StewardReviewDone    = "done"
	StewardReviewSkipped = "skipped"
	StewardReviewFailed  = "failed"
)

// StewardReview is one daily / manual / event review run of the steward.
type StewardReview struct {
	ID        int64  `json:"id"`
	Day       string `json:"day"`
	State     string `json:"state"`
	Trigger   string `json:"trigger,omitempty"`
	ItemIDs   string `json:"item_ids,omitempty"`
	Summary   string `json:"summary,omitempty"`
	JobID     string `json:"job_id,omitempty"`
	StartedAt int64  `json:"started_at"`
	EndedAt   int64  `json:"ended_at,omitempty"`
	Error     string `json:"error,omitempty"`
}

// MaxStewardReviewSummary caps the stored point-of-view text of one review.
const MaxStewardReviewSummary = 2000

const selectStewardReviewCols = `SELECT id, day, state, cause, item_ids, summary, job_id, started_at, ended_at, error FROM steward_reviews`

func scanStewardReview(sc rowScanner) (StewardReview, error) {
	var r StewardReview
	err := sc.Scan(&r.ID, &r.Day, &r.State, &r.Trigger, &r.ItemIDs, &r.Summary, &r.JobID, &r.StartedAt, &r.EndedAt, &r.Error)
	return r, err
}

// BeginStewardReview records a review run in state running and returns its id.
func (s *Store) BeginStewardReview(day, trigger string, itemIDs []string) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`INSERT INTO steward_reviews(day, state, cause, item_ids, started_at) VALUES (?,?,?,?,?)`,
		day, StewardReviewRunning, trigger, strings.Join(itemIDs, ","), s.unixNow())
	if err != nil {
		return 0, fmt.Errorf("jobstore: begin steward review: %w", err)
	}
	return res.LastInsertId()
}

// FinishStewardReview closes a review run. An empty summary keeps what the steward already
// wrote through SetStewardReviewSummary.
func (s *Store) FinishStewardReview(id int64, state, summary, jobID, errText string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`UPDATE steward_reviews SET state = ?, summary = CASE WHEN ? <> '' THEN ? ELSE summary END,
  job_id = CASE WHEN ? <> '' THEN ? ELSE job_id END, ended_at = ?, error = ? WHERE id = ?`,
		state, summary, capText(summary, MaxStewardReviewSummary), jobID, jobID, s.unixNow(), capText(errText, 500), id)
	if err != nil {
		return fmt.Errorf("jobstore: finish steward review: %w", err)
	}
	return nil
}

// SetStewardReviewSummary stores the steward's point-of-view text on the newest running
// review (or, when none is running, on today's newest done one) and reports which review
// took it.
func (s *Store) SetStewardReviewSummary(day, summary string) (StewardReview, error) {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return StewardReview{}, fmt.Errorf("%w: summary required", ErrWorkInvalid)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	r, err := scanStewardReview(s.db.QueryRow(selectStewardReviewCols+` WHERE state = ? ORDER BY id DESC LIMIT 1`, StewardReviewRunning))
	if errors.Is(err, sql.ErrNoRows) {
		r, err = scanStewardReview(s.db.QueryRow(selectStewardReviewCols+` WHERE day = ? AND state = ? ORDER BY id DESC LIMIT 1`, day, StewardReviewDone))
	}
	if errors.Is(err, sql.ErrNoRows) {
		return StewardReview{}, ErrStewardNoReview
	}
	if err != nil {
		return StewardReview{}, err
	}
	r.Summary = capText(summary, MaxStewardReviewSummary)
	if _, err := s.db.Exec(`UPDATE steward_reviews SET summary = ? WHERE id = ?`, r.Summary, r.ID); err != nil {
		return StewardReview{}, fmt.Errorf("jobstore: set steward review summary: %w", err)
	}
	return r, nil
}

// ErrStewardNoReview is returned when a review summary arrives with no review to attach to.
var ErrStewardNoReview = errors.New("no steward review is running or finished for today")

// LastStewardReview returns the newest review that actually ran (not skipped).
func (s *Store) LastStewardReview() (StewardReview, bool, error) {
	r, err := scanStewardReview(s.db.QueryRow(selectStewardReviewCols + ` WHERE state <> 'skipped' ORDER BY id DESC LIMIT 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return StewardReview{}, false, nil
	}
	return r, err == nil, err
}

// StewardReviewComment returns the point-of-view text of the newest done review of day
// ("" when none): what the daily digest appends as the steward's comment.
func (s *Store) StewardReviewComment(day string) (string, error) {
	var summary string
	err := s.db.QueryRow(`SELECT summary FROM steward_reviews WHERE day = ? AND state = ? AND summary <> '' ORDER BY id DESC LIMIT 1`,
		day, StewardReviewDone).Scan(&summary)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return summary, err
}

// ListStewardReviews lists review runs newest first.
func (s *Store) ListStewardReviews(limit int) ([]StewardReview, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	rows, err := s.db.Query(selectStewardReviewCols+` ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list steward reviews: %w", err)
	}
	defer rows.Close()
	out := make([]StewardReview, 0)
	for rows.Next() {
		r, err := scanStewardReview(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// FailStaleStewardReviews closes reviews left running by a restart (started before cutoff).
func (s *Store) FailStaleStewardReviews(cutoff int64) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`UPDATE steward_reviews SET state = ?, ended_at = ?, error = 'interrupted' WHERE state = ? AND started_at < ?`,
		StewardReviewFailed, s.unixNow(), StewardReviewRunning, cutoff)
	return err
}

// ---------------------------------------------------------------- merge suggestions

// Merge suggestion states.
const (
	MergeSuggestPending   = "pending"
	MergeSuggestAccepted  = "accepted"
	MergeSuggestDismissed = "dismissed"
)

// ErrMergeSuggestionNotFound is returned for an unknown suggestion id.
var ErrMergeSuggestionNotFound = errors.New("merge suggestion not found")

// WorkMergeSuggestion is a recorded "these two items look like one thing" for a person to
// confirm. Nothing merges until a person accepts it.
type WorkMergeSuggestion struct {
	ID       int64  `json:"id"`
	TargetID string `json:"target_id"`
	SourceID string `json:"source_id"`
	Reason   string `json:"reason,omitempty"`
	By       string `json:"by"`
	At       int64  `json:"at"`
	State    string `json:"state"`
}

// AddWorkMergeSuggestion records a pending suggestion (source into target). The same
// pending pair is not recorded twice (the existing one is returned, stored=false). It
// journals a line on both items so the card shows it.
func (s *Store) AddWorkMergeSuggestion(targetID, sourceID, reason, by string) (WorkMergeSuggestion, bool, error) {
	targetID, sourceID = strings.TrimSpace(targetID), strings.TrimSpace(sourceID)
	if targetID == "" || sourceID == "" || targetID == sourceID {
		return WorkMergeSuggestion{}, false, fmt.Errorf("%w: a merge suggestion needs two different work items", ErrWorkInvalid)
	}
	by = strings.TrimSpace(by)
	if by == "" {
		by = "system"
	}
	defer s.emit(Change{Kind: ChangeWork, ID: targetID})
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	for _, id := range []string{targetID, sourceID} {
		w, ok, err := s.getWorkItemOn(s.db, id)
		if err != nil {
			return WorkMergeSuggestion{}, false, err
		}
		if !ok {
			return WorkMergeSuggestion{}, false, ErrWorkItemNotFound
		}
		if w.MergedInto != "" || WorkStatusFinal(w.Status) {
			return WorkMergeSuggestion{}, false, fmt.Errorf("%w: work item %s is already closed or merged", ErrWorkInvalid, id)
		}
	}
	var ex WorkMergeSuggestion
	err := s.db.QueryRow(`SELECT id, target_id, source_id, reason, by, at, state FROM work_merge_suggestions
  WHERE state = 'pending' AND ((target_id = ? AND source_id = ?) OR (target_id = ? AND source_id = ?)) LIMIT 1`,
		targetID, sourceID, sourceID, targetID).Scan(&ex.ID, &ex.TargetID, &ex.SourceID, &ex.Reason, &ex.By, &ex.At, &ex.State)
	if err == nil {
		return ex, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return WorkMergeSuggestion{}, false, err
	}
	now := s.unixNow()
	sg := WorkMergeSuggestion{TargetID: targetID, SourceID: sourceID, Reason: capText(reason, 500), By: by, At: now, State: MergeSuggestPending}
	res, err := s.db.Exec(`INSERT INTO work_merge_suggestions(target_id, source_id, reason, by, at, state) VALUES (?,?,?,?,?,?)`,
		sg.TargetID, sg.SourceID, sg.Reason, sg.By, sg.At, sg.State)
	if err != nil {
		return WorkMergeSuggestion{}, false, fmt.Errorf("jobstore: add merge suggestion: %w", err)
	}
	sg.ID, _ = res.LastInsertId()
	line := fmt.Sprintf("合并建议（待确认）：%s 并入 %s", sourceID, targetID)
	if sg.Reason != "" {
		line += "；理由：" + sg.Reason
	}
	for _, id := range []string{targetID, sourceID} {
		if _, err := s.appendWorkJournalOn(s.db, id, WorkJournalSteward, line, by, now, ""); err != nil {
			return WorkMergeSuggestion{}, false, err
		}
	}
	return sg, true, nil
}

// ListWorkMergeSuggestions lists suggestions in the given state ("" = pending), newest first.
func (s *Store) ListWorkMergeSuggestions(state string) ([]WorkMergeSuggestion, error) {
	if state == "" {
		state = MergeSuggestPending
	}
	rows, err := s.db.Query(`SELECT id, target_id, source_id, reason, by, at, state FROM work_merge_suggestions WHERE state = ? ORDER BY id DESC LIMIT 200`, state)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list merge suggestions: %w", err)
	}
	defer rows.Close()
	out := make([]WorkMergeSuggestion, 0)
	for rows.Next() {
		var g WorkMergeSuggestion
		if err := rows.Scan(&g.ID, &g.TargetID, &g.SourceID, &g.Reason, &g.By, &g.At, &g.State); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// GetWorkMergeSuggestion reads one suggestion.
func (s *Store) GetWorkMergeSuggestion(id int64) (WorkMergeSuggestion, error) {
	var g WorkMergeSuggestion
	err := s.db.QueryRow(`SELECT id, target_id, source_id, reason, by, at, state FROM work_merge_suggestions WHERE id = ?`, id).
		Scan(&g.ID, &g.TargetID, &g.SourceID, &g.Reason, &g.By, &g.At, &g.State)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkMergeSuggestion{}, ErrMergeSuggestionNotFound
	}
	return g, err
}

// ResolveWorkMergeSuggestion moves a pending suggestion to accepted / dismissed.
func (s *Store) ResolveWorkMergeSuggestion(id int64, state string) error {
	if state != MergeSuggestAccepted && state != MergeSuggestDismissed {
		return fmt.Errorf("%w: invalid suggestion state %q", ErrWorkInvalid, state)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE work_merge_suggestions SET state = ? WHERE id = ? AND state = 'pending'`, state, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrMergeSuggestionNotFound
	}
	return nil
}

// ---------------------------------------------------------------- recent journal

// ListRecentWorkJournal returns journal entries written at or after `since` across every
// open work item, newest first (the steward prime's 24h digest reads it).
func (s *Store) ListRecentWorkJournal(since int64, limit int) ([]WorkJournalEntry, error) {
	if limit <= 0 || limit > 2000 {
		limit = 300
	}
	rows, err := s.db.Query(`SELECT j.id, j.work_item_id, j.kind, j.text, j.by, j.at, j.origin_item, j.level FROM work_journal j
  JOIN work_items w ON w.id = j.work_item_id WHERE j.at >= ? AND w.merged_into = '' ORDER BY j.id DESC LIMIT ?`, since, limit)
	if err != nil {
		return nil, fmt.Errorf("jobstore: recent work journal: %w", err)
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
	return out, rows.Err()
}
