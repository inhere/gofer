package job

import (
	"sort"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
)

// runnerCaps is the resolved capability + policy view of the runner a job would
// execute on (capabilitiesFor). Projects/Agents are the runnable key sets; WorkerID,
// PolicyPending and PolicyRev are meaningful only on a worker runner (a local runner
// leaves them zero) and feed the policy_pending admission message (T4-E) — they never
// add a rejection.
type runnerCaps struct {
	Projects      []string
	Agents        []string
	WorkerID      string
	PolicyPending bool
	PolicyRev     int64
}

// capabilitiesFor returns the capability view of the runner a job would execute
// on: which project keys and agent keys are actually runnable THERE (config
// federation P2). It is the single source P3's admission checks read, so a job
// is validated against the executing side's config rather than the host's.
//
//   - local (any non-worker runner — the job runs in THIS process): the host's own
//     config is the authority.
//   - worker runner: the worker's register-time report, read through the injected
//     WorkerSelector (the job package never imports wshub). online=false means the
//     worker is offline / unregistered / unresolvable, i.e. there is no capability
//     view to validate against.
//
// peer-http runners never reach here: a peer resolves the job with ITS OWN config
// and is deliberately out of scope for federation validation, so the caller (P3)
// keeps peers on the pre-federation path.
func (s *Service) capabilitiesFor(cfg *config.Config, runner, explicitWorkerID string) (runnerCaps, bool) {
	if isWorkerRunner(cfg, runner) {
		wid := explicitWorkerID
		if wid == "" {
			wid = cfg.Runners[runner].WorkerID
		}
		if wid == "" || s.workers == nil {
			return runnerCaps{}, false
		}
		cand, ok := s.workers.Candidate(wid)
		if !ok {
			return runnerCaps{}, false
		}
		return runnerCaps{
			Projects:      cand.Projects,
			Agents:        cand.Agents,
			WorkerID:      wid,
			PolicyPending: cand.PolicyPending,
			PolicyRev:     cand.PolicyRev,
		}, true
	}
	return runnerCaps{Projects: localProjectKeys(cfg), Agents: localAgentKeys(cfg)}, true
}

// localProjectKeys returns the project keys this process can run, sorted.
func localProjectKeys(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	out := make([]string, 0, len(cfg.Projects))
	for k := range cfg.Projects {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// localAgentKeys returns the agent keys this process can ACTUALLY run: the ones
// declared in config PLUS the built-in exec agent, which agent.ResolveAgent makes
// available even when undeclared. Reading the raw config map alone would under-report
// exec and make P3 reject a legitimate exec job on the local runner — the same fix
// the worker applies to its own report (commands.resolvedAgentKeys, P1). Sorted for
// a stable result (Go map iteration is not).
func localAgentKeys(cfg *config.Config) []string {
	seen := map[string]bool{agent.ExecAgentKey: true}
	if cfg != nil {
		for k := range cfg.Agents {
			seen[k] = true
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
