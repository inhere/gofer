package jobstore

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/gookit/goutil/x/assert"

	_ "modernc.org/sqlite"
)

// tableHasColumn reports whether table has a column named col via PRAGMA.
func tableHasColumn(t *testing.T, s *Store, table, col string) bool {
	t.Helper()
	cols, err := s.tableColumns(table)
	assert.NoErr(t, err)
	_, ok := cols[col]
	return ok
}

// indexExists reports whether an index of the given name exists on the db.
func indexExists(t *testing.T, s *Store, name string) bool {
	t.Helper()
	var n string
	err := s.db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='index' AND name = ?", name,
	).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	assert.NoErr(t, err)
	return n == name
}

// TestMigrateAddsColumnsToOldDB simulates a pre-existing C1 database that has
// the jobs table WITHOUT the caller_id/request_id columns and without the
// request_id index. Re-Open must additively add both columns and the partial
// unique index (the old-DB ordering hazard: the index references a column that
// did not exist until the ALTER runs).
func TestMigrateAddsColumnsToOldDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")

	// Build a minimal "old" jobs table lacking the new columns, then close.
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
	  updated_at   INTEGER NOT NULL
	)`)
	assert.NoErr(t, err)
	assert.NoErr(t, raw.Close())

	// Re-open via the package: applySchema is a no-op (table exists), migrate
	// must add the new columns + index.
	s, err := Open(path)
	assert.NoErr(t, err)
	defer s.Close()

	assert.True(t, tableHasColumn(t, s, "jobs", "caller_id"))
	assert.True(t, tableHasColumn(t, s, "jobs", "request_id"))
	assert.True(t, indexExists(t, s, "idx_jobs_request_id"))
	// 产出与审计（job-outcomes-audit）：旧库经 migrate 必须补全这 4 列。
	assert.True(t, tableHasColumn(t, s, "jobs", "rendered_command"))
	assert.True(t, tableHasColumn(t, s, "jobs", "result_json"))
	assert.True(t, tableHasColumn(t, s, "jobs", "artifacts_json"))
	assert.True(t, tableHasColumn(t, s, "jobs", "diff_summary"))
	// E5：旧库经 migrate 必须补全 tags_json 列。
	assert.True(t, tableHasColumn(t, s, "jobs", "tags_json"))
	// session 捕获：旧库经 migrate 必须补全 session_id 列。
	assert.True(t, tableHasColumn(t, s, "jobs", "session_id"))
	// 提交来源（provenance）：旧库经 migrate 必须补全 channel / client 列。
	assert.True(t, tableHasColumn(t, s, "jobs", "channel"))
	assert.True(t, tableHasColumn(t, s, "jobs", "client"))
	// 监督分层升级路由（supervisor-routing P1.1）：旧库经 migrate 必须补全 origin_agent /
	// escalate_to 列。
	assert.True(t, tableHasColumn(t, s, "jobs", "origin_agent"))
	assert.True(t, tableHasColumn(t, s, "jobs", "escalate_to"))

	// The migrated DB is usable: a job with a request_id round-trips.
	rec := sampleJob("j1", "proj", 100)
	rec.RequestID = "req-1"
	rec.CallerID = "caller-a"
	assert.NoErr(t, s.UpsertJob(rec))
	got, ok, err := s.GetJobByRequestID("req-1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "j1", got.ID)
	assert.Eq(t, "caller-a", got.CallerID)

	// 旧库里早先没有新列的 job 读出时新字段为空（回归不破）。
	old := sampleJob("j-old", "proj", 50)
	assert.NoErr(t, s.UpsertJob(old))
	gotOld, ok, err := s.GetJob("j-old")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "", gotOld.RenderedCommand)
	assert.Eq(t, "", gotOld.ResultJSON)
	assert.Eq(t, "", gotOld.ArtifactsJSON)
	assert.Eq(t, "", gotOld.DiffSummary)
	assert.Eq(t, "", gotOld.TagsJSON)
	assert.Eq(t, "", gotOld.SessionID)
}

// TestFreshOpenHasNewColumnsAndIndex asserts a brand-new database gets the new
// columns and the partial unique index in one Open (schema + migrate).
func TestFreshOpenHasNewColumnsAndIndex(t *testing.T) {
	s := openTest(t)
	assert.True(t, tableHasColumn(t, s, "jobs", "caller_id"))
	assert.True(t, tableHasColumn(t, s, "jobs", "request_id"))
	assert.True(t, indexExists(t, s, "idx_jobs_request_id"))
	// 产出与审计（job-outcomes-audit）：新库一次建全这 4 列。
	assert.True(t, tableHasColumn(t, s, "jobs", "rendered_command"))
	assert.True(t, tableHasColumn(t, s, "jobs", "result_json"))
	assert.True(t, tableHasColumn(t, s, "jobs", "artifacts_json"))
	assert.True(t, tableHasColumn(t, s, "jobs", "diff_summary"))
	// E5：新库一次建全 tags_json 列。
	assert.True(t, tableHasColumn(t, s, "jobs", "tags_json"))
	// session 捕获：新库一次建全 session_id 列。
	assert.True(t, tableHasColumn(t, s, "jobs", "session_id"))
	// 监督分层升级路由（supervisor-routing P1.1）：新库一次建全 origin_agent / escalate_to 列，
	// interactions 一次建全 escalated_at 列。
	assert.True(t, tableHasColumn(t, s, "jobs", "origin_agent"))
	assert.True(t, tableHasColumn(t, s, "jobs", "escalate_to"))
	assert.True(t, tableHasColumn(t, s, "interactions", "escalated_at"))
}

// TestMigrateAddsEscalatedAtToOldInteractions simulates a pre-existing database
// whose interactions table lacks the escalated_at column (added in
// supervisor-routing P1.1). Re-Open must additively ALTER ADD it, and an
// interaction round-trips through the migrated table with escalated_at defaulting
// to 0 (COALESCE) for a row that never set it.
func TestMigrateAddsEscalatedAtToOldInteractions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old-inter.db")

	// Build a minimal "old" interactions table lacking escalated_at, then close.
	raw, err := sql.Open("sqlite", "file:"+path)
	assert.NoErr(t, err)
	_, err = raw.Exec(`CREATE TABLE interactions (
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
	)`)
	assert.NoErr(t, err)
	assert.NoErr(t, raw.Close())

	// Re-open via the package: migrate must add escalated_at.
	s, err := Open(path)
	assert.NoErr(t, err)
	defer s.Close()
	assert.True(t, tableHasColumn(t, s, "interactions", "escalated_at"))

	// A pending interaction (no escalated_at set) round-trips; the column reads 0.
	rec := InteractionRecord{
		ID: "i1", JobID: "j1", Type: "question", Prompt: "?",
		Status: "pending", CreatedAt: 100,
	}
	assert.NoErr(t, s.UpsertInteraction(rec))
	got, err := s.ListInteractions("j1")
	assert.NoErr(t, err)
	assert.Len(t, got, 1)
	assert.Eq(t, int64(0), got[0].EscalatedAt)
}

// TestUpsertGetOutcomeFields covers the round-trip of the 4 产出与审计 columns
// (job-outcomes-audit): a record carrying all four reads back identical.
func TestUpsertGetOutcomeFields(t *testing.T) {
	s := openTest(t)

	in := sampleJob("j-out", "proj", 700)
	in.RenderedCommand = `{"command":"go","args":["version"],"env_keys":["PATH"]}`
	in.ResultJSON = `{"ok":true,"count":3}`
	in.ArtifactsJSON = `[{"name":"a.txt","size":12,"mtime":1}]`
	in.DiffSummary = " a.txt | 2 +-\n 1 file changed"
	assert.NoErr(t, s.UpsertJob(in))

	got, ok, err := s.GetJob("j-out")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, in.RenderedCommand, got.RenderedCommand)
	assert.Eq(t, in.ResultJSON, got.ResultJSON)
	assert.Eq(t, in.ArtifactsJSON, got.ArtifactsJSON)
	assert.Eq(t, in.DiffSummary, got.DiffSummary)
}

// TestGetJobByRequestID covers the round-trip and the empty-string short
// circuit (empty reqID is "no key" -> not found, no DB hit).
func TestGetJobByRequestID(t *testing.T) {
	s := openTest(t)

	rec := sampleJob("j-rid", "p", 500)
	rec.RequestID = "rid-42"
	assert.NoErr(t, s.UpsertJob(rec))

	got, ok, err := s.GetJobByRequestID("rid-42")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "j-rid", got.ID)
	assert.Eq(t, "rid-42", got.RequestID)

	// Empty key returns false, no error.
	_, ok, err = s.GetJobByRequestID("")
	assert.NoErr(t, err)
	assert.False(t, ok)

	// Unknown key returns false, no error.
	_, ok, err = s.GetJobByRequestID("nope")
	assert.NoErr(t, err)
	assert.False(t, ok)
}

// TestRequestIDUniqueConflict asserts the partial unique index: two DIFFERENT
// job ids with the SAME non-empty request_id collide on the second upsert
// (ErrRequestIDConflict), while empty request_id never collides.
func TestRequestIDUniqueConflict(t *testing.T) {
	s := openTest(t)

	a := sampleJob("job-a", "p", 100)
	a.RequestID = "dup"
	assert.NoErr(t, s.UpsertJob(a))

	b := sampleJob("job-b", "p", 200)
	b.RequestID = "dup"
	err := s.UpsertJob(b)
	assert.Err(t, err)
	assert.True(t, errors.Is(err, ErrRequestIDConflict))

	// Re-upserting the SAME id with the SAME request_id is an in-place update,
	// not a conflict.
	a.Status = "done"
	assert.NoErr(t, s.UpsertJob(a))

	// Two jobs with empty request_id never collide.
	e1 := sampleJob("e1", "p", 300)
	e2 := sampleJob("e2", "p", 400)
	assert.NoErr(t, s.UpsertJob(e1))
	assert.NoErr(t, s.UpsertJob(e2))
}

// TestListQueryCallerFilter asserts ListQuery.Caller filters by caller_id.
func TestListQueryCallerFilter(t *testing.T) {
	s := openTest(t)

	ja := sampleJob("ja", "p", 100)
	ja.CallerID = "alice"
	jb := sampleJob("jb", "p", 200)
	jb.CallerID = "bob"
	jc := sampleJob("jc", "p", 300)
	jc.CallerID = "alice"
	assert.NoErr(t, s.UpsertJob(ja))
	assert.NoErr(t, s.UpsertJob(jb))
	assert.NoErr(t, s.UpsertJob(jc))

	alice, err := s.ListJobs(ListQuery{Caller: "alice"})
	assert.NoErr(t, err)
	assert.Len(t, alice, 2)
	for _, r := range alice {
		assert.Eq(t, "alice", r.CallerID)
	}

	bob, err := s.ListJobs(ListQuery{Caller: "bob"})
	assert.NoErr(t, err)
	assert.Len(t, bob, 1)
	assert.Eq(t, "jb", bob[0].ID)

	none, err := s.ListJobs(ListQuery{Caller: "carol"})
	assert.NoErr(t, err)
	assert.Len(t, none, 0)
}
