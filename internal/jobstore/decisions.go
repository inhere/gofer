package jobstore

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// Decision lifecycle states (decision channel, Part C §C3). A decision starts
// OPEN; a human answer moves it to ANSWERED; a timeout moves it to EXPIRED.
// Expiry is LAZY: nothing sweeps the table; every read/answer path first runs
// expireDueDecisions so a past-deadline OPEN row surfaces as EXPIRED (plan D2).
const (
	DecisionOpen     = "OPEN"
	DecisionAnswered = "ANSWERED"
	DecisionExpired  = "EXPIRED"
)

// Decision timeout bounds (plan D4). The authoritative clamp lives in
// InsertDecision so every entry face (HTTP/CLI/MCP) is covered; entry faces may
// additionally clamp for early failure. DefaultDecisionTimeoutSec applies when
// timeout_sec <= 0 is passed.
const (
	MinDecisionTimeoutSec     = 2
	MaxDecisionTimeoutSec     = 86400
	DefaultDecisionTimeoutSec = 1800
)

// ValidDecisionState reports whether s is one of the decision states.
func ValidDecisionState(s string) bool {
	switch s {
	case DecisionOpen, DecisionAnswered, DecisionExpired:
		return true
	}
	return false
}

// PlanDecision is an agent-raised question waiting for a human answer. PlanID ""
// is a global question (no plan attached); OptionsJSON "" means free-text answer
// (an empty JSON array "[]" is normalised to "" on insert so a zero-option
// choice card can never be projected). Timestamps are unix seconds.
type PlanDecision struct {
	ID          string
	PlanID      string
	Title       string
	Question    string
	OptionsJSON string
	Answer      string
	State       string
	TimeoutSec  int64
	AskedAt     int64
	AnsweredAt  int64
	AnsweredBy  string
}

const selectDecisionCols = `SELECT id, COALESCE(plan_id,''), COALESCE(title,''),
  COALESCE(question,''), COALESCE(options_json,''), COALESCE(answer,''),
  state, COALESCE(timeout_sec,1800), asked_at,
  COALESCE(answered_at,0), COALESCE(answered_by,'')
  FROM plan_decisions`

func scanDecision(sc rowScanner) (PlanDecision, error) {
	var d PlanDecision
	err := sc.Scan(&d.ID, &d.PlanID, &d.Title, &d.Question, &d.OptionsJSON,
		&d.Answer, &d.State, &d.TimeoutSec, &d.AskedAt, &d.AnsweredAt, &d.AnsweredBy)
	return d, err
}

// decisionRandomSuffix is the jobstore-local equivalent of job.RandomSuffix
// (jobstore must stay job-import-free to avoid the job -> jobstore -> job
// cycle, see store.go). 8 lowercase hex chars from crypto/rand.
func decisionRandomSuffix() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%08x", time.Now().UnixNano()&0xffffffff)
	}
	return hex.EncodeToString(b[:])
}

// InsertDecision persists a new decision. It is the single收口 point for the
// invariants every entry face relies on (plan HIGH-2 + 二审补丁③④):
//   - an empty ID is generated here ("dec-<ts>-<rand>") because the MCP
//     standalone deployment reaches the store directly, bypassing httpapi;
//   - TimeoutSec is clamped to [2, 86400]; <= 0 falls back to the 1800s default;
//   - an empty OptionsJSON array "[]" is normalised to NULL (= free-text) so a
//     zero-option choice card (dead UI) can never be projected;
//   - AskedAt 0 is stamped with now (seconds); State "" defaults to OPEN.
func (s *Store) InsertDecision(d *PlanDecision) error {
	if d.Title == "" {
		return errors.New("jobstore: InsertDecision: empty title")
	}
	if d.Question == "" {
		return errors.New("jobstore: InsertDecision: empty question")
	}
	if d.ID == "" {
		d.ID = "dec-" + time.Now().Format("20060102-150405") + "-" + decisionRandomSuffix()
	}
	switch {
	case d.TimeoutSec <= 0:
		d.TimeoutSec = DefaultDecisionTimeoutSec
	case d.TimeoutSec < MinDecisionTimeoutSec:
		d.TimeoutSec = MinDecisionTimeoutSec
	case d.TimeoutSec > MaxDecisionTimeoutSec:
		d.TimeoutSec = MaxDecisionTimeoutSec
	}
	if d.OptionsJSON == "[]" {
		d.OptionsJSON = ""
	}
	if d.State == "" {
		d.State = DecisionOpen
	}
	if !ValidDecisionState(d.State) {
		return fmt.Errorf("jobstore: InsertDecision: invalid state %q", d.State)
	}
	if d.AskedAt == 0 {
		d.AskedAt = s.unixNow()
	}
	var planID, options any
	if d.PlanID != "" {
		planID = d.PlanID
	}
	if d.OptionsJSON != "" {
		options = d.OptionsJSON
	}
	const q = `INSERT INTO plan_decisions
  (id, plan_id, title, question, options_json, answer, state, timeout_sec, asked_at, answered_at, answered_by)
  VALUES (?,?,?,?,?,NULL,?,?,?,NULL,NULL)`
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(q, d.ID, planID, d.Title, d.Question, options,
		d.State, d.TimeoutSec, d.AskedAt); err != nil {
		return fmt.Errorf("jobstore: insert decision %q: %w", d.ID, err)
	}
	return nil
}

