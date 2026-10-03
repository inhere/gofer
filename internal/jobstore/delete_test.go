package jobstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJobDeleteRemovesRecordKeepsAudit(t *testing.T) {
	s := openTest(t)
	dir := t.TempDir()
	job := sampleJob("delete-me", "proj", 100)
	job.Status, job.EndedAt, job.UpdatedAt = "done", 200, 200
	job.RequestJSON = `{"title":"private title"}`
	job.ResultDir = filepath.Join(dir, "result")
	if err := os.MkdirAll(job.ResultDir, 0o700); err != nil {
		t.Fatalf("mkdir result: %v", err)
	}
	if err := os.WriteFile(filepath.Join(job.ResultDir, "stdout.log"), []byte("output"), 0o600); err != nil {
		t.Fatalf("write result: %v", err)
	}
	if err := s.UpsertJob(job); err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}
	if _, err := s.InsertJobEvent(JobEvent{JobID: job.ID, Type: "job.terminal", Detail: `{"title":"private title"}`, At: 200}); err != nil {
		t.Fatalf("InsertJobEvent: %v", err)
	}
	if err := s.InsertComment(Comment{ID: "delete-comment", Scope: CommentScopeJob, ScopeID: job.ID, Author: "alice", AuthorKind: CommentAuthorUser, Body: "comment", CreatedAt: 200}); err != nil {
		t.Fatalf("InsertComment: %v", err)
	}

	if err := s.DeleteJob(job.ID, "alice"); err != nil {
		t.Fatalf("DeleteJob: %v", err)
	}
	if _, ok, err := s.GetJob(job.ID); err != nil || ok {
		t.Fatalf("deleted job still visible: ok=%v err=%v", ok, err)
	}
	if _, err := os.Stat(job.ResultDir); !os.IsNotExist(err) {
		t.Fatalf("result dir stat error=%v, want removed", err)
	}
	events, err := s.ListJobEventsDesc(job.ID, 0, 20)
	if err != nil {
		t.Fatalf("ListJobEventsDesc: %v", err)
	}
	if len(events) != 1 || events[0].Type != "job.deleted" || strings.Contains(events[0].Detail, "private title") {
		t.Fatalf("audit events = %+v, want one redacted job.deleted audit", events)
	}
}
