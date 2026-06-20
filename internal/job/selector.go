package job

import (
	"sort"
	"time"
)

// WorkerCandidate is the neutral, point-in-time snapshot selectWorker scores. It
// is populated by the commands layer from the hub registry (see
// commands.hubWorkerSelector) so the job package never imports wshub.
type WorkerCandidate struct {
	WorkerID string
	Labels   []string
	InFlight int
	// HeartbeatAge is the time since the worker's most recent inbound frame
	// (smaller = fresher). A candidate older than workerStaleAfter is excluded.
	HeartbeatAge time.Duration
}

// WorkerSelector exposes the currently-connected worker candidates for
// label-based auto-selection (D3). The commands layer injects a hub-backed
// implementation; Submit only consults it on the worker+labels path, so a nil
// selector is safe for every other runner.
type WorkerSelector interface {
	Candidates() []WorkerCandidate
}

// workerStaleAfter is the heartbeat freshness threshold: a candidate whose last
// inbound frame is older than this is treated as offline for selection (aligns
// with the C6 observability staleness口径).
const workerStaleAfter = 30 * time.Second

// selectWorker picks one worker that advertises ALL required labels and is fresh
// (HeartbeatAge <= workerStaleAfter), preferring the least loaded then the most
// recently seen (sort in_flight↑ → heartbeat_age↑, D3 load-aware). It returns ""
// when no candidate qualifies (the caller maps that to ErrNoEligibleWorker).
func selectWorker(cands []WorkerCandidate, required []string) string {
	ok := make([]WorkerCandidate, 0, len(cands))
	for _, w := range cands {
		if w.HeartbeatAge > workerStaleAfter {
			continue
		}
		if !hasAllLabels(w.Labels, required) {
			continue
		}
		ok = append(ok, w)
	}
	if len(ok) == 0 {
		return ""
	}
	sort.Slice(ok, func(i, j int) bool {
		if ok[i].InFlight != ok[j].InFlight {
			return ok[i].InFlight < ok[j].InFlight
		}
		return ok[i].HeartbeatAge < ok[j].HeartbeatAge
	})
	return ok[0].WorkerID
}

// hasAllLabels reports whether have contains every label in required (AND
// semantics). An empty required set matches any worker.
func hasAllLabels(have, required []string) bool {
	set := make(map[string]struct{}, len(have))
	for _, l := range have {
		set[l] = struct{}{}
	}
	for _, r := range required {
		if _, ok := set[r]; !ok {
			return false
		}
	}
	return true
}
