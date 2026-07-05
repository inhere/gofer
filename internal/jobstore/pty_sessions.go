package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
)

// PtySessionRecord is the SQLite-persisted metadata of one established pty relay
// recording (WEB-03 P3, design §3). Like JobRecord it is a neutral struct so this
// package stays decoupled from internal/job / internal/httpapi. httpapi is the
// sole writer: it upserts an `open` row when the relay opens and a `closed` row
// (same PK) at finalize. Encrypted is 1 (yes) / 2 (no) per SR301; RecordingURI is
// <result_dir>/pty.cast, empty when the recording is absent / write-failed /
// TTL-expired. Times are unix seconds; EndedAt 0 means still open.
type PtySessionRecord struct {
	PtySessionID string
	JobID        string
	WorkerID     string
	InstanceID   string
	Owner        string
	State        string
	Cols         int
	Rows         int
	RecordingURI string
	Encrypted    int
	BytesIn      int64
	BytesOut     int64
	StartedAt    int64
	EndedAt      int64
}

// selectPtyCols is the shared projection for pty_sessions reads. COALESCE guards
// the nullable columns so a NULL scans into the zero value instead of failing.
const selectPtyCols = `SELECT pty_session_id, job_id, COALESCE(worker_id,''),
  COALESCE(instance_id,''), COALESCE(owner,''), state, COALESCE(cols,0),
  COALESCE(rows,0), COALESCE(recording_uri,''), COALESCE(encrypted,2),
  COALESCE(bytes_in,0), COALESCE(bytes_out,0), started_at, COALESCE(ended_at,0)
  FROM pty_sessions`

// scanPtySession reads one row (in selectPtyCols order) into a PtySessionRecord.
func scanPtySession(sc rowScanner) (PtySessionRecord, error) {
	var r PtySessionRecord
	err := sc.Scan(
		&r.PtySessionID, &r.JobID, &r.WorkerID, &r.InstanceID, &r.Owner,
		&r.State, &r.Cols, &r.Rows, &r.RecordingURI, &r.Encrypted,
		&r.BytesIn, &r.BytesOut, &r.StartedAt, &r.EndedAt,
	)
	return r, err
}

