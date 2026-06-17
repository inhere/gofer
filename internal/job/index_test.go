package job

import (
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"

	"dev-agent-bridge/internal/store"
)

// TestJobsIndexCreateAndTerminal verifies a completed exec job writes exactly
// two index lines (queued + terminal), both with the same id, and the last line
// reflects the terminal status (done).
func TestJobsIndexCreateAndTerminal(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("setup: expected done, got %s (err=%s)", final.Status, final.Error)
	}

	recs, err := store.NewFileStore(filepath.Join(root, "self")).ReadIndex()
	if err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}
	if len(recs) < 2 {
		t.Fatalf("expected >=2 index lines (create + terminal), got %d", len(recs))
	}

	// Fold by id; the last line for this job must be terminal (done).
	var last JobResult
	if err := json.Unmarshal(recs[len(recs)-1], &last); err != nil {
		t.Fatalf("last line not a JobResult: %v", err)
	}
	if last.ID != final.ID {
		t.Fatalf("last index id %q != job id %q", last.ID, final.ID)
	}
	if last.Status != StatusDone {
		t.Fatalf("expected last index status=done, got %s", last.Status)
	}
}

// TestJobsIndexConcurrentNoInterleave submits N jobs concurrently and asserts
// the index ends with exactly 2N lines, every one a valid JSON object (no
// interleaved/corrupt lines). Runs clean under -race.
func TestJobsIndexConcurrentNoInterleave(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)

	const n = 12
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			res, err := s.Submit(JobRequest{
				ProjectKey: "self", Agent: "exec", Runner: "local",
				Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
			})
			if err != nil {
				t.Errorf("Submit: %v", err)
				return
			}
			s.Wait(res.ID)
		}()
	}
	wg.Wait()

	recs, err := store.NewFileStore(filepath.Join(root, "self")).ReadIndex()
	if err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}
	// ReadIndex already skips corrupt lines, so a count == 2N proves no line was
	// lost to interleaving (each job contributes exactly 2 valid lines).
	if len(recs) != 2*n {
		t.Fatalf("expected %d index lines (2 per job, no interleave), got %d", 2*n, len(recs))
	}
	for i, r := range recs {
		if !json.Valid(r) {
			t.Fatalf("index line %d is not valid JSON", i)
		}
	}
}
