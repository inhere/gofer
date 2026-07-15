package core

import (
	"sort"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/wsproto"
)

// workerRunnerType is the runner.Type value that dispatches a job to a ws-worker.
// The Build runner switch keys on the same literal (core.go); there is no shared
// const for it, so this one names the meaning locally.
const workerRunnerType = "worker"

// computePolicy projects the server config onto the per-worker Policy for workerID
// at config generation rev. It is a PURE function (no I/O, no mutation of cfg) so it
// is trivially testable and safe to call under any lock: the caller (corePolicySource)
// hands it one atomic (cfg, rev) snapshot.
//
// A project P is pushed to worker W iff some runner in P.AllowedRunners reaches W
// (see projectReachesWorker). Reachability is the ONLY routing input — the whitelist
// fields (AllowedAgents/…) are透传 guards the worker applies, never an intersection.
//
// Q8 (钉死 by 验收 12): an EMPTY AllowedRunners reaches NO worker — it is NOT a
// wildcard. The whole point of the reachability test is that "∃ a reachable runner"
// is vacuously false for an empty set, so such a project is pushed to nobody.
func computePolicy(cfg *config.Config, workerID string, rev int64) wsproto.Policy {
	// Non-nil Projects so an empty Policy serialises `projects:[]`, not null.
	pol := wsproto.Policy{Rev: rev, Projects: []wsproto.PolicyProject{}}
	if cfg == nil {
		return pol
	}
	// Deterministic output order (stable frame for tests / later fingerprinting).
	keys := make([]string, 0, len(cfg.Projects))
	for k := range cfg.Projects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		// The key becomes a directory name on the worker; skip any that fails the same
		// charset rule the job layer enforces (never push an unsafe segment).
		if job.CheckProjectKey(key) != nil {
			continue
		}
		proj := cfg.Projects[key]
		if !projectReachesWorker(cfg, proj, workerID) {
			continue
		}
		pol.Projects = append(pol.Projects, projectToPolicy(key, proj))
	}
	return pol
}

// projectReachesWorker reports whether proj should be pushed to workerID:
//
//	P 推给 W ⟺ ∃ r ∈ P.AllowedRunners 使 W 经 r 可达
//	  cfg.Runners[r].Type=="worker" && WorkerID==W  → 可达 (pin 型, 精确)
//	  cfg.Runners[r].Type=="worker" && WorkerID==""  → 可达 (池型; server 算不出候选 → 保守全推)
//	  其余 (local / peer-http / 未定义的 runner key)  → 忽略
//	  P.AllowedRunners 为空 → ∃ 恒假 → 不推给任何 worker (Q8, 绝不当通配)
func projectReachesWorker(cfg *config.Config, proj config.ProjectConfig, workerID string) bool {
	for _, r := range proj.AllowedRunners {
		rc, ok := cfg.Runners[r]
		if !ok {
			continue // undefined runner key → ignore (cannot reach any worker)
		}
		if rc.Type != workerRunnerType {
			continue // local / peer-http → not a worker path → ignore
		}
		// A pinned worker runner reaches only its bound worker; a pool runner
		// (WorkerID unset) has no server-side candidate list, so we conservatively
		// treat it as reaching every worker.
		if rc.WorkerID == "" || rc.WorkerID == workerID {
			return true
		}
	}
	return false
}

// projectToPolicy maps a server ProjectConfig onto its wire PolicyProject. HostPath
// is the server's host_path verbatim (it IS the logical path; ContainerPath is never
//下发). MaxConcurrentJobs / CaptureDiff ride along unchanged (H2). AllowedAgents /
// InteractiveAllowedAgents are forced NON-nil so the wire form is `[]`, never null
// (MEDIUM-1: a nil slice marshals to null, which a downstream must not confuse with
// "no whitelist" — computePolicy guarantees non-nil here).
func projectToPolicy(key string, proj config.ProjectConfig) wsproto.PolicyProject {
	return wsproto.PolicyProject{
		Key:                      key,
		HostPath:                 proj.HostPath,
		AllowedAgents:            nonNilStrings(proj.AllowedAgents),
		InteractiveAllowedAgents: nonNilStrings(proj.InteractiveAllowedAgents),
		AllowExec:                proj.AllowExec,
		MaxConcurrentJobs:        proj.MaxConcurrentJobs,
		CaptureDiff:              proj.CaptureDiff,
	}
}

// nonNilStrings returns s unchanged when non-nil, otherwise a non-nil empty slice so
// the JSON wire form is `[]` rather than `null`.
func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// corePolicySource adapts Core to the hub's PolicySource seam (T3 wiring). It is the
// ONLY place a per-worker Policy is computed, keeping internal/wshub free of any
// config/computation dependency (verification 17).
type corePolicySource struct{ core *Core }

// PolicyFor computes the Policy for workerID from a SINGLE atomic config snapshot
// (Snapshot returns cfg and rev from the SAME generation), so the pushed Rev always
// matches the cfg it was computed from — never a torn (stale cfg, newer rev) pair
// that would leave a worker permanently stuck (T1-A). It always returns ok=true: an
// empty Policy (0 projects) is a legitimate "you have no projects at this rev" push
// (revoke semantics), which the hub still delivers.
func (s *corePolicySource) PolicyFor(workerID string) (wsproto.Policy, bool) {
	snap := s.core.Snapshot() // ONE load: cfg + rev from one generation (T1-A)
	return computePolicy(snap.Cfg, workerID, snap.Rev), true
}