// GetDecision returns a decision by id. ok is false with nil error when absent.
// Lazy expiry runs first (plan D2) so a past-deadline row reads back EXPIRED.
//
// 🔒 Lock discipline (plan HIGH-1, SetTodoDone precedent): expireDueDecisions
// takes writeMu itself; callers here must NOT hold writeMu when calling it
// (sync.Mutex is not re-entrant) and take their own lock afterwards if needed.
func (s *Store) GetDecision(id string) (PlanDecision, bool, error) {
	if err := s.expireDueDecisions(); err != nil {
		return PlanDecision{}, false, err
	}
	d, err := scanDecision(s.db.QueryRow(selectDecisionCols+" WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return PlanDecision{}, false, nil
	}
	if err != nil {
		return PlanDecision{}, false, fmt.Errorf("jobstore: get decision %q: %w", id, err)
	}
	return d, true, nil
}

// ListDecisions returns decisions, optionally filtered by state ("" = all)
// and/or plan id ("" = all plans), oldest-asked first. Lazy expiry runs first
// (plan D2) so the bell never shows an "eternally OPEN" ghost card.
func (s *Store) ListDecisions(state, planID string) ([]*PlanDecision, error) {
	if state != "" && !ValidDecisionState(state) {
		return nil, fmt.Errorf("jobstore: list decisions: invalid state %q", state)
	}
	if err := s.expireDueDecisions(); err != nil {
		return nil, err
	}
	query := selectDecisionCols
	var args []any
	where := ""
	if state != "" {
		where += " state = ?"
		args = append(args, state)
	}
	if planID != "" {
		if where != "" {
			where += " AND"
		}
		where += " plan_id = ?"
		args = append(args, planID)
	}
	if where != "" {
		query += " WHERE" + where
	}
	query += " ORDER BY asked_at ASC, id ASC"
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list decisions: %w", err)
	}
	defer rows.Close()
	out := make([]*PlanDecision, 0)
	for rows.Next() {
		d, scanErr := scanDecision(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("jobstore: scan decision row: %w", scanErr)
		}
		dd := d
		out = append(out, &dd)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list decisions rows: %w", err)
	}
	return out, nil
}

// AnswerDecision records a human answer. It first runs lazy expiry (plan M2:
// answering a past-deadline decision must not succeed), then a single
// conditional UPDATE gated on state='OPEN' — a second answer, or a race with
// expiry, affects 0 rows and reports ok=false (plan D3, 验收3).
func (s *Store) AnswerDecision(id, answer, answeredBy string) (bool, error) {
	if err := s.expireDueDecisions(); err != nil {
		return false, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	const q = `UPDATE plan_decisions
  SET state='ANSWERED', answer=?, answered_at=?, answered_by=?
  WHERE id=? AND state='OPEN'`
	res, err := s.db.Exec(q, answer, s.unixNow(), answeredBy, id)
	if err != nil {
		return false, fmt.Errorf("jobstore: answer decision %q: %w", id, err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// expireDueDecisions lazily moves past-deadline OPEN decisions to EXPIRED
// (plan D2; the only author of EXPIRED — there is no active ExpireDecision, H1).
// asked_at + timeout_sec is in SECONDS (B1: never multiply by 1000).
//
// 🔒 It takes writeMu ITSELF; callers must not hold writeMu when calling it
// (plan HIGH-1: sync.Mutex is not re-entrant).
func (s *Store) expireDueDecisions() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	const q = `UPDATE plan_decisions SET state='EXPIRED'
  WHERE state='OPEN' AND asked_at + timeout_sec <= ?`
	if _, err := s.db.Exec(q, s.unixNow()); err != nil {
		return fmt.Errorf("jobstore: expire due decisions: %w", err)
	}
	return nil
}
