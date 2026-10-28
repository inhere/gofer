package jobstore

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"time"
)

// PreviewRecord is the persisted, non-secret preview lease.
type PreviewRecord struct {
	ID, JobID, CallerID, WorkerID, WorkerInstanceID string
	TokenHash                                       string
	Port                                            int
	State                                           string
	CreatedAt, ExpiresAt, LastUsedAt, ClosedAt      int64
	CloseReason                                     string
	ConnectionCount                                 int
}

var previewSchema = []string{
	`CREATE TABLE IF NOT EXISTS job_previews (
 id TEXT PRIMARY KEY, token_hash TEXT NOT NULL UNIQUE, job_id TEXT NOT NULL, caller_id TEXT NOT NULL,
 worker_id TEXT NOT NULL, worker_instance_id TEXT NOT NULL, port INTEGER NOT NULL, state TEXT NOT NULL,
 created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, last_used_at INTEGER, closed_at INTEGER,
 close_reason TEXT, connection_count INTEGER NOT NULL DEFAULT 0)`,
	`CREATE INDEX IF NOT EXISTS idx_job_previews_job ON job_previews(job_id)`,
	`CREATE INDEX IF NOT EXISTS idx_job_previews_expiry ON job_previews(state, expires_at)`,
}

func init() { schemaStmts = append(schemaStmts, previewSchema...) }
func PreviewTokenHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func (s *Store) InsertPreview(p PreviewRecord) error {
	_, err := s.db.Exec(`INSERT INTO job_previews(id,token_hash,job_id,caller_id,worker_id,worker_instance_id,port,state,created_at,expires_at,last_used_at,closed_at,close_reason,connection_count) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, p.ID, p.TokenHash, p.JobID, p.CallerID, p.WorkerID, p.WorkerInstanceID, p.Port, p.State, p.CreatedAt, p.ExpiresAt, nilIfZero(p.LastUsedAt), nilIfZero(p.ClosedAt), p.CloseReason, p.ConnectionCount)
	return err
}
func nilIfZero(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}
func scanPreview(row interface{ Scan(...any) error }) (PreviewRecord, error) {
	var p PreviewRecord
	var last, closed sql.NullInt64
	err := row.Scan(&p.ID, &p.TokenHash, &p.JobID, &p.CallerID, &p.WorkerID, &p.WorkerInstanceID, &p.Port, &p.State, &p.CreatedAt, &p.ExpiresAt, &last, &closed, &p.CloseReason, &p.ConnectionCount)
	p.LastUsedAt = last.Int64
	p.ClosedAt = closed.Int64
	return p, err
}
func (s *Store) GetPreview(id string) (PreviewRecord, bool, error) {
	p, e := scanPreview(s.db.QueryRow(`SELECT id,token_hash,job_id,caller_id,worker_id,worker_instance_id,port,state,created_at,expires_at,last_used_at,closed_at,close_reason,connection_count FROM job_previews WHERE id=?`, id))
	if e == sql.ErrNoRows {
		return PreviewRecord{}, false, nil
	}
	return p, true, e
}
func (s *Store) GetPreviewByTokenHash(hash string) (PreviewRecord, bool, error) {
	p, e := scanPreview(s.db.QueryRow(`SELECT id,token_hash,job_id,caller_id,worker_id,worker_instance_id,port,state,created_at,expires_at,last_used_at,closed_at,close_reason,connection_count FROM job_previews WHERE token_hash=?`, hash))
	if e == sql.ErrNoRows {
		return PreviewRecord{}, false, nil
	}
	return p, true, e
}
func (s *Store) ListPreviews(jobID string) ([]PreviewRecord, error) {
	rows, e := s.db.Query(`SELECT id,token_hash,job_id,caller_id,worker_id,worker_instance_id,port,state,created_at,expires_at,last_used_at,closed_at,close_reason,connection_count FROM job_previews WHERE job_id=? ORDER BY created_at DESC`, jobID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []PreviewRecord
	for rows.Next() {
		p, e := scanPreview(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Store) UpdatePreviewState(id, state, reason string, closedAt int64) error {
	_, e := s.db.Exec(`UPDATE job_previews SET state=?,close_reason=?,closed_at=? WHERE id=?`, state, reason, nilIfZero(closedAt), id)
	return e
}
func (s *Store) TouchPreview(id string, n int) error {
	_, e := s.db.Exec(`UPDATE job_previews SET last_used_at=?,connection_count=? WHERE id=?`, time.Now().Unix(), n, id)
	return e
}
func (s *Store) ClosePreviewsForJob(jobID, reason string, at int64) error {
	_, e := s.db.Exec(`UPDATE job_previews SET state='job_ended',close_reason=?,closed_at=? WHERE job_id=? AND state='active'`, reason, at, jobID)
	return e
}
func (s *Store) ExpirePreviews(at int64) error {
	_, e := s.db.Exec(`UPDATE job_previews SET state='expired',close_reason='expired',closed_at=? WHERE state='active' AND expires_at<=?`, at, at)
	return e
}
