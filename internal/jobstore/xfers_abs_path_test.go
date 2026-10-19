package jobstore

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/gookit/goutil/x/assert"

	_ "modernc.org/sqlite"
)

// TestMigrateXfersAddsAbsPath simulates a database written BEFORE the absolute-path
// report: the xfers table has no abs_path column. Re-Open must add it additively, the
// legacy row must read back as "not reported" (empty) with its other fields intact, and
// the new setter must round-trip through the migrated table.
func TestMigrateXfersAddsAbsPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")

	raw, err := sql.Open("sqlite", "file:"+path)
	assert.NoErr(t, err)
	_, err = raw.Exec(`CREATE TABLE xfers (
  id           TEXT PRIMARY KEY,
  op           TEXT NOT NULL,
  runner       TEXT NOT NULL,
  project_key  TEXT NOT NULL,
  path         TEXT NOT NULL,
  size         INTEGER NOT NULL DEFAULT 0,
  sha256       TEXT,
  state        TEXT NOT NULL,
  error        TEXT,
  caller_id    TEXT,
  force        INTEGER NOT NULL DEFAULT 0,
  job_id       TEXT,
  created_at   INTEGER NOT NULL,
  finished_at  INTEGER,
  expires_at   INTEGER
)`)
	assert.NoErr(t, err)
	_, err = raw.Exec(`INSERT INTO xfers (id, op, runner, project_key, path, size, sha256, state, caller_id, created_at)
VALUES ('xf-legacy01', 'put', 'w1', 'alpha', 'tmp/in/a.bin', 3, 'ab12', 'done', 'alice', 100)`)
	assert.NoErr(t, err)
	assert.NoErr(t, raw.Close())

	s, err := Open(path)
	assert.NoErr(t, err)
	defer s.Close()

	assert.True(t, tableHasColumn(t, s, "xfers", "abs_path"))
	assert.NoErr(t, s.migrate()) // idempotent second pass: the column already exists.

	legacy, ok, err := s.GetXfer("xf-legacy01")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "", legacy.AbsPath, "a pre-column row reports no absolute path")
	assert.Eq(t, "tmp/in/a.bin", legacy.Path)
	assert.Eq(t, "done", legacy.State)
	assert.Eq(t, int64(3), legacy.Size)

	assert.NoErr(t, s.SetXferAbsPath("xf-legacy01", "/home/ci/alpha/tmp/in/a.bin"))
	updated, ok, err := s.GetXfer("xf-legacy01")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "/home/ci/alpha/tmp/in/a.bin", updated.AbsPath)

	// A row inserted with the path already set round-trips it too (the insert carries the
	// column, so a caller that knows the path up front does not need a second write).
	fresh := XferRecord{
		ID: "xf-fresh01", Op: "put", Runner: "local", ProjectKey: "alpha",
		Path: "tmp/gofer-attachments/20261019/xf-fresh01.png", AbsPath: "/srv/alpha/tmp/gofer-attachments/20261019/xf-fresh01.png",
		Size: 3, State: "done", CallerID: "alice", CreatedAt: 200,
	}
	assert.NoErr(t, s.InsertXfer(fresh))
	got, ok, err := s.GetXfer(fresh.ID)
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, fresh.AbsPath, got.AbsPath)
	assert.Eq(t, "", got.JobID)
}
