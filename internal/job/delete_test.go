package job

import (
	"path/filepath"
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
)

// A job is durable as terminal a moment before finish evicts it; a delete landing in
// that window must not leave Get answering from the in-memory entry.
func TestDeleteJobEvictsFinishedEntry(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	const id = "delete-window"
	rec := jobstore.JobRecord{ID: id, ProjectKey: "p", Agent: "exec", Runner: builtinLocalRunner, Status: StatusDone,
		Cwd: ".", ResultDir: filepath.Join(root, id), RequestJSON: `{}`, StartedAt: 100, EndedAt: 200, UpdatedAt: 200}
	if err := s.meta.UpsertJob(rec); err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}
	entry := &jobEntry{result: JobResult{ID: id, Agent: "exec", Runner: builtinLocalRunner, Status: StatusDone}}
	s.mu.Lock()
	s.jobs[id] = entry
	s.mu.Unlock()

	if err := s.DeleteJob(id, "alice"); err != nil {
		t.Fatalf("DeleteJob: %v", err)
	}
	if _, ok := s.Get(id); ok {
		t.Fatal("deleted job still visible through the in-memory entry")
	}
}
