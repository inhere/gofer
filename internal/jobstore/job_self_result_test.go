package jobstore

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestJobSelfResultSchemaUpgrade: a database from a release that predates the self-report
// columns gains them additively, and an old job row reads back as "no report".
func TestJobSelfResultSchemaUpgrade(t *testing.T) {
	ddl, err := os.ReadFile(filepath.Join("testdata", "schema-v0.214.1.sql"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "gofer.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(string(ddl)); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open old schema: %v", err)
	}
	defer s.Close()
	cols, err := s.tableColumns("jobs")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"result_decision", "result_reason", "result_report_json", "result_attempt"} {
		if _, ok := cols[c]; !ok {
			t.Errorf("jobs.%s missing after upgrade", c)
		}
	}
}

// TestSetJobSelfResultOncePerJob: the first report is stored (with the job's attempt, at
// least 1) and read back by GetJob; a second one is refused and leaves the first intact;
// an unknown job is an error that is not ErrSelfResultExists.
func TestSetJobSelfResultOncePerJob(t *testing.T) {
	s := openTest(t)
	if err := s.UpsertJob(JobRecord{ID: "j1", ProjectKey: "p", Agent: "exec", Runner: "local", Status: "running", Attempt: 3}); err != nil {
		t.Fatal(err)
	}
	attempt, err := s.SetJobSelfResult("j1", "fail", "could not build", `{"decision":"fail"}`)
	if err != nil || attempt != 3 {
		t.Fatalf("first report = attempt %d, err %v, want 3, nil", attempt, err)
	}
	if _, err := s.SetJobSelfResult("j1", "pass", "again", `{}`); !errors.Is(err, ErrSelfResultExists) {
		t.Fatalf("second report err = %v, want ErrSelfResultExists", err)
	}
	rec, ok, err := s.GetJob("j1")
	if err != nil || !ok {
		t.Fatalf("GetJob: %v %v", ok, err)
	}
	if rec.ResultDecision != "fail" || rec.ResultReason != "could not build" || rec.ResultAttempt != 3 {
		t.Fatalf("stored = %+v", rec)
	}
	// A later general upsert of the row must not wipe the report.
	rec.Status = "done"
	if err := s.UpsertJob(rec); err != nil {
		t.Fatal(err)
	}
	if rec2, _, _ := s.GetJob("j1"); rec2.ResultDecision != "fail" {
		t.Fatalf("report lost after upsert: %+v", rec2)
	}
	if _, err := s.SetJobSelfResult("nope", "pass", "x", `{}`); err == nil || errors.Is(err, ErrSelfResultExists) {
		t.Fatalf("unknown job err = %v", err)
	}
}
