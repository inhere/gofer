package job

import (
	"fmt"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
)

// isPeerRunner reports whether name is a configured peer-http runner (a remote
// runner that forwards the job to a peer bridge) in the given config snapshot.
// Such runners resolve the agent/command on the peer, so the host skips local
// exec resolution.
func isPeerRunner(cfg *config.Config, name string) bool {
	rc, ok := cfg.Runners[name]
	return ok && rc.Type == "peer-http"
}

// isWorkerRunner reports whether name is a configured ws-worker runner (a remote
// runner that dispatches the job to a worker over the hub WebSocket). Like
// peer-http it resolves the agent/command on the remote side, so the host skips
// local exec resolution.
func isWorkerRunner(cfg *config.Config, name string) bool {
	rc, ok := cfg.Runners[name]
	return ok && rc.Type == "worker"
}

// isRemoteRunner reports whether name is any remote runner (peer-http or
// ws-worker); both share the host-side "skip local resolution + set Forward"
// path in Submit.
func isRemoteRunner(cfg *config.Config, name string) bool {
	return isPeerRunner(cfg, name) || isWorkerRunner(cfg, name)
}

// validate enforces the project/agent/runner/exec allowlists (plan §11) and
// returns the resolved project config.
//
// remote (a peer-http runner) relaxes two host-local checks: the exec-type
// security gate and the agent-must-be-known check. A remote job is resolved and
// executed on the peer with ITS config, so the host may legitimately not know
// the agent (it can be a peer-only agent) and must not impose its own exec gate.
// The agent allowlist (CheckAllowed) and the runner allowlist still apply on the
// host as the access-control boundary.
func (s *Service) validate(cfg *config.Config, req JobRequest, remote bool) (config.ProjectConfig, error) {
	proj, ok := cfg.Projects[req.ProjectKey]
	if !ok {
		return config.ProjectConfig{}, fmt.Errorf("%w: unknown project %q", ErrUnknownProject, req.ProjectKey)
	}

	// Agent must be in the project's allowed_agents (exec is not exempt).
	if err := agent.CheckAllowed(cfg, req.ProjectKey, req.Agent); err != nil {
		return config.ProjectConfig{}, fmt.Errorf("%w: %s", ErrInvalidRequest, err.Error())
	}

	if !remote {
		// exec security gate: the agent must be type exec AND the project must opt
		// in. Skipped for remote jobs — the peer enforces its own exec gate.
		ac, ok := agent.ResolveAgent(cfg, req.Agent)
		if !ok {
			return config.ProjectConfig{}, fmt.Errorf("%w: unknown agent %q", ErrInvalidRequest, req.Agent)
		}
		if ac.Type == agent.TypeExec && !proj.AllowExec {
			return config.ProjectConfig{}, fmt.Errorf("%w: exec agent %q not allowed: project %q has allow_exec=false", ErrInvalidRequest, req.Agent, req.ProjectKey)
		}
	}

	// Runner must be in allowed_runners ("local" is a built-in default).
	if req.Runner == "" {
		return config.ProjectConfig{}, fmt.Errorf("%w: runner is required", ErrInvalidRequest)
	}
	if err := checkRunnerAllowed(proj, req.Runner); err != nil {
		return config.ProjectConfig{}, fmt.Errorf("%w: %s", ErrInvalidRequest, err.Error())
	}

	// ws-worker runner: an EXPLICIT worker_id must be a known server.workers entry
	// (review #1: worker_id is part of the worker's caller identity; an unknown id
	// has no live binding/conn). An empty worker_id is allowed now — the worker is
	// resolved post-validate by selectTargetWorker (labels → auto-select, else the
	// runner's configured default, D4).
	if isWorkerRunner(cfg, req.Runner) && req.WorkerID != "" {
		if _, ok := cfg.Server.Workers[req.WorkerID]; !ok {
			return config.ProjectConfig{}, fmt.Errorf("%w: unknown worker_id %q", ErrInvalidRequest, req.WorkerID)
		}
	}
	return proj, nil
}

// selectTargetWorker resolves req.WorkerID for a worker runner when it was not
// given explicitly (D3/D4). It runs in Submit right after validate so the chosen
// id flows into both the Forward and the persisted JobResult.worker_id. It is a
// no-op for non-worker runners and for an explicit worker_id (explicit routing
// wins, labels ignored).
//
// Resolution order when worker_id is empty:
//   - worker_labels given: auto-select a connected worker advertising ALL labels
//     (least loaded / freshest, D3). No eligible candidate → ErrNoEligibleWorker.
//   - no labels: leave worker_id empty and rely on the runner's configured default
//     worker (D4 fallback); the worker runner errors if it has no default binding.
func (s *Service) selectTargetWorker(cfg *config.Config, req *JobRequest) error {
	if !isWorkerRunner(cfg, req.Runner) || req.WorkerID != "" {
		return nil
	}
	if len(req.WorkerLabels) == 0 {
		return nil // D4: fall back to the runner's configured default worker.
	}
	var cands []WorkerCandidate
	if s.workers != nil {
		cands = s.workers.Candidates()
	}
	picked := selectWorker(cands, req.WorkerLabels)
	if picked == "" {
		return fmt.Errorf("%w: no eligible worker for labels %v", ErrNoEligibleWorker, req.WorkerLabels)
	}
	req.WorkerID = picked // inject: Forward + JobResult.worker_id now use it.
	return nil
}

// checkRunnerAllowed verifies req.Runner is in the project allowlist. The
// built-in "local" runner is accepted when the allowlist is empty or lists it.
func checkRunnerAllowed(proj config.ProjectConfig, runnerKey string) error {
	for _, r := range proj.AllowedRunners {
		if r == runnerKey {
			return nil
		}
	}
	if runnerKey == builtinLocalRunner && len(proj.AllowedRunners) == 0 {
		return nil
	}
	return fmt.Errorf("runner %q is not allowed in project", runnerKey)
}
