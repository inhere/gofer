package job

import (
	"fmt"
	"slices"

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

// IsRemoteRunner reports whether name is any remote runner (peer-http or
// ws-worker); both share the host-side "skip local resolution + set Forward"
// path in Submit.
func IsRemoteRunner(cfg *config.Config, name string) bool {
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

	// A resume submission (ResumeSourceAgent set) mechanically carries Agent="exec"
	// — the resume argv runs as the built-in exec carrier — but its REAL identity
	// for access control is the SOURCE agent whose session is being续接. Gate the
	// allowed_agents check AND the exec security gate below on that source agent:
	// resume only re-runs the source agent's CLI in a constrained, templated form,
	// so it must NOT demand the broad allow_exec the exec carrier would (2026-06-26
	// decision; ResumeSourceAgent doc on JobRequest). For a normal job gateAgent ==
	// req.Agent, preserving the established behaviour (exec is not exempt).
	gateAgent := req.Agent
	if req.ResumeSourceAgent != "" {
		gateAgent = req.ResumeSourceAgent
	}

	// Agent must be in the project's allowed_agents (exec is not exempt).
	if err := agent.CheckAllowed(cfg, req.ProjectKey, gateAgent); err != nil {
		return config.ProjectConfig{}, fmt.Errorf("%w: %s", ErrInvalidRequest, err.Error())
	}

	if req.Interactive {
		if remote && !isWorkerRunner(cfg, req.Runner) {
			return config.ProjectConfig{}, fmt.Errorf("%w: interactive not supported on peer runner", ErrInvalidRequest)
		}
		ac, ok := agent.ResolveAgent(cfg, req.Agent)
		if !ok {
			return config.ProjectConfig{}, fmt.Errorf("%w: unknown agent %q", ErrInvalidRequest, req.Agent)
		}
		if !ac.Interactive {
			return config.ProjectConfig{}, fmt.Errorf("%w: agent %q is not interactive", ErrInvalidRequest, req.Agent)
		}
		if !slices.Contains(proj.InteractiveAllowedAgents, req.Agent) {
			return config.ProjectConfig{}, fmt.Errorf("%w: agent %q not in interactive_allowed_agents", ErrInvalidRequest, req.Agent)
		}
		if ac.Type == agent.TypeExec || !ac.NoRawCmd {
			return config.ProjectConfig{}, fmt.Errorf("%w: interactive agent must be no-raw-cmd and non-exec", ErrInvalidRequest)
		}
		if len(req.Cmd) > 0 {
			return config.ProjectConfig{}, fmt.Errorf("%w: interactive job cannot override Cmd", ErrInvalidRequest)
		}
	}
	if req.RecordPty {
		if !req.Interactive {
			return config.ProjectConfig{}, fmt.Errorf("%w: record_pty requires interactive=true", ErrInvalidRequest)
		}
		if !cfg.Storage.Cast.Enabled {
			return config.ProjectConfig{}, fmt.Errorf("%w: record_pty requires storage.cast.enabled=true", ErrInvalidRequest)
		}
	}

	if !remote {
		// exec security gate: the agent must be type exec AND the project must opt
		// in. Skipped for remote jobs — the peer enforces its own exec gate. For a
		// resume, gateAgent is the source agent: a cli-agent (claude/codex) source is
		// not exec-type so it passes regardless of allow_exec, while a (contrived)
		// exec-type source still honours the original allow_exec requirement.
		ac, ok := agent.ResolveAgent(cfg, gateAgent)
		if !ok {
			return config.ProjectConfig{}, fmt.Errorf("%w: unknown agent %q", ErrInvalidRequest, gateAgent)
		}
		if ac.Type == agent.TypeExec && !proj.AllowExec {
			return config.ProjectConfig{}, fmt.Errorf("%w: exec agent %q not allowed: project %q has allow_exec=false", ErrInvalidRequest, gateAgent, req.ProjectKey)
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
// no-op for non-worker runners. For an explicit worker_id, explicit routing wins
// over labels but interactive requests still verify the selected worker's pty
// capability at admission time.
//
// Resolution order when worker_id is empty:
//   - worker_labels given: auto-select a connected worker advertising ALL labels
//     (least loaded / freshest, D3). No eligible candidate → ErrNoEligibleWorker.
//   - no labels: leave worker_id empty and rely on the runner's configured default
//     worker (D4 fallback). For interactive requests, resolve that default now
//     and verify it is a live pty-capable worker so the final worker is known
//     before dispatch.
func (s *Service) selectTargetWorker(cfg *config.Config, req *JobRequest) error {
	if !isWorkerRunner(cfg, req.Runner) {
		return nil
	}
	if req.WorkerID != "" {
		return s.checkInteractiveWorkerPty(req.WorkerID, req.Interactive)
	}
	if len(req.WorkerLabels) == 0 {
		if req.Interactive {
			workerID := cfg.Runners[req.Runner].WorkerID
			if workerID == "" {
				return fmt.Errorf("%w: runner %q has no default worker_id", ErrNoEligibleWorker, req.Runner)
			}
			if err := s.checkInteractiveWorkerPty(workerID, true); err != nil {
				return err
			}
			req.WorkerID = workerID
		}
		return nil // D4: fall back to the runner's configured default worker.
	}
	var cands []WorkerCandidate
	if s.workers != nil {
		cands = s.workers.Candidates()
	}
	picked := selectWorker(cands, req.WorkerLabels, req.Interactive)
	if picked == "" {
		return fmt.Errorf("%w: no eligible worker for labels %v", ErrNoEligibleWorker, req.WorkerLabels)
	}
	req.WorkerID = picked // inject: Forward + JobResult.worker_id now use it.
	return nil
}

func (s *Service) checkInteractiveWorkerPty(workerID string, interactive bool) error {
	if !interactive {
		return nil
	}
	if s.workers == nil {
		return fmt.Errorf("%w: worker %q has no pty capability snapshot", ErrNoEligibleWorker, workerID)
	}
	cand, ok := s.workers.Candidate(workerID)
	if !ok || !cand.PtyCapable {
		return fmt.Errorf("%w: worker %q is not pty-capable", ErrNoEligibleWorker, workerID)
	}
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
