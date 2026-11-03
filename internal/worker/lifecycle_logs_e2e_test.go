package worker_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/worker"
)

// lockedLogBuf collects JSON log records from several goroutines at once (the hub
// side and the worker side both log into the default logger).
type lockedLogBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedLogBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedLogBuf) rows(event string) []map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(l.b.Bytes()), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var row map[string]any
		if json.Unmarshal(line, &row) == nil && row["event"] == event {
			out = append(out, row)
		}
	}
	return out
}

// TestE2EJobLifecycleLogsOnBothSides runs one runner=worker job end to end and checks
// that BOTH machines logged it: the hub-side job service emits job.submitted with the
// worker runner and the worker-side one re-submits it locally (runner=local), and each
// emits job.terminal. That pairing is what lets an operator follow a job across logs.
func TestE2EJobLifecycleLogsOnBothSides(t *testing.T) {
	hub := buildHubSide(t)
	logs := &lockedLogBuf{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(old)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cl := buildWorkerSide(t, hub.ts.URL)
	clientErr := worker.StartClient(t, ctx, cl)
	waitWorkerOnline(t, hub.hub)

	created := createJob(t, hub.ts, job.JobRequest{
		ProjectKey: "alpha", Agent: "exec", Runner: "remote-w1", WorkerID: e2eWorkerID,
		Cmd: []string{"echo", "hi"}, Cwd: ".", TimeoutSec: 30,
	})
	final, ok := hub.jobs.Wait(created.ID)
	if !ok || final.Status != job.StatusDone {
		t.Fatalf("hub job: ok=%v status=%s err=%s", ok, final.Status, final.Error)
	}

	// The hub side accepted a job dispatched to its worker.
	foundHost := false
	for _, row := range logs.rows("job.submitted") {
		if row["runner"] == "remote-w1" && row["project"] == "alpha" {
			foundHost = true
		}
	}
	if !foundHost {
		t.Fatalf("no hub-side job.submitted with runner=remote-w1: %v", logs.rows("job.submitted"))
	}
	// The worker side re-submits the SAME work locally and logs it there.
	foundWorker := false
	for _, row := range logs.rows("job.submitted") {
		if row["runner"] == "local" && row["project"] == "alpha" {
			foundWorker = true
		}
	}
	if !foundWorker {
		t.Fatalf("no worker-side job.submitted with runner=local: %v", logs.rows("job.submitted"))
	}

	// Both ends report how it ended.
	terminals := logs.rows("job.terminal")
	done := 0
	for _, row := range terminals {
		if row["status"] == job.StatusDone {
			done++
		}
	}
	if done < 2 {
		t.Fatalf("job.terminal rows=%d, want one per side: %v", done, terminals)
	}

	cancel()
	select {
	case <-clientErr:
	case <-time.After(3 * time.Second):
		t.Log("worker client did not exit promptly after cancel (non-fatal)")
	}
}
