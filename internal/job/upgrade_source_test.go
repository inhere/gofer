package job

import (
	"path/filepath"
	"testing"

	"github.com/inhere/gofer/internal/daemon"
	"github.com/inhere/gofer/internal/jobstore"
)

func TestValidateUpgradeSourceRequiresLiveDirectExecIdentity(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	self, err := daemon.CurrentProcessIdentity()
	if err != nil {
		t.Fatal(err)
	}
	const id = "upgrade-source-test"
	entry := &jobEntry{result: JobResult{ID: id, Agent: "exec", Runner: builtinLocalRunner, Status: StatusRunning}, process: self}
	s.mu.Lock()
	s.jobs[id] = entry
	s.mu.Unlock()
	record := jobstore.JobRecord{ID: id, ProjectKey: "self", Agent: "exec", Runner: builtinLocalRunner,
		Status: StatusRunning, Cwd: root, ResultDir: filepath.Join(root, id), RequestJSON: `{}`}
	if err := s.meta.UpsertJob(record); err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateUpgradeSource(id, self.PID); err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateUpgradeSource(id, self.PID+1); err == nil {
		t.Fatal("wrong initiator PID accepted")
	}
	entry.mu.Lock()
	entry.process.StartID = "reused-pid"
	entry.mu.Unlock()
	if err := s.ValidateUpgradeSource(id, self.PID); err == nil {
		t.Fatal("changed process birth accepted")
	}
	entry.mu.Lock()
	entry.process = self
	entry.mu.Unlock()
	record.Runner = "remote"
	if err := s.meta.UpsertJob(record); err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateUpgradeSource(id, self.PID); err == nil {
		t.Fatal("non-local persisted row accepted")
	}
}
