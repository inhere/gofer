package jobstore

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Work request kinds (W2a, design §14.3): what was asked for.
const (
	WorkRequestReport    = "report"    // ask a running session to report its progress
	WorkRequestHandoff   = "handoff"   // ask a running session to write a hand-over
	WorkRequestSummarize = "summarize" // tidy the work item up from the transcript
)

// Work request states.
const (
	WorkRequestPending  = "pending"
	WorkRequestSent     = "sent"
	WorkRequestAnswered = "answered"
	WorkRequestFailed   = "failed"
	WorkRequestExpired  = "expired"
)

// ErrWorkRequestNotFound is returned for an unknown request id.
var ErrWorkRequestNotFound = errors.New("work request not found")

// WorkRequest is one row of the request ledger: an ask that was (or could not be)
// delivered, and what became of it. Timestamps are unix seconds.
type WorkRequest struct {
	ID         string `json:"id"`
	WorkItemID string `json:"work_item_id"`
	SessionID  string `json:"session_id,omitempty"`
	Kind       string `json:"kind"`
	State      string `json:"state"`
	Channel    string `json:"channel,omitempty"`
	Text       string `json:"text,omitempty"`
	By         string `json:"by"`
	Error      string `json:"error,omitempty"`
	CreatedAt  int64  `json:"created_at"`
	SentAt     int64  `json:"sent_at,omitempty"`
	AnsweredAt int64  `json:"answered_at,omitempty"`
	Deadline   int64  `json:"deadline,omitempty"`
	ParentID   string `json:"parent_id,omitempty"`
}

// WorkRequestActive reports whether the request is still in flight.
func WorkRequestActive(state string) bool {
	return state == WorkRequestPending || state == WorkRequestSent
}

const selectWorkRequestCols = `SELECT id, work_item_id, session_id, kind, state, channel, text, by, error,
  created_at, sent_at, answered_at, deadline, parent_id FROM work_requests`

func scanWorkRequest(sc rowScanner) (WorkRequest, error) {
	var r WorkRequest
	err := sc.Scan(&r.ID, &r.WorkItemID, &r.SessionID, &r.Kind, &r.State, &r.Channel, &r.Text, &r.By, &r.Error,
		&r.CreatedAt, &r.SentAt, &r.AnsweredAt, &r.Deadline, &r.ParentID)
	return r, err
}

func newWorkRequestID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("wr-%08x", time.Now().UnixNano()&0xffffffff)
	}
	return "wr-" + hex.EncodeToString(b[:])
}

func validWorkRequestKind(k string) bool {
	return k == WorkRequestReport || k == WorkRequestHandoff || k == WorkRequestSummarize
}

// WorkRequestInput creates a ledger row (state pending).
type WorkRequestInput struct {
	WorkItemID string
	SessionID  string
	Kind       string
	By         string
	Text       string
	// Deadline is the unix time after which an unanswered request expires (0 = never).
	Deadline int64
	ParentID string
}

// CreateWorkRequest records a new pending request.
func (s *Store) CreateWorkRequest(in WorkRequestInput) (WorkRequest, error) {
	if !validWorkRequestKind(in.Kind) {
		return WorkRequest{}, fmt.Errorf("%w: invalid request kind %q", ErrWorkInvalid, in.Kind)
	}
	id := strings.TrimSpace(in.WorkItemID)
	defer s.emit(Change{Kind: ChangeWork, ID: id})
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, ok, err := s.getWorkItemOn(s.db, id); err != nil {
		return WorkRequest{}, err
	} else if !ok {
		return WorkRequest{}, ErrWorkItemNotFound
	}
	by := strings.TrimSpace(in.By)
	if by == "" {
		by = "system"
	}
	r := WorkRequest{
		ID: newWorkRequestID(), WorkItemID: id, SessionID: strings.TrimSpace(in.SessionID), Kind: in.Kind,
		State: WorkRequestPending, Text: capText(in.Text, maxWorkField), By: by, CreatedAt: s.unixNow(),
		Deadline: in.Deadline, ParentID: in.ParentID,
	}
	if _, err := s.db.Exec(`INSERT INTO work_requests(id, work_item_id, session_id, kind, state, channel, text, by, error,
  created_at, sent_at, answered_at, deadline, parent_id) VALUES (?,?,?,?,?,'',?,?,'',?,0,0,?,?)`,
		r.ID, r.WorkItemID, r.SessionID, r.Kind, r.State, r.Text, r.By, r.CreatedAt, r.Deadline, r.ParentID); err != nil {
		return WorkRequest{}, fmt.Errorf("jobstore: create work request: %w", err)
	}
	return r, nil
}

