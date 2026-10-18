package jobstore

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/gookit/goutil/x/assert"

	_ "modernc.org/sqlite"
)

// TestMigrateAddsOriginCallerToOldDB simulates a database whose jobs table predates
// the caller-ceiling work (design 2026-10-18): Re-Open must additively add
// `origin_caller` and the pre-existing row must read back an empty owner — i.e. a
// system job, which the job layer admits without a ceiling check.
func TestMigrateAddsOriginCallerToOldDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old-origin.db")

	raw, err := sql.Open("sqlite", "file:"+path)
	assert.NoErr(t, err)
	_, err = raw.Exec(`CREATE TABLE jobs (
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
	  updated_at   INTEGER NOT NULL,
	  caller_id    TEXT
	)`)
	assert.NoErr(t, err)
	_, err = raw.Exec(`INSERT INTO jobs (id, project_key, agent, runner, status, result_dir, started_at, updated_at, caller_id)
VALUES ('old-job', 'proj', 'exec', 'local', 'done', '/tmp/old-job', 1, 1, 'alice')`)
	assert.NoErr(t, err)
	assert.NoErr(t, raw.Close())

	s, err := Open(path)
	assert.NoErr(t, err)
	defer s.Close()

	assert.True(t, tableHasColumn(t, s, "jobs", "origin_caller"))

	rec, ok, err := s.GetJob("old-job")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "alice", rec.CallerID)
	assert.Eq(t, "", rec.OriginCaller)
}

// TestJobOriginCallerRoundTrip pins the column to the read/write path: the ceiling
// owner of a job survives a write and a read-back, and an update of the row carries it
// too (the upsert must not clear it).
func TestJobOriginCallerRoundTrip(t *testing.T) {
	s := openTest(t)
	defer s.Close()

	rec := sampleJob("job-origin", "proj", 1)
	rec.CallerID = "job-source"
	rec.OriginCaller = "ci-bot"
	assert.NoErr(t, s.UpsertJob(rec))

	got, ok, err := s.GetJob("job-origin")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "ci-bot", got.OriginCaller)

	// A later upsert (the finish write) keeps the owner.
	rec.Status = "done"
	assert.NoErr(t, s.UpsertJob(rec))
	got, ok, err = s.GetJob("job-origin")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "ci-bot", got.OriginCaller)
}
