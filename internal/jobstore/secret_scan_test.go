package jobstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecretScanFindsAcrossJobs(t *testing.T) {
	s := openTest(t)
	const secret = "scan-secret-42"
	cmd := sampleJob("scan-command", "proj", 100)
	cmd.Status, cmd.EndedAt, cmd.UpdatedAt = "done", 200, 200
	cmd.RequestJSON = `{"title":"command job","cmd":["echo","` + secret + `"]}`
	if err := s.UpsertJob(cmd); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	file := sampleJob("scan-output", "proj", 101)
	file.Status, file.EndedAt, file.UpdatedAt, file.ResultDir = "done", 201, 201, dir
	if err := s.UpsertJob(file); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stdout.log"), []byte("output "+secret), 0o600); err != nil {
		t.Fatal(err)
	}
	comment := sampleJob("scan-comment", "proj", 102)
	comment.Status, comment.EndedAt, comment.UpdatedAt = "done", 202, 202
	if err := s.UpsertJob(comment); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertComment(Comment{ID: "scan-comment-body", Scope: CommentScopeJob, ScopeID: comment.ID, Author: "alice", AuthorKind: CommentAuthorUser, Body: "note " + secret, CreatedAt: 202}); err != nil {
		t.Fatal(err)
	}

	report, err := s.ScanSecrets([]string{secret}, nil, SecretScanFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Jobs) != 3 {
		t.Fatalf("jobs = %+v, want three hits", report.Jobs)
	}
	for _, hit := range report.Jobs {
		if hit.JobID == "" || len(hit.Locations) == 0 {
			t.Fatalf("incomplete hit: %+v", hit)
		}
	}
}

func TestSecretScanNeverEchoes(t *testing.T) {
	s := openTest(t)
	const secret = "scan-never-echo-xyz"
	job := sampleJob("scan-no-echo", "proj", 100)
	job.Status, job.EndedAt, job.UpdatedAt = "done", 200, 200
	job.RequestJSON = `{"title":"` + secret + `"}`
	if err := s.UpsertJob(job); err != nil {
		t.Fatal(err)
	}
	report, err := s.ScanSecrets([]string{secret}, nil, SecretScanFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(report.String(), secret) {
		t.Fatalf("scan report echoed literal: %q", report.String())
	}
	if len(report.Jobs) != 1 || report.Jobs[0].Title == secret {
		t.Fatalf("masked title = %+v", report.Jobs)
	}
}

func TestSecretScanOwnerScope(t *testing.T) {
	s := openTest(t)
	const secret = "scan-owner-secret"
	for _, item := range []struct {
		id, owner string
	}{{"scan-alice", "alice"}, {"scan-bob", "bob"}} {
		job := sampleJob(item.id, "proj", 100)
		job.Status, job.EndedAt, job.UpdatedAt, job.CallerID = "done", 200, 200, item.owner
		job.RequestJSON = `{"prompt":"` + secret + `"}`
		if err := s.UpsertJob(job); err != nil {
			t.Fatal(err)
		}
	}
	report, err := s.ScanSecrets([]string{secret}, nil, SecretScanFilter{OwnerID: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Jobs) != 1 || report.Jobs[0].JobID != "scan-alice" {
		t.Fatalf("owner-scoped jobs = %+v", report.Jobs)
	}
}

func TestSecretScanRedactAll(t *testing.T) {
	s := openTest(t)
	const secret = "batch-secret-991"
	cmd := sampleJob("batch-command", "proj", 100)
	cmd.Status, cmd.EndedAt, cmd.UpdatedAt = "done", 200, 200
	cmd.RequestJSON = `{"cmd":["echo","` + secret + `"]}`
	if err := s.UpsertJob(cmd); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	file := sampleJob("batch-output", "proj", 101)
	file.Status, file.EndedAt, file.UpdatedAt, file.ResultDir = "done", 201, 201, dir
	if err := s.UpsertJob(file); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stdout.log"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	comment := sampleJob("batch-comment", "proj", 102)
	comment.Status, comment.EndedAt, comment.UpdatedAt = "done", 202, 202
	if err := s.UpsertJob(comment); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertComment(Comment{ID: "batch-comment-body", Scope: CommentScopeJob, ScopeID: comment.ID, Author: "alice", AuthorKind: CommentAuthorUser, Body: secret, CreatedAt: 202}); err != nil {
		t.Fatal(err)
	}
	report, err := s.RedactSecrets([]string{secret}, nil, SecretScanFilter{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Redacted) != 3 || !report.WALPurged {
		t.Fatalf("batch report = %+v", report)
	}
	for _, id := range []string{cmd.ID, file.ID, comment.ID} {
		events, err := s.ListJobEvents(id, 0)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, event := range events {
			if event.Type == "job.redacted" {
				found = true
			}
		}
		if !found {
			t.Fatalf("job %s missing job.redacted", id)
		}
	}
	left, err := s.ScanSecrets([]string{secret}, nil, SecretScanFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(left.Jobs) != 0 || len(left.Running) != 0 {
		t.Fatalf("secret survived batch: %+v", left)
	}
}
