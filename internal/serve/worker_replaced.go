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

// workerDisplacedTTL is how long a displaced instance_id is remembered. A process
// that comes back within it while another one holds the worker_id is the
// two-processes signature; a restart or an upgrade handover never comes back.
const workerDisplacedTTL = 30 * time.Minute

// replaceReporter turns the hub's replace callbacks into a server log line plus a
// rate-limited worker event. A single replacement is a handover (restart, upgrade
// re-exec racing its predecessor's teardown) and is only logged; the event, which
// the worker page shows as a warning, is recorded only when a displaced instance
// connects again — two live processes evicting each other. The tables live in
// memory: they only exist to classify and to keep a fight from flooding the stream.
type replaceReporter struct {
	mu         sync.Mutex
	last       map[string]time.Time
	suppressed map[string]int
	displaced  map[string]map[string]time.Time // worker_id -> displaced instance_id -> when
	now        func() time.Time
	record     func(scope, eventType, projectKey string, detail map[string]any)
}

func newReplaceReporter(record func(scope, eventType, projectKey string, detail map[string]any)) *replaceReporter {
	return &replaceReporter{last: map[string]time.Time{}, suppressed: map[string]int{},
		displaced: map[string]map[string]time.Time{}, now: time.Now, record: record}
}

// report handles one replacement.
func (r *replaceReporter) report(info wshub.ReplaceInfo) {
	now := r.now()
	r.mu.Lock()
	conflict := r.noteDisplaced(info, now)
	if !conflict {
		r.mu.Unlock()
		slog.Info("worker.handover", "event", "worker.handover", "component", "server", "worker_id", info.WorkerID,
			"old_instance", info.OldInstance, "new_instance", info.NewInstance,
			"old_remote", info.OldRemote, "new_remote", info.NewRemote)
		return
	}
	slog.Warn("worker.replaced", "event", "worker.replaced", "component", "server", "worker_id", info.WorkerID,
		"old_instance", info.OldInstance, "new_instance", info.NewInstance,
		"old_remote", info.OldRemote, "new_remote", info.NewRemote)
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
		"old_remote": info.OldRemote, "new_remote": info.NewRemote, "suppressed": suppressed, "conflict": true,
	})
}

// noteDisplaced remembers info.OldInstance as displaced and reports whether
// info.NewInstance was itself displaced within workerDisplacedTTL. Caller holds r.mu.
func (r *replaceReporter) noteDisplaced(info wshub.ReplaceInfo, now time.Time) bool {
	seen := r.displaced[info.WorkerID]
	if seen == nil {
		seen = map[string]time.Time{}
		r.displaced[info.WorkerID] = seen
	}
	for inst, at := range seen {
		if now.Sub(at) >= workerDisplacedTTL {
			delete(seen, inst)
		}
	}
	_, conflict := seen[info.NewInstance]
	seen[info.OldInstance] = now
	return conflict
}

// wireWorkerReplaced installs the hub's replace observer.
func wireWorkerReplaced(wh *wshub.Hub, jobs *job.Service) {
	if wh == nil || jobs == nil {
		return
	}
	wh.SetReplaceObserver(newReplaceReporter(jobs.RecordScopedEvent).report)
}
