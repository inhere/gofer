package serve

import (
	"log/slog"
	"sync"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/wshub"
)

// workerReplacedEventInterval is the per-worker rate limit of the worker.replaced
// event; every replacement is still logged.
const workerReplacedEventInterval = 10 * time.Minute

// replaceReporter turns the hub's replace callbacks into a server log line plus a
// rate-limited worker event. The limit table lives in memory: it only exists to
// keep two fighting processes from flooding the stream.
type replaceReporter struct {
	mu         sync.Mutex
	last       map[string]time.Time
	suppressed map[string]int
	now        func() time.Time
	record     func(scope, eventType, projectKey string, detail map[string]any)
}

func newReplaceReporter(record func(scope, eventType, projectKey string, detail map[string]any)) *replaceReporter {
	return &replaceReporter{last: map[string]time.Time{}, suppressed: map[string]int{}, now: time.Now, record: record}
}

// report handles one replacement.
func (r *replaceReporter) report(info wshub.ReplaceInfo) {
	slog.Warn("worker.replaced", "event", "worker.replaced", "component", "server", "worker_id", info.WorkerID,
		"old_instance", info.OldInstance, "new_instance", info.NewInstance,
		"old_remote", info.OldRemote, "new_remote", info.NewRemote)
	now := r.now()
	r.mu.Lock()
	if at, ok := r.last[info.WorkerID]; ok && now.Sub(at) < workerReplacedEventInterval {
		r.suppressed[info.WorkerID]++
		r.mu.Unlock()
		return
	}
	r.last[info.WorkerID] = now
	suppressed := r.suppressed[info.WorkerID]
	delete(r.suppressed, info.WorkerID)
	r.mu.Unlock()
	r.record(job.WorkerEventScope(info.WorkerID), job.EventWorkerReplaced, "", map[string]any{
		"worker_id": info.WorkerID, "old_instance": info.OldInstance, "new_instance": info.NewInstance,
		"old_remote": info.OldRemote, "new_remote": info.NewRemote, "suppressed": suppressed,
	})
}

// wireWorkerReplaced installs the hub's replace observer.
func wireWorkerReplaced(wh *wshub.Hub, jobs *job.Service) {
	if wh == nil || jobs == nil {
		return
	}
	wh.SetReplaceObserver(newReplaceReporter(jobs.RecordScopedEvent).report)
}
