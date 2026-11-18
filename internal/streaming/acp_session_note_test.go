package streaming

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// sseRecorder collects the SSE bytes a stream writes and satisfies http.Flusher, so a
// test can follow a live stream without an HTTP connection.
type sseRecorder struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (r *sseRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.Write(p)
}

func (r *sseRecorder) Flush() {}

func (r *sseRecorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.String()
}

// TestStreamACPLiveForwardsSessionNote: a session note appended to the transcript while
// a live stream is following it reaches that stream (not only the next replay), so the
// workspace can show the restart notice without a page reload.
func TestStreamACPLiveForwardsSessionNote(t *testing.T) {
	resultDir := t.TempDir()
	artifact := filepath.Join(resultDir, "artifacts", acpArtifactFile)
	if err := os.MkdirAll(filepath.Dir(artifact), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact, []byte(`{"t":"prompt","text":"hi"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := &sseRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	// jobs is nil: the test only exercises the transcript follow, not job status lookups.
	res := job.JobResult{ID: "job-note", Status: job.StatusRunning, ResultDir: resultDir}
	go func() {
		defer close(done)
		StreamACP(ctx, rec, rec, nil, res.ID, res, true, ACPStreamOpts{})
	}()

	wait.Until(t, 10*time.Second, "the replayed prompt", func() bool {
		return strings.Contains(rec.String(), `"kind":"prompt"`)
	})

	f, err := os.OpenFile(artifact, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"t":"session_note","code":"turn_interrupted_worker_restart"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	wait.Until(t, 10*time.Second, "the session note on the live stream", func() bool {
		return strings.Contains(rec.String(), `"kind":"session_note"`)
	})
	if !strings.Contains(rec.String(), `"code":"turn_interrupted_worker_restart"`) {
		t.Fatalf("session note lost its code: %s", rec.String())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(wait.Timeout(t, 10*time.Second)):
		t.Fatal("stream did not stop after cancel")
	}
}
