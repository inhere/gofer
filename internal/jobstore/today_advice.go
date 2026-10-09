package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// N3 T4 管家建议 (design docs/design/2026-10-09-n3-today-decision-home-design.md §5): the
// steward's one-line advice on a 「今天」 decision card, keyed by the card key. The row is
// pruned once its card is gone (internal/today does that while building the queue).

// TodayAdviceAudit is the audit_events kind written each time advice is recorded: the
// status bar's 「管家今日」 counts the distinct cards advised today from it (the advice rows
// themselves are pruned as cards get handled).
const TodayAdviceAudit = "today.advice"

// decisionAdviceSchema is additive (IF NOT EXISTS) and appended to schemaStmts below so
// the table lands on fresh and existing databases alike.
var decisionAdviceSchema = []string{
	`CREATE TABLE IF NOT EXISTS decision_advice (
  card_key  TEXT PRIMARY KEY,
  text      TEXT NOT NULL DEFAULT '',
  action_id TEXT NOT NULL DEFAULT '',
  digest    TEXT NOT NULL DEFAULT '',
  by        TEXT NOT NULL DEFAULT '',
  at        INTEGER NOT NULL,
  job_id    TEXT NOT NULL DEFAULT ''
)`,
}

func init() { schemaStmts = append(schemaStmts, decisionAdviceSchema...) }

// DecisionAdvice is one decision_advice row.
type DecisionAdvice struct {
	CardKey  string `json:"card_key"`
	Text     string `json:"text"`
	ActionID string `json:"action_id,omitempty"`
	Digest   string `json:"digest,omitempty"`
	By       string `json:"by,omitempty"`
	At       int64  `json:"at"`
	JobID    string `json:"job_id,omitempty"`
}

// UpsertDecisionAdvice writes (replaces) the advice of one card; At defaults to now.
func (s *Store) UpsertDecisionAdvice(a DecisionAdvice) (DecisionAdvice, error) {
	a.CardKey = strings.TrimSpace(a.CardKey)
	if a.CardKey == "" {
		return DecisionAdvice{}, fmt.Errorf("%w: card_key is required", ErrWorkInvalid)
	}
	if a.At == 0 {
		a.At = s.unixNow()
	}
	// The 「今天」 page refreshes on the work topic; emit after the write lock is released.
	defer s.emit(Change{Kind: ChangeWork})
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`INSERT INTO decision_advice (card_key,text,action_id,digest,by,at,job_id) VALUES (?,?,?,?,?,?,?)
  ON CONFLICT(card_key) DO UPDATE SET text=excluded.text, action_id=excluded.action_id, digest=excluded.digest,
  by=excluded.by, at=excluded.at, job_id=excluded.job_id`,
		a.CardKey, a.Text, a.ActionID, a.Digest, a.By, a.At, a.JobID)
	if err != nil {
		return DecisionAdvice{}, fmt.Errorf("jobstore: upsert decision advice: %w", err)
	}
	return a, nil
}

// GetDecisionAdvice reads one card's advice; ok is false when there is none.
func (s *Store) GetDecisionAdvice(cardKey string) (DecisionAdvice, bool, error) {
	var a DecisionAdvice
	err := s.db.QueryRow(`SELECT card_key,text,action_id,digest,by,at,job_id FROM decision_advice WHERE card_key=?`, cardKey).
		Scan(&a.CardKey, &a.Text, &a.ActionID, &a.Digest, &a.By, &a.At, &a.JobID)
	if errors.Is(err, sql.ErrNoRows) {
		return DecisionAdvice{}, false, nil
	}
	if err != nil {
		return DecisionAdvice{}, false, fmt.Errorf("jobstore: get decision advice: %w", err)
	}
	return a, true, nil
}

// ListDecisionAdvice returns every advice row, oldest first.
func (s *Store) ListDecisionAdvice() ([]DecisionAdvice, error) {
	rows, err := s.db.Query(`SELECT card_key,text,action_id,digest,by,at,job_id FROM decision_advice ORDER BY at ASC, card_key ASC`)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list decision advice: %w", err)
	}
	defer rows.Close()
	out := make([]DecisionAdvice, 0)
	for rows.Next() {
		var a DecisionAdvice
		if err := rows.Scan(&a.CardKey, &a.Text, &a.ActionID, &a.Digest, &a.By, &a.At, &a.JobID); err != nil {
			return nil, fmt.Errorf("jobstore: scan decision advice: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// DeleteDecisionAdvice removes the advice of the given cards (missing keys are ignored).
func (s *Store) DeleteDecisionAdvice(cardKeys ...string) error {
	if len(cardKeys) == 0 {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	for _, k := range cardKeys {
		if _, err := s.db.Exec(`DELETE FROM decision_advice WHERE card_key=?`, k); err != nil {
			return fmt.Errorf("jobstore: delete decision advice: %w", err)
		}
	}
	return nil
}

// CountAuditTargetsSince counts the distinct targets of one audit kind written at or after
// since by an actor starting with actorPrefix ("" = anyone).
func (s *Store) CountAuditTargetsSince(kind, actorPrefix string, since int64) (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(DISTINCT target_id) FROM audit_events WHERE kind=? AND at>=? AND substr(actor,1,?)=?`,
		kind, since, len(actorPrefix), actorPrefix).Scan(&n); err != nil {
		return 0, fmt.Errorf("jobstore: count audit targets: %w", err)
	}
	return n, nil
}
