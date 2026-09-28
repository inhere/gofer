package jobstore

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestResumeAgentBackfill(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`CREATE TABLE jobs (
  id TEXT PRIMARY KEY, project_key TEXT NOT NULL, agent TEXT NOT NULL,
  runner TEXT NOT NULL, worker_id TEXT, status TEXT NOT NULL,
  exit_code INTEGER NOT NULL DEFAULT 0, cwd TEXT, result_dir TEXT NOT NULL,
  request_json TEXT, error TEXT, started_at INTEGER NOT NULL,
  ended_at INTEGER, updated_at INTEGER NOT NULL, resumed_from TEXT
)`)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ id, agent, from string }{
		{"source", "codex", ""},
		{"carrier-1", "exec", "source"},
		{"carrier-2", "exec", "carrier-1"},
		{"missing", "exec", "absent"},
		{"plain", "exec", ""},
	} {
		_, err = raw.Exec(`INSERT INTO jobs
  (id, project_key, agent, runner, status, result_dir, started_at, updated_at, resumed_from)
  VALUES (?, 'self', ?, 'local', 'done', '.', 1, 1, ?)`, row.id, row.agent, row.from)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	check := func(id, want string) {
		t.Helper()
		var got string
		if err := store.db.QueryRow(`SELECT COALESCE(resume_agent,'') FROM jobs WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s resume_agent=%q, want %q", id, got, want)
		}
	}
	check("carrier-1", "codex")
	check("carrier-2", "codex")
	check("missing", "")
	check("plain", "")

	// A new legacy-shaped row inserted after migration must not trigger a fresh
	// historical sweep on the next Open.
	_, err = store.db.Exec(`INSERT INTO jobs
  (id, project_key, agent, runner, status, result_dir, started_at, updated_at, resumed_from)
  VALUES ('late', 'self', 'exec', 'local', 'done', '.', 2, 2, 'source')`)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	check("late", "")
}
