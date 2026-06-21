package jobstore

import (
	"errors"
	"fmt"
)

// Delivery status values (design §5.6). A delivery starts pending, then either
// reaches delivered (a 2xx webhook response) or failed (the retry cap is hit).
// While pending it may be re-claimed by the sweeper on each due tick.
const (
	DeliveryPending   = "pending"
	DeliveryDelivered = "delivered"
	DeliveryFailed    = "failed"
)

// Delivery is the SQLite-persisted projection of one E14 webhook delivery (design
// §5.6): one event_seq aimed at one webhook target. Like JobEvent it is a neutral
// struct (no internal/job import) so the job package can enqueue/inspect it
// without forming an import cycle.
//
// EventSeq links back to job_events.seq; Target is the webhook URL. Status is one
// of the Delivery* constants. Attempts counts POST tries so far; NextRetryAt is
// the unix-second time the row becomes due again (the sweeper claims pending rows
// whose NextRetryAt <= now). LastError holds the most recent failure message (for
// the deliveries view); it never carries a secret.
type Delivery struct {
	ID          int64  `json:"id"`
	EventSeq    int64  `json:"event_seq"`
	JobID       string `json:"job_id"`
	Target      string `json:"target"`
	Status      string `json:"status"`
	Attempts    int    `json:"attempts"`
	NextRetryAt int64  `json:"next_retry_at"`
	LastError   string `json:"last_error,omitempty"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

// selectDeliveryCols is the shared projection. COALESCE guards the nullable
// last_error so a NULL scans into "" instead of failing the scan.
const selectDeliveryCols = `SELECT id, event_seq, job_id, target, status, attempts,
  next_retry_at, COALESCE(last_error,''), created_at, updated_at
  FROM event_deliveries`

// scanDelivery reads one row (in selectDeliveryCols order) into a Delivery.
func scanDelivery(sc rowScanner) (Delivery, error) {
	var d Delivery
	err := sc.Scan(
		&d.ID, &d.EventSeq, &d.JobID, &d.Target, &d.Status, &d.Attempts,
		&d.NextRetryAt, &d.LastError, &d.CreatedAt, &d.UpdatedAt,
	)
	return d, err
}

// InsertDelivery enqueues one pending webhook delivery (E14). It stamps
// created_at/updated_at to d.CreatedAt (the caller passes the current time so the
// store stays clock-free / testable) and returns the assigned row id. Status and
// NextRetryAt are taken from d as given (the enqueue path passes pending +
// next_retry_at=now so the row is immediately due). Writes go through s.writeMu
// (like every other writer) so SQLite never sees two concurrent writers.
func (s *Store) InsertDelivery(d Delivery) (int64, error) {
	if d.JobID == "" {
		return 0, errors.New("jobstore: InsertDelivery: empty job id")
	}
	if d.Target == "" {
		return 0, errors.New("jobstore: InsertDelivery: empty target")
	}
	if d.Status == "" {
		d.Status = DeliveryPending
	}
	const q = `INSERT INTO event_deliveries
  (event_seq, job_id, target, status, attempts, next_retry_at, last_error, created_at, updated_at)
  VALUES (?,?,?,?,?,?,?,?,?)`
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var lastErr any
	if d.LastError != "" {
		lastErr = d.LastError
	}
	res, err := s.db.Exec(q,
		d.EventSeq, d.JobID, d.Target, d.Status, d.Attempts,
		d.NextRetryAt, lastErr, d.CreatedAt, d.CreatedAt,
	)
	if err != nil {
		return 0, fmt.Errorf("jobstore: insert delivery %q->%q: %w", d.JobID, d.Target, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("jobstore: insert delivery last id: %w", err)
	}
	return id, nil
}

// ClaimLeaseSeconds is the default lease a claimed-but-not-yet-resolved delivery
// is pushed out of the due window for (see ClaimDueDeliveries). It must exceed
// the per-POST webhook timeout so a delivery still being attempted is never
// re-claimed; on a process crash mid-attempt the lease expires and the row
// becomes due again (at-least-once delivery).
const ClaimLeaseSeconds = 60

// ClaimDueDeliveries atomically claims up to limit pending deliveries that are
// due (status='pending' AND next_retry_at <= now) and returns them for the
// sweeper to POST.
//
// SR303 single-claim guarantee: the claim is a conditional UPDATE that moves the
// row's next_retry_at to a future lease time (now+lease) — only the UPDATE that
// actually affects the row (`... WHERE id=? AND status='pending' AND
// next_retry_at <= now`) wins it, so two concurrent claims can never hand the
// SAME delivery out twice. The row STAYS pending (the sweep marks it
// delivered/retry/failed after the POST); leasing it out of the due window is the
// dedup barrier. If the process crashes between claim and resolution the lease
// expires and the delivery becomes due again on a later tick (at-least-once).
//
// All work runs under writeMu (the in-process write lock every writer uses) so
// the SELECT and the per-row UPDATEs never interleave with another writer. now/
// limit/lease are injected so tests can pin the clock, batch size and lease. A
// non-positive limit yields no rows; a non-positive lease falls back to
// ClaimLeaseSeconds.
func (s *Store) ClaimDueDeliveries(now int64, limit int, lease int64) ([]Delivery, error) {
	if limit <= 0 {
		return nil, nil
	}
	if lease <= 0 {
		lease = ClaimLeaseSeconds
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	rows, err := s.db.Query(
		selectDeliveryCols+` WHERE status = ? AND next_retry_at <= ? ORDER BY next_retry_at ASC, id ASC LIMIT ?`,
		DeliveryPending, now, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("jobstore: claim due select: %w", err)
	}
	var candidates []Delivery
	for rows.Next() {
		d, scanErr := scanDelivery(rows)
		if scanErr != nil {
			rows.Close()
			return nil, fmt.Errorf("jobstore: claim due scan: %w", scanErr)
		}
		candidates = append(candidates, d)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("jobstore: claim due rows: %w", err)
	}
	rows.Close()

	// Conditional lease per candidate: only a row still pending AND still due is
	// claimed (next_retry_at moves to now+lease so it leaves the due window). The
	// WHERE re-checks both predicates so a competing claim that already leased the
	// row affects 0 rows and we skip it — the single-claim barrier.
	leaseUntil := now + lease
	out := make([]Delivery, 0, len(candidates))
	for _, d := range candidates {
		res, uerr := s.db.Exec(
			`UPDATE event_deliveries SET next_retry_at = ?, updated_at = ?
       WHERE id = ? AND status = ? AND next_retry_at <= ?`,
			leaseUntil, now, d.ID, DeliveryPending, now,
		)
		if uerr != nil {
			return nil, fmt.Errorf("jobstore: claim delivery %d: %w", d.ID, uerr)
		}
		n, _ := res.RowsAffected()
		if n == 1 {
			d.NextRetryAt = leaseUntil
			d.UpdatedAt = now
			out = append(out, d)
		}
	}
	return out, nil
}

// MarkDelivered moves a delivery to the terminal delivered state after a 2xx
// webhook response. now stamps updated_at and last_error is cleared.
func (s *Store) MarkDelivered(id int64, now int64) error {
	return s.updateDeliveryStatus(id, DeliveryDelivered, -1, 0, "", now)
}

// MarkRetry records a failed attempt that will be retried: it sets status back to
// pending, bumps attempts, sets next_retry_at to the backoff target and records
// lastErr. now stamps updated_at.
func (s *Store) MarkRetry(id int64, attempts int, nextRetryAt int64, lastErr string, now int64) error {
	return s.updateDeliveryStatus(id, DeliveryPending, attempts, nextRetryAt, lastErr, now)
}

// MarkFailed moves a delivery to the terminal failed state (retry cap reached).
// attempts is recorded as given and lastErr captures the final failure.
func (s *Store) MarkFailed(id int64, attempts int, lastErr string, now int64) error {
	return s.updateDeliveryStatus(id, DeliveryFailed, attempts, 0, lastErr, now)
}

// updateDeliveryStatus is the shared writer for the three Mark* transitions. A
// negative attempts leaves the column unchanged (MarkDelivered keeps the existing
// count); a zero/empty lastErr clears the column. Runs under writeMu.
func (s *Store) updateDeliveryStatus(id int64, status string, attempts int, nextRetryAt int64, lastErr string, now int64) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var le any
	if lastErr != "" {
		le = lastErr
	}
	var (
		q    string
		args []any
	)
	if attempts < 0 {
		q = `UPDATE event_deliveries SET status=?, next_retry_at=?, last_error=?, updated_at=? WHERE id=?`
		args = []any{status, nextRetryAt, le, now, id}
	} else {
		q = `UPDATE event_deliveries SET status=?, attempts=?, next_retry_at=?, last_error=?, updated_at=? WHERE id=?`
		args = []any{status, attempts, nextRetryAt, le, now, id}
	}
	if _, err := s.db.Exec(q, args...); err != nil {
		return fmt.Errorf("jobstore: update delivery %d -> %s: %w", id, status, err)
	}
	return nil
}

// ListDeliveriesByJob returns a job's webhook deliveries in id order (creation
// order) for the read-only deliveries view (P2-d). A job with no deliveries
// yields an empty slice and no error.
func (s *Store) ListDeliveriesByJob(jobID string) ([]Delivery, error) {
	rows, err := s.db.Query(selectDeliveryCols+" WHERE job_id = ? ORDER BY id ASC", jobID)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list deliveries %q: %w", jobID, err)
	}
	defer rows.Close()

	out := make([]Delivery, 0)
	for rows.Next() {
		d, scanErr := scanDelivery(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("jobstore: scan delivery row: %w", scanErr)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list deliveries %q rows: %w", jobID, err)
	}
	return out, nil
}
