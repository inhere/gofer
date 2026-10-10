package job

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/testcmd"
	"github.com/inhere/gofer/internal/testutil/wait"
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
	done := make(chan struct{})
	close(done) // its execute goroutine has returned; only the eviction is missing
	entry := &jobEntry{result: JobResult{ID: id, Agent: "exec", Runner: builtinLocalRunner, Status: StatusDone}, done: done}
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

// holdingLinker parks finish inside its window: LinkIssueFinished runs after the
// in-memory status flipped to terminal and before the terminal row is persisted.
type holdingLinker struct {
	entered chan string
	release chan struct{}
}

func (l *holdingLinker) LinkIssueStarted(JobResult) {}

func (l *holdingLinker) LinkIssueFinished(snap JobResult) {
	l.entered <- snap.ID
	<-l.release
}

// startFinishingJob submits a quick job and returns once its finish is held inside
// the window (GET already reads it terminal, the row does not yet).
func startFinishingJob(t *testing.T) (*Service, string, func()) {
	t.Helper()
	s := newTestService(t, t.TempDir())
	l := &holdingLinker{entered: make(chan string, 1), release: make(chan struct{})}
	s.SetIssueLinker(l)
	var once bool
	release := func() {
		if !once {
			once = true
			close(l.release)
		}
	}
	t.Cleanup(release) // before the drain (cleanups are LIFO)
	res, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{testcmd.Path(t), "exit", "0"}, Cwd: ".", TimeoutSec: 30})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	select {
	case <-l.entered:
	case <-time.After(wait.Timeout(t, 30*time.Second)):
		t.Fatal("job never reached finish")
	}
	if got, ok := s.Get(res.ID); !ok || !IsTerminal(got.Status) {
		t.Fatalf("GET inside the finish window = %+v (ok=%v), want terminal", got, ok)
	}
	return s, res.ID, release
}

// C1 (gofer-r7am): GET reads a job terminal the moment finish flips it in memory,
// while the row is still running. A DELETE in that window was refused "not terminal";
// it now waits for the finish and succeeds.
func TestDeleteJobWaitsOutFinishWindow(t *testing.T) {
	s, id, release := startFinishingJob(t)
	errc := make(chan error, 1)
	go func() { errc <- s.DeleteJob(id, "alice") }()
	select {
	case err := <-errc:
		t.Fatalf("DeleteJob returned while the job was still finishing: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	release()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("DeleteJob after the finish: %v", err)
		}
	case <-time.After(wait.Timeout(t, 10*time.Second)):
		t.Fatal("DeleteJob did not return after the finish completed")
	}
	if _, ok := s.Get(id); ok {
		t.Fatal("job still readable after DeleteJob")
	}
}

// C1: a finish outlasting the bound is refused with ErrJobFinishing, worded with the
// jobstore delete prefix the HTTP layer answers with 409.
func TestDeleteJobFinishingTimesOut(t *testing.T) {
	old := deleteFinishWait
	deleteFinishWait = 50 * time.Millisecond
	t.Cleanup(func() { deleteFinishWait = old })
	s, id, release := startFinishingJob(t)
	err := s.DeleteJob(id, "alice")
	if !errors.Is(err, ErrJobFinishing) || !strings.HasPrefix(err.Error(), "jobstore: delete: job ") {
		t.Fatalf("DeleteJob inside a long finish = %v, want ErrJobFinishing with the jobstore prefix", err)
	}
	release()
	if _, ok := s.Wait(id); !ok {
		t.Fatal("job lost after a refused delete")
	}
	if err := s.DeleteJob(id, "alice"); err != nil {
		t.Fatalf("DeleteJob once finished: %v", err)
	}
}
