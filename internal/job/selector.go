package job

import (
	"slices"
	"sort"
	"time"
)

// WorkerCandidate is the neutral, point-in-time snapshot selectWorker scores. It
// is populated by the commands layer from the hub registry (see
// commands.hubWorkerSelector) so the job package never imports wshub.
type WorkerCandidate struct {
	WorkerID string
	Labels   []string
	// Projects / Agents are the capability keys the worker reported on register
	// (authoritative, P1). They carry the worker's own view of what it can run so
	// the host can validate/filter against it without importing wshub.
	Projects   []string
	Agents     []string
	InFlight   int
	PtyCapable bool
	// HeartbeatAge is the time since the worker's most recent inbound frame
	// (smaller = fresher). A candidate older than workerStaleAfter is excluded.
	HeartbeatAge time.Duration
	// PolicyPending is true when the worker negotiated server-side policy push (P3 T4)
	// and the hub pushed a policy rev the worker has not yet reported applied. It NEVER
	// adds a rejection: a pending worker is still running its previous, fully valid config
	// and dispatch keeps working, so admission uses it ONLY to swap in a clearer error
	// message when the requested project happens not to be in the reported caps yet.
	// PolicyRev/AppliedRev are the pushed/applied revs (diagnostics for the message).
	PolicyPending bool
	PolicyRev     int64
	AppliedRev    int64
}

// WorkerSelector exposes the currently-connected worker candidates for
// label-based auto-selection (D3) and exact worker lookup for admission-time
// capability checks. The commands layer injects a hub-backed implementation;
// Submit only consults it on worker-runner paths, so a nil selector is safe for
// every other runner.
type WorkerSelector interface {
	Candidates() []WorkerCandidate
	Candidate(workerID string) (WorkerCandidate, bool)
}

// workerStaleAfter is the heartbeat freshness threshold: a candidate whose last
// inbound frame is older than this is treated as offline for selection (aligns
// with the C6 observability staleness口径).
const workerStaleAfter = 30 * time.Second

// selectWorker picks one worker that advertises ALL required labels, CAN RUN the
// job (its reported capabilities include project + agent, federation P3/G2) and is
// fresh (HeartbeatAge <= workerStaleAfter), preferring the least loaded then the
// most recently seen (sort in_flight↑ → heartbeat_age↑, D3 load-aware). It returns
// "" when no candidate qualifies (the caller maps that to ErrNoCapableWorker).
//
// project / agent are the job's target capability keys; an empty value disables
// that filter (the caller has nothing to match on — e.g. a remote job that leaves
// the agent to the executor).
func selectWorker(cands []WorkerCandidate, required []string, interactive bool, project, agent string) string {
	ok := make([]WorkerCandidate, 0, len(cands))
	for _, w := range cands {
		if w.HeartbeatAge > workerStaleAfter {
			continue
		}
		if !hasAllLabels(w.Labels, required) {
			continue
		}
		if interactive && !w.PtyCapable {
			continue
		}
		// The worker reports what it can actually run (register-time capability set,
		// P1). A worker that does not carry the project / agent is not a candidate —
		// dispatching to it would only earn a remote rejection.
		if project != "" && !slices.Contains(w.Projects, project) {
			continue
		}
		if agent != "" && !slices.Contains(w.Agents, agent) {
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
