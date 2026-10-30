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

func (s *Store) AppendTriggerAudit(kind, triggerName, owner, requestID, eventID, jobID, code string) error {
	detail, err := json.Marshal(map[string]string{"trigger": triggerName, "request_id": requestID, "external_event_id": eventID, "job_id": jobID, "code": code})
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err = s.db.Exec(`INSERT INTO audit_events(kind,target_id,actor,at,detail_json) VALUES(?,?,?,?,?)`, kind, triggerName, owner, time.Now().Unix(), string(detail))
	if err != nil {
		return fmt.Errorf("jobstore: append trigger audit: %w", err)
	}
	return nil
}