// GetWorkRequest reads one request.
func (s *Store) GetWorkRequest(id string) (WorkRequest, bool, error) {
	r, err := scanWorkRequest(s.db.QueryRow(selectWorkRequestCols+" WHERE id = ?", strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return WorkRequest{}, false, nil
	}
	if err != nil {
		return WorkRequest{}, false, fmt.Errorf("jobstore: get work request: %w", err)
	}
	return r, true, nil
}

// ListWorkRequests lists an item's requests, newest first ("" = every item). Limit
// defaults to 50.
func (s *Store) ListWorkRequests(workItemID string, activeOnly bool, limit int) ([]WorkRequest, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	q := selectWorkRequestCols
	var where []string
	var args []any
	if id := strings.TrimSpace(workItemID); id != "" {
		where = append(where, "work_item_id = ?")
		args = append(args, id)
	}
	if activeOnly {
		where = append(where, "state IN ('pending','sent')")
	}
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY created_at DESC, id DESC LIMIT ?"
	args = append(args, limit)
	return s.queryWorkRequests(q, args...)
}

func (s *Store) queryWorkRequests(q string, args ...any) ([]WorkRequest, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list work requests: %w", err)
	}
	defer rows.Close()
	out := make([]WorkRequest, 0)
	for rows.Next() {
		r, err := scanWorkRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecentWorkRequests returns, for the card, an item's in-flight requests plus its most
// recent finished one (so "会话已回复 / 未回应，已改为整理" stays visible for a while).
func (s *Store) RecentWorkRequests(workItemID string, finishedSince int64) ([]WorkRequest, error) {
	return s.queryWorkRequests(selectWorkRequestCols+` WHERE work_item_id = ? AND
  (state IN ('pending','sent') OR created_at >= ?) ORDER BY created_at DESC, id DESC LIMIT 20`,
		strings.TrimSpace(workItemID), finishedSince)
}

// ListOverdueWorkRequests returns the in-flight requests whose deadline has passed.
func (s *Store) ListOverdueWorkRequests(now int64) ([]WorkRequest, error) {
	return s.queryWorkRequests(selectWorkRequestCols+` WHERE state IN ('pending','sent') AND deadline > 0 AND deadline <= ?
  ORDER BY deadline, id LIMIT 200`, now)
}

// MarkWorkRequest moves a request to a new state if its current state is one of from
// (empty = any) and reports whether it changed. sent / answered stamp their times; an
// error text is kept for failed.
func (s *Store) MarkWorkRequest(id, state, channel, errText string, from ...string) (WorkRequest, bool, error) {
	id = strings.TrimSpace(id)
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	cur, ok, err := s.GetWorkRequest(id)
	if err != nil {
		return WorkRequest{}, false, err
	}
	if !ok {
		return WorkRequest{}, false, ErrWorkRequestNotFound
	}
	defer s.emit(Change{Kind: ChangeWork, ID: cur.WorkItemID})
	if len(from) > 0 {
		match := false
		for _, f := range from {
			if f == cur.State {
				match = true
			}
		}
		if !match {
			return cur, false, nil
		}
	}
	now := s.unixNow()
	next := cur
	next.State = state
	if channel != "" {
		next.Channel = channel
	}
	if errText != "" {
		next.Error = capText(errText, 1000)
	}
	switch state {
	case WorkRequestSent:
		next.SentAt = now
	case WorkRequestAnswered:
		next.AnsweredAt = now
		next.Error = ""
	}
	if _, err := s.db.Exec(`UPDATE work_requests SET state=?, channel=?, error=?, sent_at=?, answered_at=? WHERE id=?`,
		next.State, next.Channel, next.Error, next.SentAt, next.AnsweredAt, id); err != nil {
		return cur, false, fmt.Errorf("jobstore: update work request: %w", err)
	}
	return next, true, nil
}
