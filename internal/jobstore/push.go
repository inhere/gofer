package jobstore

import (
	"errors"
	"fmt"
)

// PushSubscription is one browser endpoint owned by an authenticated user
// caller. The encryption key material is opaque to jobstore; internal/webpush is
// the only package that interprets it.
type PushSubscription struct {
	CallerID  string `json:"caller_id,omitempty"`
	Endpoint  string `json:"endpoint"`
	P256DH    string `json:"p256dh,omitempty"`
	Auth      string `json:"auth,omitempty"`
	UserAgent string `json:"user_agent"`
	CreatedAt int64  `json:"created_at"`
	LastOKAt  int64  `json:"last_ok_at"`
}

// UpsertPushSubscription registers endpoint for caller. Endpoint is globally
// unique: a browser endpoint re-registered by another authenticated caller moves
// to that caller and resets last_ok_at because the key material changed.
func (s *Store) UpsertPushSubscription(sub PushSubscription) error {
	if sub.CallerID == "" {
		return errors.New("jobstore: push subscription: empty caller id")
	}
	if sub.Endpoint == "" {
		return errors.New("jobstore: push subscription: empty endpoint")
	}
	if sub.P256DH == "" || sub.Auth == "" {
		return errors.New("jobstore: push subscription: missing browser keys")
	}
	if sub.CreatedAt <= 0 {
		return errors.New("jobstore: push subscription: invalid created_at")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`INSERT INTO push_subscriptions
  (caller_id, endpoint, p256dh, auth, user_agent, created_at, last_ok_at)
  VALUES (?,?,?,?,?,?,0)
  ON CONFLICT(endpoint) DO UPDATE SET
    caller_id=excluded.caller_id,
    p256dh=excluded.p256dh,
    auth=excluded.auth,
    user_agent=excluded.user_agent,
    created_at=excluded.created_at,
    last_ok_at=0`,
		sub.CallerID, sub.Endpoint, sub.P256DH, sub.Auth, sub.UserAgent, sub.CreatedAt)
	if err != nil {
		return fmt.Errorf("jobstore: upsert push subscription: %w", err)
	}
	return nil
}

// ListPushSubscriptions returns only caller's subscriptions. Encryption keys are
// included for the internal sender; HTTP projections deliberately omit them.
func (s *Store) ListPushSubscriptions(callerID string) ([]PushSubscription, error) {
	if callerID == "" {
		return nil, errors.New("jobstore: list push subscriptions: empty caller id")
	}
	rows, err := s.db.Query(`SELECT caller_id, endpoint, p256dh, auth,
  user_agent, created_at, last_ok_at
  FROM push_subscriptions WHERE caller_id=?
  ORDER BY created_at, endpoint`, callerID)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list push subscriptions: %w", err)
	}
	defer rows.Close()
	out := make([]PushSubscription, 0)
	for rows.Next() {
		var sub PushSubscription
		if err := rows.Scan(&sub.CallerID, &sub.Endpoint, &sub.P256DH, &sub.Auth,
			&sub.UserAgent, &sub.CreatedAt, &sub.LastOKAt); err != nil {
			return nil, fmt.Errorf("jobstore: scan push subscription: %w", err)
		}
		out = append(out, sub)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list push subscription rows: %w", err)
	}
	return out, nil
}

// DeletePushSubscription removes endpoint only when it belongs to caller. The
// boolean lets HTTP return 404 without exposing another caller's ownership.
func (s *Store) DeletePushSubscription(callerID, endpoint string) (bool, error) {
	if callerID == "" || endpoint == "" {
		return false, errors.New("jobstore: delete push subscription: caller and endpoint are required")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	result, err := s.db.Exec(`DELETE FROM push_subscriptions
  WHERE caller_id=? AND endpoint=?`, callerID, endpoint)
	if err != nil {
		return false, fmt.Errorf("jobstore: delete push subscription: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("jobstore: delete push subscription rows: %w", err)
	}
	return n == 1, nil
}

// DeletePushSubscriptionEndpoint removes an endpoint after its push service
// returns 404/410. It is intentionally not caller-scoped: the endpoint itself is
// the globally unique sender identity.
func (s *Store) DeletePushSubscriptionEndpoint(endpoint string) error {
	if endpoint == "" {
		return errors.New("jobstore: delete push endpoint: empty endpoint")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(`DELETE FROM push_subscriptions WHERE endpoint=?`, endpoint); err != nil {
		return fmt.Errorf("jobstore: delete push endpoint: %w", err)
	}
	return nil
}

// MarkPushSubscriptionOK records the most recent successful 2xx delivery.
func (s *Store) MarkPushSubscriptionOK(endpoint string, at int64) error {
	if endpoint == "" || at <= 0 {
		return errors.New("jobstore: mark push subscription ok: invalid endpoint or timestamp")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(`UPDATE push_subscriptions SET last_ok_at=?
  WHERE endpoint=?`, at, endpoint); err != nil {
		return fmt.Errorf("jobstore: mark push subscription ok: %w", err)
	}
	return nil
}