// UpsertPtySession inserts a pty session row or updates the existing one with the
// same pty_session_id. The open and closed writes for one session are two upserts
// on the same PK (the closed snapshot overwrites the open one), so the table keeps
// a single latest row per session. Encrypted defaults to 2 (no) when left zero so
// the SR301 "avoid 0" invariant holds. Writes go through writeMu like every other
// writer so SQLite never sees two concurrent writers.
func (s *Store) UpsertPtySession(rec PtySessionRecord) error {
	if rec.PtySessionID == "" {
		return errors.New("jobstore: UpsertPtySession: empty pty_session_id")
	}
	if rec.JobID == "" {
		return errors.New("jobstore: UpsertPtySession: empty job id")
	}
	if rec.Encrypted == 0 {
		rec.Encrypted = 2
	}
	const q = `INSERT INTO pty_sessions
  (pty_session_id, job_id, worker_id, instance_id, owner, state, cols, rows,
   recording_uri, encrypted, bytes_in, bytes_out, started_at, ended_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
  ON CONFLICT(pty_session_id) DO UPDATE SET
    job_id=excluded.job_id,
    worker_id=excluded.worker_id,
    instance_id=excluded.instance_id,
    owner=excluded.owner,
    state=excluded.state,
    cols=excluded.cols,
    rows=excluded.rows,
    recording_uri=excluded.recording_uri,
    encrypted=excluded.encrypted,
    bytes_in=excluded.bytes_in,
    bytes_out=excluded.bytes_out,
    started_at=excluded.started_at,
    ended_at=excluded.ended_at`
	var endedAt any
	if rec.EndedAt > 0 {
		endedAt = rec.EndedAt
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(q,
		rec.PtySessionID, rec.JobID, rec.WorkerID, rec.InstanceID, rec.Owner,
		rec.State, rec.Cols, rec.Rows, rec.RecordingURI, rec.Encrypted,
		rec.BytesIn, rec.BytesOut, rec.StartedAt, endedAt,
	)
	if err != nil {
		return fmt.Errorf("jobstore: upsert pty session %q: %w", rec.PtySessionID, err)
	}
	return nil
}

// GetPtySessionByJob returns the most recent pty session for a job (the recording
// download gate looks it up by job id). "Most recent" is the latest started_at,
// then pty_session_id desc as a stable tiebreaker. The bool is false (nil error)
// when the job has no pty session.
func (s *Store) GetPtySessionByJob(jobID string) (PtySessionRecord, bool, error) {
	rec, err := scanPtySession(s.db.QueryRow(
		selectPtyCols+" WHERE job_id = ? ORDER BY started_at DESC, pty_session_id DESC LIMIT 1",
		jobID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return PtySessionRecord{}, false, nil
	}
	if err != nil {
		return PtySessionRecord{}, false, fmt.Errorf("jobstore: get pty session by job %q: %w", jobID, err)
	}
	return rec, true, nil
}

// ExpireCastRecordings clears the recording of every closed pty session whose cast
// TTL has elapsed (retention regime 1, design §D-P3-6). It runs in one transaction:
// SELECT the expired recording_uris (so the caller can best-effort delete the cast
// files on disk), then UPDATE those rows to drop recording_uri and mark encrypted=2
// — the session ROW is retained (owner/state/bytes/timestamps stay for audit), only
// the recording pointer is cleared. now / ttlSeconds are injected; a session is
// expired when ended_at is set (>0) and ended_at + ttlSeconds < now and it still
// has a recording_uri. A non-positive ttlSeconds prunes nothing. Signature matches
// the store-does-not-hold-config contract: the caller passes the TTL.
func (s *Store) ExpireCastRecordings(now, ttlSeconds int64) (uris []string, err error) {
	if ttlSeconds <= 0 {
		return nil, nil
	}
	cutoff := now - ttlSeconds

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("jobstore: expire cast begin tx: %w", err)
	}
	const sel = `SELECT recording_uri FROM pty_sessions
  WHERE recording_uri IS NOT NULL AND recording_uri != ''
    AND ended_at IS NOT NULL AND ended_at > 0 AND ended_at < ?`
	rows, qerr := tx.Query(sel, cutoff)
	if qerr != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("jobstore: expire cast select: %w", qerr)
	}
	for rows.Next() {
		var uri string
		if scanErr := rows.Scan(&uri); scanErr != nil {
			rows.Close()
			_ = tx.Rollback()
			return nil, fmt.Errorf("jobstore: expire cast scan: %w", scanErr)
		}
		if uri != "" {
			uris = append(uris, uri)
		}
	}
	if rErr := rows.Err(); rErr != nil {
		rows.Close()
		_ = tx.Rollback()
		return nil, fmt.Errorf("jobstore: expire cast select rows: %w", rErr)
	}
	rows.Close()
	if len(uris) == 0 {
		// Nothing expired; commit the (empty) tx so it is not left open.
		if cErr := tx.Commit(); cErr != nil {
			return nil, fmt.Errorf("jobstore: expire cast commit: %w", cErr)
		}
		return nil, nil
	}
	const upd = `UPDATE pty_sessions SET recording_uri = '', encrypted = 2
  WHERE recording_uri IS NOT NULL AND recording_uri != ''
    AND ended_at IS NOT NULL AND ended_at > 0 AND ended_at < ?`
	if _, uErr := tx.Exec(upd, cutoff); uErr != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("jobstore: expire cast update: %w", uErr)
	}
	if cErr := tx.Commit(); cErr != nil {
		return nil, fmt.Errorf("jobstore: expire cast commit: %w", cErr)
	}
	return uris, nil
}
