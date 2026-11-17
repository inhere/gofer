package worker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/wsproto"
)

// R8: the dispatch's keep_dir_lock reaches this machine's own Submit (the lock of a
// worker-hosted session is taken here), and a dispatch without it leaves the default.
func TestDispatchKeepDirLockReachesLocalSubmit(t *testing.T) {
	for _, keep := range []bool{true, false} {
		jobs := &stubJobs{
			submitID: "local-1", getResult: job.JobResult{Status: job.StatusDone}, getOK: true,
			waitResult: job.JobResult{Status: job.StatusDone}, waitOK: true,
		}
		cl, _, url := dialLiveClient(t, jobs)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		cl.handleDispatch(ctx, url, wsproto.Dispatch{JobID: "d1", KeepDirLock: keep})
		cancel()
		jobs.mu.Lock()
		got := jobs.gotKeepDirLock
		jobs.mu.Unlock()
		if got != keep {
			t.Fatalf("dispatch keep_dir_lock=%v reached Submit as %v", keep, got)
		}
	}
}

// The wire key is omitted when false (an old worker sees a byte-identical frame) and
// named keep_dir_lock when set.
func TestDispatchKeepDirLockWireKey(t *testing.T) {
	off, _ := json.Marshal(wsproto.Dispatch{JobID: "d1"})
	if strings.Contains(string(off), "keep_dir_lock") {
		t.Fatalf("false must be omitted: %s", off)
	}
	on, _ := json.Marshal(wsproto.Dispatch{JobID: "d1", KeepDirLock: true})
	if !strings.Contains(string(on), `"keep_dir_lock":true`) {
		t.Fatalf("set value missing: %s", on)
	}
}
