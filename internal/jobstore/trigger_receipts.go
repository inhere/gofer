package jobstore

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var ErrTriggerReceiptConflict = errors.New("trigger receipt external id reused with different body")

type TriggerReceipt struct {
	TriggerName     string `json:"trigger_name"`
	ExternalEventID string `json:"external_event_id"`
	RequestID       string `json:"request_id"`
	BodyHash        string `json:"body_hash"`
	Source          string `json:"source,omitempty"`
	ReceivedAt      int64  `json:"received_at"`
	Status          string `json:"status"`
	JobID           string `json:"job_id,omitempty"`
	LastError       string `json:"last_error,omitempty"`
	ExpiresAt       int64  `json:"expires_at"`
}

func HashTriggerBody(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// ClaimTriggerReceipt atomically claims an event. Existing claims are returned
// without changing their status, so a concurrent retry can never submit twice.
func (s *Store) ClaimTriggerReceipt(r TriggerReceipt) (existing *TriggerReceipt, duplicate bool, err error) {
	if r.TriggerName == "" || r.ExternalEventID == "" {
		return nil, false, errors.New("trigger receipt requires trigger and external event id")
	}
	if r.ReceivedAt == 0 {
		r.ReceivedAt = time.Now().Unix()
	}
	if r.ExpiresAt == 0 {
		r.ExpiresAt = r.ReceivedAt + 86400
	}
	if r.Status == "" {
		r.Status = "claimed"
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`INSERT OR IGNORE INTO trigger_receipts(trigger_name,external_event_id,request_id,body_hash,source,received_at,status,job_id,last_error,expires_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, r.TriggerName, r.ExternalEventID, r.RequestID, r.BodyHash, r.Source, r.ReceivedAt, r.Status, r.JobID, r.LastError, r.ExpiresAt)
	if err != nil {
		return nil, false, fmt.Errorf("jobstore: claim trigger receipt: %w", err)
	}
	inserted, _ := res.RowsAffected()
	var got TriggerReceipt
	err = s.db.QueryRow(`SELECT trigger_name,external_event_id,request_id,body_hash,source,received_at,status,job_id,last_error,expires_at FROM trigger_receipts WHERE trigger_name=? AND external_event_id=?`, r.TriggerName, r.ExternalEventID).Scan(&got.TriggerName, &got.ExternalEventID, &got.RequestID, &got.BodyHash, &got.Source, &got.ReceivedAt, &got.Status, &got.JobID, &got.LastError, &got.ExpiresAt)
	if err != nil {
		return nil, false, fmt.Errorf("jobstore: read trigger receipt: %w", err)
	}
	if got.BodyHash != r.BodyHash {
		return &got, false, ErrTriggerReceiptConflict
	}
	if inserted == 0 {
		return &got, true, nil
	}
	return nil, false, nil
}

func (s *Store) UpdateTriggerReceipt(triggerName, eventID, status, jobID, lastError string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE trigger_receipts SET status=?,job_id=?,last_error=? WHERE trigger_name=? AND external_event_id=?`, status, jobID, lastError, triggerName, eventID)
	if err != nil {
		return fmt.Errorf("jobstore: update trigger receipt: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// Receipt statuses. claimed is the only non-terminal state: the event is accepted but
// the job submit has not finished (or the process died in between).
const (
	TriggerReceiptClaimed   = "claimed"
	TriggerReceiptSubmitted = "submitted"
	// TriggerReceiptRejected is a PERMANENT refusal (ceiling, invalid request, revoked
	// owner): a redelivery of the same event gets the same answer, so it is not retried.
	TriggerReceiptRejected = "rejected"
	// TriggerReceiptFailed is a transient failure or an interrupted claim: a redelivery of
	// the same event may take the receipt over (ReclaimTriggerReceipt) and submit again.
	TriggerReceiptFailed = "failed"
)

// ReclaimTriggerReceipt lets a redelivered event take over a receipt that never reached
// submitted: status failed, or claimed but older than staleBefore (the claiming process
// died or is stuck). It is one conditional UPDATE, so of several concurrent redeliveries
// exactly one wins (ok=true); the loser sees the receipt claimed again and fresh. The body
// hash must match — a reused id with another body is never taken over. The receipt keeps
// its request_id, which is the job idempotency key, so a job the interrupted attempt did
// create is returned by the re-submit instead of being created twice.
func (s *Store) ReclaimTriggerReceipt(triggerName, eventID, bodyHash string, now, staleBefore int64) (ok bool, err error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE trigger_receipts SET status='claimed', received_at=?, last_error='' WHERE trigger_name=? AND external_event_id=? AND body_hash=? AND (status='failed' OR (status='claimed' AND received_at<?))`,
		now, triggerName, eventID, bodyHash, staleBefore)
	if err != nil {
		return false, fmt.Errorf("jobstore: reclaim trigger receipt: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ListStaleTriggerReceipts returns claimed receipts older than staleBefore, oldest first.
func (s *Store) ListStaleTriggerReceipts(staleBefore int64, limit int) ([]TriggerReceipt, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT trigger_name,external_event_id,request_id,body_hash,source,received_at,status,job_id,last_error,expires_at FROM trigger_receipts WHERE status='claimed' AND received_at<? ORDER BY received_at LIMIT ?`, staleBefore, limit)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list stale trigger receipts: %w", err)
	}
	defer rows.Close()
	var out []TriggerReceipt
	for rows.Next() {
		var r TriggerReceipt
		if err := rows.Scan(&r.TriggerName, &r.ExternalEventID, &r.RequestID, &r.BodyHash, &r.Source, &r.ReceivedAt, &r.Status, &r.JobID, &r.LastError, &r.ExpiresAt); err != nil {
			return nil, fmt.Errorf("jobstore: scan trigger receipt: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetTriggerReceipt returns one receipt.
func (s *Store) GetTriggerReceipt(triggerName, eventID string) (TriggerReceipt, bool, error) {
	var r TriggerReceipt
	err := s.db.QueryRow(`SELECT trigger_name,external_event_id,request_id,body_hash,source,received_at,status,job_id,last_error,expires_at FROM trigger_receipts WHERE trigger_name=? AND external_event_id=?`, triggerName, eventID).
		Scan(&r.TriggerName, &r.ExternalEventID, &r.RequestID, &r.BodyHash, &r.Source, &r.ReceivedAt, &r.Status, &r.JobID, &r.LastError, &r.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return TriggerReceipt{}, false, nil
	}
	if err != nil {
		return TriggerReceipt{}, false, fmt.Errorf("jobstore: get trigger receipt: %w", err)
	}
	return r, true, nil
}

// PruneTriggerReceipts deletes receipts whose dedup window has passed. A receipt is
// never deleted before expires_at, so an event inside its window cannot be reinterpreted
// as a new one; a claimed receipt that expired is deleted too (the reconciler normally
// settles it long before).
func (s *Store) PruneTriggerReceipts(now int64) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`DELETE FROM trigger_receipts WHERE expires_at<=?`, now)
	if err != nil {
		return 0, fmt.Errorf("jobstore: prune trigger receipts: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// TriggerAuditEvent is one trigger audit row. The external event id is stored only as a
// hash (EventHash), never as the provider's raw value, and the remote address only as a
// digest; the raw body is never stored.
type TriggerAuditEvent struct {
	Kind      string
	Trigger   string
	Owner     string
	Project   string
	Provider  string
	RequestID string
	EventHash string
	JobID     string
	Code      string
	Remote    string
}

// HashTriggerEventID is the stored form of a provider's external event id: a SHA-256 hex
// digest scoped by the trigger name, so the raw id (which can embed tenant or repository
// identifiers) never lands in the database and one id in two triggers does not collide.
func HashTriggerEventID(triggerName, eventID string) string {
	sum := sha256.Sum256([]byte(triggerName + "\x00" + eventID))
	return hex.EncodeToString(sum[:])
}

// AppendTriggerAudit writes one trigger audit event.
func (s *Store) AppendTriggerAudit(ev TriggerAuditEvent) error {
	detail, err := json.Marshal(map[string]string{
		"trigger": ev.Trigger, "project": ev.Project, "provider": ev.Provider,
		"request_id": ev.RequestID, "external_event_hash": truncateHash(ev.EventHash),
		"job_id": ev.JobID, "code": ev.Code, "remote": ev.Remote,
	})
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err = s.db.Exec(`INSERT INTO audit_events(kind,target_id,actor,at,detail_json) VALUES(?,?,?,?,?)`, ev.Kind, ev.Trigger, ev.Owner, time.Now().Unix(), string(detail))
	if err != nil {
		return fmt.Errorf("jobstore: append trigger audit: %w", err)
	}
	return nil
}

// truncateHash keeps audit rows short: 16 hex chars (64 bits) still tell events apart.
func truncateHash(h string) string {
	if len(h) > 16 {
		return h[:16]
	}
	return h
}

// SettleTriggerReceipt moves a receipt from one status to another only if it is still in
// from (a compare-and-set), so the reconciler can never overwrite a receipt a live request
// just finished.
func (s *Store) SettleTriggerReceipt(triggerName, eventID, from, status, jobID, lastError string) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE trigger_receipts SET status=?,job_id=?,last_error=? WHERE trigger_name=? AND external_event_id=? AND status=?`, status, jobID, lastError, triggerName, eventID, from)
	if err != nil {
		return false, fmt.Errorf("jobstore: settle trigger receipt: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}
