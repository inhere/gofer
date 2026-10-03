package jobstore

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJobRedactLiteralAllLocations(t *testing.T) {
	s := openTest(t)
	dir := t.TempDir()
	const secret = "fake-secret-redact-123456"
	job := sampleJob("redact-all", "proj", 100)
	job.Status, job.EndedAt, job.UpdatedAt = "done", 200, 200
	job.ResultDir = dir
	job.RequestJSON = `{"prompt":"` + secret + `"}`
	job.RenderedCommand = "printf " + secret
	job.ResultJSON = `{"summary":"` + secret + `"}`
	job.Error = "error " + secret
	if err := s.UpsertJob(job); err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}
	if _, err := s.InsertJobEvent(JobEvent{JobID: job.ID, Type: "job.terminal", Detail: `{"line":"` + secret + `"}`, At: 200}); err != nil {
		t.Fatalf("InsertJobEvent: %v", err)
	}
	if err := s.InsertComment(Comment{ID: "c-redact", Scope: CommentScopeJob, ScopeID: job.ID, Author: "alice", AuthorKind: CommentAuthorUser, Body: "comment " + secret, CreatedAt: 200}); err != nil {
		t.Fatalf("InsertComment: %v", err)
	}
	for _, name := range []string{"stdout.log", "stderr.log", "turn-1.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("file "+secret), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "binary.bin"), []byte{0, 1, 2, 3}, 0o600); err != nil {
		t.Fatalf("write binary: %v", err)
	}

	report, err := s.RedactJob(job.ID, []string{secret}, nil)
	if err != nil {
		t.Fatalf("RedactJob: %v", err)
	}
	if report.DBMatches == 0 || report.FileMatches == 0 {
		t.Fatalf("report = %+v, want DB and file matches", report)
	}
	events, err := s.ListJobEvents(job.ID, 0)
	if err != nil {
		t.Fatalf("ListJobEvents: %v", err)
	}
	var audit struct {
		FileMatches int `json:"file_matches"`
	}
	for _, event := range events {
		if event.Type != "job.redacted" {
			continue
		}
		if err := json.Unmarshal([]byte(event.Detail), &audit); err != nil {
			t.Fatalf("decode redact audit: %v", err)
		}
		if audit.FileMatches != report.FileMatches {
			t.Fatalf("audit file_matches=%d, report file_matches=%d", audit.FileMatches, report.FileMatches)
		}
		goto auditChecked
	}
	t.Fatal("job.redacted audit event not found")

auditChecked:
	if len(report.SkippedFiles) != 1 || report.SkippedFiles[0] != "binary.bin" {
		t.Fatalf("skipped files = %v, want binary.bin", report.SkippedFiles)
	}
	assertNoSecretInJobDB(t, s.db, job.ID, secret)
	for _, name := range []string{"stdout.log", "stderr.log", "turn-1.txt"} {
		body, readErr := os.ReadFile(filepath.Join(dir, name))
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		if strings.Contains(string(body), secret) {
			t.Fatalf("secret survived in %s", name)
		}
	}
}

func TestJobRedactRejectsRunningJob(t *testing.T) {
	s := openTest(t)
	job := sampleJob("redact-running", "proj", 100)
	job.Status = "running"
	if err := s.UpsertJob(job); err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}
	if _, err := s.RedactJob(job.ID, []string{"secret"}, nil); err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("RedactJob running error = %v, want terminal refusal", err)
	}
}

func TestJobRedactNeverEchoes(t *testing.T) {
	s := openTest(t)
	job := sampleJob("redact-no-echo", "proj", 100)
	job.Status, job.EndedAt = "done", 200
	if err := s.UpsertJob(job); err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}
	const secret = "secret-not-echoed"
	report, err := s.RedactJob(job.ID, []string{secret}, nil)
	if err != nil {
		t.Fatalf("RedactJob: %v", err)
	}
	if strings.Contains(strings.ToLower(report.String()), secret) {
		t.Fatalf("report echoed literal: %q", report.String())
	}
}

func assertNoSecretInJobDB(t *testing.T, db *sql.DB, jobID, secret string) {
	t.Helper()
	tables, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer tables.Close()
	for tables.Next() {
		var table string
		if err := tables.Scan(&table); err != nil {
			t.Fatalf("scan table: %v", err)
		}
		rows, queryErr := db.Query(`SELECT * FROM "` + strings.ReplaceAll(table, `"`, `""`) + `"`)
		if queryErr != nil {
			continue
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			values := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range values {
				ptrs[i] = &values[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				rows.Close()
				t.Fatalf("scan %s: %v", table, err)
			}
			for _, value := range values {
				if text, ok := value.(string); ok && strings.Contains(text, secret) {
					rows.Close()
					t.Fatalf("secret survived in table %s", table)
				}
			}
		}
		rows.Close()
	}
}
