package jobstore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Wakeup KINDS (JOB-09, design §五.1). A wakeup is either a TIMER (at / every /
// cron) or an EVENT subscription; both start a continuation of the job it is
// registered on when they fire.
const (
	// WakeupKindAt fires once at an absolute instant.
	WakeupKindAt = "at"
	// WakeupKindEvery fires every EverySec seconds from the moment it was created
	// or last enabled, never replaying a tick it could not fire (design §五.1).
	WakeupKindEvery = "every"
	// WakeupKindCron fires on the next CronExpr instant in Timezone.
	WakeupKindCron = "cron"
	// WakeupKindEvent fires when another job's lifecycle event matches.
	WakeupKindEvent = "event"
)

// Wakeup MODES: once consumes the wakeup on its first fire (enabled -> 0);
// continuous keeps it armed. The default is once for event/at and continuous for
// every/cron (design §五.1).
const (
	WakeupModeOnce       = "once"
	WakeupModeContinuous = "continuous"
)

// WakeupClaimPending is the continuation_job_id SENTINEL a fire writes before it
// submits the continuation job (see Store.ClaimWakeupFire). The slot must read as
// taken while that submit is in flight: submitting records job.submitted, which
// runs through the SAME event path the wakeup matcher listens on, so a slot that
// still looked free would let one wakeup fire (and re-fire) itself.
const WakeupClaimPending = "pending"

// WakeupRecord is the SQLite-persisted projection of one JOB-09 wakeup: an event
// subscription or timer registered on a job that starts a CONTINUATION of that job
// when it fires. The JSON columns are opaque strings to this package — jobstore
// must not import internal/job (G022) — and hold an array of event-type strings
// (EventTypesJSON) and of status strings (FilterStatusJSON).
//
// Revision is bumped by every UpdateWakeup so a reader can tell an edited wakeup
// from the one it read; the fire counters (LastFiredAt / FiredCount /
// CoalescedCount / ContinuationJobID) are written by the fire path alone.
type WakeupRecord struct {
	ID               string
	JobID            string
	Kind             string
	At               int64
	EverySec         int64
	CronExpr         string
	Timezone         string
	EventTypesJSON   string
	FilterJobID      string
	FilterStatusJSON string
	Mode             string
	Instruction      string
	Enabled          int
	Revision         int64
	NextRunAt        int64
	LastFiredAt      int64
	FiredCount       int64
	CoalescedCount   int64
	// ContinuationJobID is the single outstanding continuation this wakeup started
	// (or WakeupClaimPending while that submit is in flight). A trigger that finds
	// it set and non-terminal only bumps CoalescedCount — one wakeup never stacks
	// continuations (design §五 决策 6).
	ContinuationJobID string
	CreatedBy         string
	CreatedAt         int64
	ExpiresAt         int64
}

const selectWakeupCols = `SELECT id, job_id, kind, COALESCE(at,0), COALESCE(every_sec,0),
  COALESCE(cron_expr,''), COALESCE(timezone,''), COALESCE(event_types_json,''),
  COALESCE(filter_job_id,''), COALESCE(filter_status_json,''), COALESCE(mode,'once'),
  COALESCE(instruction,''), COALESCE(enabled,0), COALESCE(revision,1), COALESCE(next_run_at,0),
  COALESCE(last_fired_at,0), COALESCE(fired_count,0), COALESCE(coalesced_count,0),
  COALESCE(continuation_job_id,''), COALESCE(created_by,''), created_at, COALESCE(expires_at,0)
  FROM job_wakeups`

func scanWakeup(sc rowScanner) (WakeupRecord, error) {
	var r WakeupRecord
	err := sc.Scan(
		&r.ID, &r.JobID, &r.Kind, &r.At, &r.EverySec,
		&r.CronExpr, &r.Timezone, &r.EventTypesJSON,
		&r.FilterJobID, &r.FilterStatusJSON, &r.Mode,
		&r.Instruction, &r.Enabled, &r.Revision, &r.NextRunAt,
		&r.LastFiredAt, &r.FiredCount, &r.CoalescedCount,
		&r.ContinuationJobID, &r.CreatedBy, &r.CreatedAt, &r.ExpiresAt,
	)
	return r, err
}

// IsContinuationPending reports whether the fire slot is held by an in-flight
// submit rather than by a job id.
func (r WakeupRecord) IsContinuationPending() bool {
	return r.ContinuationJobID == WakeupClaimPending
}

// InsertWakeup records a new wakeup. It refuses a half-built row (no id / target
// job / kind) and defaults the mode to once so a row never reads back as "unset".
func (s *Store) InsertWakeup(r WakeupRecord) error {
	if r.ID == "" {
		return errors.New("jobstore: InsertWakeup: empty wakeup id")
	}
	if r.JobID == "" || r.Kind == "" {
		return fmt.Errorf("jobstore: InsertWakeup %q: missing job_id/kind", r.ID)
	}
	if r.Mode == "" {
		r.Mode = WakeupModeOnce
	}
	if r.Revision <= 0 {
		r.Revision = 1
	}
	const q = `INSERT INTO job_wakeups
  (id, job_id, kind, at, every_sec, cron_expr, timezone, event_types_json, filter_job_id,
   filter_status_json, mode, instruction, enabled, revision, next_run_at, last_fired_at,
   fired_count, coalesced_count, continuation_job_id, created_by, created_at, expires_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(q,
		r.ID, r.JobID, r.Kind, r.At, r.EverySec, r.CronExpr, r.Timezone, r.EventTypesJSON,
		r.FilterJobID, r.FilterStatusJSON, r.Mode, r.Instruction, r.Enabled, r.Revision,
		r.NextRunAt, r.LastFiredAt, r.FiredCount, r.CoalescedCount, r.ContinuationJobID,
		r.CreatedBy, r.CreatedAt, r.ExpiresAt,
	); err != nil {
		return fmt.Errorf("jobstore: insert wakeup %q: %w", r.ID, err)
	}
	return nil
}

// GetWakeup returns one wakeup row; ok=false when the id is unknown.
func (s *Store) GetWakeup(id string) (WakeupRecord, bool, error) {
	r, err := scanWakeup(s.db.QueryRow(selectWakeupCols+" WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return WakeupRecord{}, false, nil
	}
	if err != nil {
		return WakeupRecord{}, false, fmt.Errorf("jobstore: get wakeup %q: %w", id, err)
	}
	return r, true, nil
}

// ListWakeups returns a job's wakeups oldest-first (the order they were registered
// in, which is how the job detail reads them).
func (s *Store) ListWakeups(jobID string) ([]WakeupRecord, error) {
	return s.listWakeups(
		selectWakeupCols+" WHERE job_id = ? ORDER BY created_at ASC, id ASC",
		jobID,
	)
}

// UpdateWakeup persists the AUTHORED fields of a wakeup (everything the operator
// may change: the timer spec, the event filter, the instruction, the mode, the
// expiry, the enabled flag and the re-armed next_run_at) and bumps Revision. The
// fire counters are NOT written here — they belong to ClaimWakeupFire /
// SetWakeupContinuation — so a re-arm cannot erase what a concurrent fire recorded.
func (s *Store) UpdateWakeup(r WakeupRecord) error {
	if r.ID == "" {
		return errors.New("jobstore: UpdateWakeup: empty wakeup id")
	}
	if r.Mode == "" {
		r.Mode = WakeupModeOnce
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	const q = `UPDATE job_wakeups SET
  kind = ?, at = ?, every_sec = ?, cron_expr = ?, timezone = ?, event_types_json = ?,
  filter_job_id = ?, filter_status_json = ?, mode = ?, instruction = ?, enabled = ?,
  next_run_at = ?, expires_at = ?, revision = COALESCE(revision,1) + 1
  WHERE id = ?`
	if _, err := s.db.Exec(q,
		r.Kind, r.At, r.EverySec, r.CronExpr, r.Timezone, r.EventTypesJSON,
		r.FilterJobID, r.FilterStatusJSON, r.Mode, r.Instruction, r.Enabled,
		r.NextRunAt, r.ExpiresAt, r.ID,
	); err != nil {
		return fmt.Errorf("jobstore: update wakeup %q: %w", r.ID, err)
	}
	return nil
}

// SetWakeupEnabled flips a wakeup's switch (the enable/disable surface and the
// once-consumption / TTL-expiry writes).
func (s *Store) SetWakeupEnabled(id string, enabled int) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(`UPDATE job_wakeups SET enabled = ? WHERE id = ?`, enabled, id); err != nil {
		return fmt.Errorf("jobstore: set wakeup %q enabled: %w", id, err)
	}
	return nil
}

// DueWakeups returns the ENABLED wakeups the sweeper must act on at now: either a
// timer that has come due (next_run_at <= now) or one whose TTL has passed
// (expires_at <= now — the sweeper disables those and records the expiry). One
// query serves both passes, so a stop-the-world tick cannot miss an expiry behind
// a due timer.
func (s *Store) DueWakeups(now int64) ([]WakeupRecord, error) {
	return s.listWakeups(
		selectWakeupCols+` WHERE enabled = 1 AND (
      (next_run_at > 0 AND next_run_at <= ?) OR
      (COALESCE(expires_at,0) > 0 AND expires_at <= ?))
     ORDER BY COALESCE(NULLIF(next_run_at,0), expires_at) ASC, id ASC`,
		now, now,
	)
}

// AdvanceWakeup moves a due timer's next_run_at with a compare-and-swap, so when
// two sweeps (or a sweep and an enable) race, exactly one of them fires that tick.
// newNext == 0 marks a consumed one-shot. It mirrors AdvanceSchedule.
func (s *Store) AdvanceWakeup(id string, oldNext, newNext, now int64) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(
		`UPDATE job_wakeups SET next_run_at = ?, last_fired_at = ? WHERE id = ? AND enabled = 1 AND COALESCE(next_run_at,0) = ?`,
		newNext, now, id, oldNext,
	)
	if err != nil {
		return false, fmt.Errorf("jobstore: advance wakeup %q: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("jobstore: advance wakeup %q rows: %w", id, err)
	}
	return n == 1, nil
}

// ClaimWakeupFire takes the wakeup's single continuation slot: it writes
// continuationJobID (normally WakeupClaimPending — see that constant) and bumps
// the fire counters, but ONLY when the wakeup is enabled and its slot is free. The
// boolean reports whether THIS call won the slot; a caller that loses must not
// submit a continuation (and counts a coalesce instead).
func (s *Store) ClaimWakeupFire(id, continuationJobID string) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(
		`UPDATE job_wakeups
     SET continuation_job_id = ?, last_fired_at = ?, fired_count = COALESCE(fired_count,0) + 1
     WHERE id = ? AND enabled = 1 AND COALESCE(continuation_job_id,'') = ''`,
		continuationJobID, time.Now().Unix(), id,
	)
	if err != nil {
		return false, fmt.Errorf("jobstore: claim wakeup %q fire: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("jobstore: claim wakeup %q rows: %w", id, err)
	}
	return n == 1, nil
}

// SetWakeupContinuation replaces the in-flight claim with the real continuation
// job id. It CASes on the pending sentinel, so a fire that had already lost its
// slot can never overwrite the winner's continuation.
func (s *Store) SetWakeupContinuation(id, jobID string) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(
		`UPDATE job_wakeups SET continuation_job_id = ? WHERE id = ? AND continuation_job_id = ?`,
		jobID, id, WakeupClaimPending,
	)
	if err != nil {
		return false, fmt.Errorf("jobstore: set wakeup %q continuation: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("jobstore: set wakeup %q continuation rows: %w", id, err)
	}
	return n == 1, nil
}

// ReleaseWakeupClaim frees the continuation slot when it still holds the value the
// caller expects (the pending sentinel after a failed submit, or the id of a
// continuation that has since reached a terminal state). The CAS keeps a release
// from clearing a slot another fire just took.
func (s *Store) ReleaseWakeupClaim(id, continuationJobID string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(
		`UPDATE job_wakeups SET continuation_job_id = '' WHERE id = ? AND continuation_job_id = ?`,
		id, continuationJobID,
	); err != nil {
		return fmt.Errorf("jobstore: release wakeup %q claim: %w", id, err)
	}
	return nil
}

// CoalesceWakeup counts one trigger that found the continuation slot occupied and
// therefore did NOT start a second continuation (design §五 决策 6). last_fired_at
// moves too: a reader asking "when did this wakeup last want to run" gets the
// trigger, not only the fires that produced a job.
func (s *Store) CoalesceWakeup(id string, now int64) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(
		`UPDATE job_wakeups SET coalesced_count = COALESCE(coalesced_count,0) + 1, last_fired_at = ? WHERE id = ?`,
		now, id,
	); err != nil {
		return fmt.Errorf("jobstore: coalesce wakeup %q: %w", id, err)
	}
	return nil
}

// DeleteWakeup removes a wakeup row (the `job wakeup rm` path).
func (s *Store) DeleteWakeup(id string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(`DELETE FROM job_wakeups WHERE id = ?`, id); err != nil {
		return fmt.Errorf("jobstore: delete wakeup %q: %w", id, err)
	}
	return nil
}

// MatchingEventWakeups returns the ENABLED event wakeups that subscribe to
// eventType raised by sourceJobID: kind=event, the source job matches the filter
// (an empty filter_job_id means "the job the wakeup is registered on" — CreateWakeup
// always fills it, so this only covers a hand-written row) and event_types_json
// contains the type. The status filter (job.terminal only) is applied by the
// caller, which is where that vocabulary lives.
func (s *Store) MatchingEventWakeups(sourceJobID, eventType string) ([]WakeupRecord, error) {
	return s.listWakeups(
		selectWakeupCols+` WHERE enabled = 1 AND kind = ? AND (COALESCE(filter_job_id,'') = '' OR filter_job_id = ?)
       AND EXISTS (SELECT 1 FROM json_each(COALESCE(NULLIF(event_types_json,''),'[]')) AS ev WHERE ev.value = ?)
     ORDER BY created_at ASC, id ASC`,
		WakeupKindEvent, sourceJobID, eventType,
	)
}

func (s *Store) listWakeups(q string, args ...any) ([]WakeupRecord, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list wakeups: %w", err)
	}
	defer rows.Close()

	out := make([]WakeupRecord, 0)
	for rows.Next() {
		r, scanErr := scanWakeup(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("jobstore: scan wakeup row: %w", scanErr)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list wakeup rows: %w", err)
	}
	return out, nil
}

// EncodeStringList marshals a string slice into the JSON array an event_types /
// filter_status column holds. A nil/empty slice yields "" — an empty column reads
// back as "no values" everywhere, which is also what an absent filter means.
func EncodeStringList(vals []string) string {
	if len(vals) == 0 {
		return ""
	}
	b, err := json.Marshal(vals)
	if err != nil {
		return "" // []string cannot fail to marshal; an empty filter is the safe read
	}
	return string(b)
}

// DecodeStringList parses such a column back; a blank/unreadable value yields nil,
// so a corrupt filter degrades to "no filter" instead of failing a read path.
func DecodeStringList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}
