package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const PlanHandoffMaxBytes = 16 << 10

var (
	ErrPlanHandoffConflict = errors.New("plan handoff version conflict")
	ErrPlanHandoffTooLarge = errors.New("plan handoff body exceeds 16 KiB")
)

// PlanHandoff is one immutable version of a plan's Markdown handoff note.
type PlanHandoff struct {
	PlanID  string `json:"plan_id"`
	Version int    `json:"version"`
	Body    string `json:"body"`
	By      string `json:"by"`
	At      int64  `json:"at"`
}

func scanPlanHandoff(sc rowScanner) (PlanHandoff, error) {
	var h PlanHandoff
	err := sc.Scan(&h.PlanID, &h.Version, &h.Body, &h.By, &h.At)
	return h, err
}

const selectPlanHandoffCols = `SELECT plan_id, version, body, by, at FROM plan_handoffs`

// GetPlanHandoff returns the latest version when version <= 0, or one requested
// historical version. Missing versions return ok=false without an error.
func (s *Store) GetPlanHandoff(planID string, version int) (PlanHandoff, bool, error) {
	q := selectPlanHandoffCols + " WHERE plan_id = ?"
	args := []any{planID}
	if version > 0 {
		q += " AND version = ?"
		args = append(args, version)
	} else {
		q += " ORDER BY version DESC LIMIT 1"
	}
	h, err := scanPlanHandoff(s.db.QueryRow(q, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return PlanHandoff{}, false, nil
	}
	if err != nil {
		return PlanHandoff{}, false, fmt.Errorf("jobstore: get plan handoff %q: %w", planID, err)
	}
	return h, true, nil
}

// ListPlanHandoffHistory returns all versions newest first.
func (s *Store) ListPlanHandoffHistory(planID string) ([]PlanHandoff, error) {
	rows, err := s.db.Query(selectPlanHandoffCols+" WHERE plan_id = ? ORDER BY version DESC", planID)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list plan handoff history %q: %w", planID, err)
	}
	defer rows.Close()
	out := make([]PlanHandoff, 0)
	for rows.Next() {
		h, scanErr := scanPlanHandoff(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("jobstore: scan plan handoff %q: %w", planID, scanErr)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list plan handoff history %q rows: %w", planID, err)
	}
	return out, nil
}

// SetPlanHandoff appends a new version if expectedVersion matches the current
// version. expectedVersion=0 is the first write. The version read and insert are
// one transaction under the store writer lock, so concurrent writers cannot skip
// or reuse a version.
func (s *Store) SetPlanHandoff(planID, body, by string, expectedVersion int) (PlanHandoff, error) {
	if planID == "" {
		return PlanHandoff{}, errors.New("jobstore: set plan handoff: empty plan id")
	}
	if len([]byte(body)) > PlanHandoffMaxBytes {
		return PlanHandoff{}, ErrPlanHandoffTooLarge
	}
	by = strings.TrimSpace(by)
	if by == "" {
		by = "unknown"
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return PlanHandoff{}, fmt.Errorf("jobstore: begin plan handoff: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var current int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(version),0) FROM plan_handoffs WHERE plan_id = ?`, planID).Scan(&current); err != nil {
		return PlanHandoff{}, fmt.Errorf("jobstore: read plan handoff version %q: %w", planID, err)
	}
	if expectedVersion != current {
		return PlanHandoff{}, fmt.Errorf("%w: expected %d, current %d", ErrPlanHandoffConflict, expectedVersion, current)
	}
	next := current + 1
	at := time.Now().Unix()
	if _, err := tx.Exec(`INSERT INTO plan_handoffs (plan_id, version, body, by, at) VALUES (?,?,?,?,?)`, planID, next, body, by, at); err != nil {
		return PlanHandoff{}, fmt.Errorf("jobstore: insert plan handoff %q: %w", planID, err)
	}
	if err := tx.Commit(); err != nil {
		return PlanHandoff{}, fmt.Errorf("jobstore: commit plan handoff %q: %w", planID, err)
	}
	return PlanHandoff{PlanID: planID, Version: next, Body: body, By: by, At: at}, nil
}
