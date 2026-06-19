// Package jobstore is the SQLite-backed metadata/index store for gofer
// jobs. It is the C1 fix (see docs/design/2026-06-18-sqlite-store-design.md):
// the in-memory job table, jobs.jsonl index and result.json metadata all grow
// without bound on a long-running server. This package moves that state into a
// single SQLite database so listing is one filtered/paginated SQL query and
// terminal jobs no longer have to live in memory.
//
// Job logs (stdout.log/stderr.log) stay as files in the per-job result dir; only
// metadata/index (and, from SP4, interactions) live here.
//
// The package uses modernc.org/sqlite (pure Go, no cgo) so the binary still
// builds in the gcc-less container. It depends on no other internal package — in
// particular NOT internal/job — so that the job service can adopt it (SP2/SP3)
// without forming a job -> jobstore -> job import cycle; JobRecord is therefore a
// neutral struct rather than job.JobResult.
package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

const (
	// DefaultListLimit caps ListJobs when the caller passes Limit <= 0. Mirrors
	// the job package's list default so behaviour is unchanged after the cutover.
	DefaultListLimit = 200
	// busyTimeoutMS is how long a blocked writer waits for the database lock
	// before failing with SQLITE_BUSY. Writes are tiny and infrequent (status /
	// interaction changes), so a few seconds absorbs any realistic contention.
	busyTimeoutMS = 5000
)

// Store is a handle to the SQLite job database. It is safe for concurrent use:
// the underlying *sql.DB is a connection pool and SQLite (in WAL mode) lets
// readers and the single writer proceed concurrently.
//
// writeMu serialises writes in-process so only one SQLite writer is ever active.
// WAL + busy_timeout alone proved insufficient under full-speed concurrent
// upserts (intermittent SQLITE_BUSY "database is locked"); since this is a
// single process owning a single db file, an in-process write lock removes the
// contention entirely while leaving reads (GetJob/ListJobs) free to run on the
// pool concurrently.
type Store struct {
	db      *sql.DB
	writeMu sync.Mutex
}

// schemaStmts is the full DDL, one statement per element so it works regardless
// of whether the driver supports multi-statement Exec. Both tables are created
// up front (建库/建表); SP1 only exercises the jobs table, the interactions table
// is populated from SP4. All statements are IF NOT EXISTS so Open is idempotent.
var schemaStmts = []string{
	`CREATE TABLE IF NOT EXISTS jobs (
  id           TEXT PRIMARY KEY,
  project_key  TEXT NOT NULL,
  agent        TEXT NOT NULL,
  runner       TEXT NOT NULL,
  worker_id    TEXT,
  status       TEXT NOT NULL,
  exit_code    INTEGER NOT NULL DEFAULT 0,
  cwd          TEXT,
  result_dir   TEXT NOT NULL,
  request_json TEXT,
  error        TEXT,
  started_at   INTEGER NOT NULL,
  ended_at     INTEGER,
  updated_at   INTEGER NOT NULL
)`,
	`CREATE INDEX IF NOT EXISTS idx_jobs_started ON jobs(started_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_jobs_proj_status ON jobs(project_key, status)`,
	`CREATE TABLE IF NOT EXISTS interactions (
  id           TEXT NOT NULL,
  job_id       TEXT NOT NULL,
  type         TEXT NOT NULL,
  prompt       TEXT NOT NULL,
  options_json TEXT,
  status       TEXT NOT NULL,
  answer       TEXT,
  created_at   INTEGER NOT NULL,
  answered_at  INTEGER,
  PRIMARY KEY (job_id, id)
)`,
	`CREATE INDEX IF NOT EXISTS idx_inter_job ON interactions(job_id)`,
}

// Open opens (creating if absent) the SQLite database at path, applies the schema
// and returns a ready Store. The parent directory is created if needed; the db
// file is restricted to 0600 (private; see design §12). Callers must Close it.
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("jobstore: empty db path")
	}
	// SQLite creates the db file but not its parent directory.
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("jobstore: create db dir: %w", err)
		}
	}

	// modernc applies every _pragma to EACH pooled connection as it is opened,
	// so busy_timeout/foreign_keys hold for all goroutines (not just the first).
	// WAL is a persistent db setting; re-asserting it per connection is harmless.
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(%d)&_pragma=foreign_keys(1)",
		path, busyTimeoutMS,
	)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("jobstore: open %q: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("jobstore: ping %q: %w", path, err)
	}

	s := &Store{db: db}
	if err := s.applySchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	// Best-effort: the db (and its -wal/-shm side files) live in the private logs
	// area; tighten perms on the main file regardless of umask.
	_ = os.Chmod(path, 0o600)
	return s, nil
}

// applySchema runs the DDL. Each statement is idempotent (IF NOT EXISTS), so it
// is safe to call on every Open.
func (s *Store) applySchema() error {
	for _, stmt := range schemaStmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("jobstore: apply schema: %w", err)
		}
	}
	return nil
}

// Close closes the underlying database. WAL auto-checkpoints on the final close,
// so no explicit checkpoint is needed for graceful shutdown (design §14).
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
