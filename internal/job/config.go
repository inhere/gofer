package job

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

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
//
// Federation (P3): the project/agent checks are scoped to the runner the job will
// EXECUTE on (capabilitiesFor, P2) — see the project block below (G1) and the
// capability gate at the end (G2).
func (s *Service) validate(cfg *config.Config, req JobRequest, remote bool) (config.ProjectConfig, error) {
	isWorker := isWorkerRunner(cfg, req.Runner)

	proj, projKnown := cfg.Projects[req.ProjectKey]
	if !projKnown {
		if !isWorker {
			// local / peer-http: the project must still be defined in the host's global
			// config (behaviour unchanged — the host resolves the cwd/exec for a local
			// job, and a peer job keeps the pre-federation contract).
			return config.ProjectConfig{}, fmt.Errorf("%w: unknown project %q", ErrUnknownProject, req.ProjectKey)
		}
		// G1: a worker-only project (defined in the WORKER's config, reported at
		// register) needs no duplicate definition here. The host does not execute the
		// job — the worker resolves cwd/agent/exec with its own config — so a
		// placeholder proj carrying only what the HOST still does is enough
		// (workerOnlyProject). The key is request-supplied now (not a config map key)
		// and becomes a directory name, so it is charset-checked first.
		if err := checkProjectKey(req.ProjectKey); err != nil {
			return config.ProjectConfig{}, fmt.Errorf("%w: %s", ErrInvalidRequest, err.Error())
		}
		proj = workerOnlyProject(cfg, req.ProjectKey, req.Runner)
	}

	// A resume submission (ResumeSourceAgent set) mechanically carries Agent="exec"
	// — the resume argv runs as the built-in exec carrier — but its REAL identity
	// for access control is the SOURCE agent whose session is being续接. Gate the
	// allowed_agents check AND the exec security gate below on that source agent:
	// resume only re-runs the source agent's CLI in a constrained, templated form,
	// so it must NOT demand the broad allow_exec the exec carrier would (2026-06-26
	// decision; ResumeSourceAgent doc on JobRequest). For a normal job gateAgent ==
	// req.Agent, preserving the established behaviour (exec is not exempt).
	gateAgent := gateAgentOf(req)

	// Agent whitelist. Empty allowed_agents = allow all configured agents (§13,
	// config-optimize); exec safety is enforced independently by the allow_exec
	// gate below (default false), so an empty whitelist never opens exec.
	if len(proj.AllowedAgents) > 0 {
		if err := agent.CheckAllowed(cfg, req.ProjectKey, gateAgent); err != nil {
			return config.ProjectConfig{}, fmt.Errorf("%w: %s", ErrInvalidRequest, err.Error())
		}
	}

	if req.Interactive {
		if remote && !isWorkerRunner(cfg, req.Runner) {
			return config.ProjectConfig{}, fmt.Errorf("%w: interactive not supported on peer runner", ErrInvalidRequest)
		}
		interactiveAgent := req.Agent
		resumeCarrier := req.ResumeSourceAgent != ""
		if resumeCarrier {
			interactiveAgent = req.ResumeSourceAgent
		}
		ac, ok := agent.ResolveAgent(cfg, interactiveAgent)
		if !ok {
			return config.ProjectConfig{}, fmt.Errorf("%w: unknown agent %q", ErrInvalidRequest, interactiveAgent)
		}
		if !ac.Interactive {
			return config.ProjectConfig{}, fmt.Errorf("%w: agent %q is not interactive", ErrInvalidRequest, interactiveAgent)
		}
		if !slices.Contains(proj.InteractiveAllowedAgents, interactiveAgent) {
			return config.ProjectConfig{}, fmt.Errorf("%w: agent %q not in interactive_allowed_agents", ErrInvalidRequest, interactiveAgent)
		}
		if ac.Type == agent.TypeExec || !ac.NoRawCmd {
			return config.ProjectConfig{}, fmt.Errorf("%w: interactive agent must be no-raw-cmd and non-exec", ErrInvalidRequest)
		}
		if len(req.Cmd) > 0 && !resumeCarrier {
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
		if len(req.AgentArgs) > 0 && ac.Type == agent.TypeExec {
			return config.ProjectConfig{}, fmt.Errorf("%w: agent_args not allowed for exec agent %q", ErrInvalidRequest, gateAgent)
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
	if isWorker && req.WorkerID != "" {
		if _, ok := cfg.Server.Workers[req.WorkerID]; !ok {
			return config.ProjectConfig{}, fmt.Errorf("%w: unknown worker_id %q", ErrInvalidRequest, req.WorkerID)
		}
	}

	// A PINNED runner (type=worker + worker_id in config) names exactly one target, and
	// that pin is an AUTHORIZATION, not a default route: allowed_runners is how a project
	// says "on the worker side, only this box may run me", and that statement is worthless
	// if the request can re-point the runner elsewhere. It used to be able to — an explicit
	// worker_id (runner/worker.Runner: f.WorkerID beats r.workerID) or worker_labels
	// (selectTargetWorker prefers the label branch) silently overrode the pin, so a job
	// submitted through a runner pinned to w-a could execute on w-b whenever w-b happened
	// to carry the project. Reject the override instead of honouring it; an explicit
	// worker_id equal to the pin stays legal (it is a no-op restatement).
	if isWorker {
		if pin := cfg.Runners[req.Runner].WorkerID; pin != "" {
			if req.WorkerID != "" && req.WorkerID != pin {
				return config.ProjectConfig{}, fmt.Errorf(
					"%w: runner %q is pinned to worker %q; worker_id %q cannot re-route it",
					ErrInvalidRequest, req.Runner, pin, req.WorkerID)
			}
			if len(req.WorkerLabels) > 0 {
				return config.ProjectConfig{}, fmt.Errorf(
					"%w: runner %q is pinned to worker %q; worker_labels cannot re-route it",
					ErrInvalidRequest, req.Runner, pin)
			}
		}
	}

	// G2 (federation): the target worker is DETERMINED here — an explicit worker_id,
	// or the runner's configured default when no labels are given (the same order
	// selectTargetWorker resolves in, D4). Fail fast on the host when the project or
	// the agent is not on that worker instead of paying a dispatch round-trip for a
	// rejection the worker's own validate would issue.
	//
	// Skipped when worker_labels drive an auto-select (worker not yet chosen): the
	// runner's configured default is NOT the job's target then, so gating on it would
	// reject jobs a labelled peer could run. selectWorker filters those candidates by
	// project+agent instead (T3.3). Not applicable to peer-http (a peer resolves with
	// its own config and is out of federation scope) nor to local (the global config
	// IS the capability view — already enforced above).
	//
	// online=false (worker offline / unregistered / no worker id resolvable) leaves
	// the pre-federation behaviour untouched: no capability view to validate against,
	// so admission falls through and dispatch fails later as before.
	if isWorker && (req.WorkerID != "" || len(req.WorkerLabels) == 0) {
		if wprojs, wagents, online := s.capabilitiesFor(cfg, req.Runner, req.WorkerID); online {
			if !slices.Contains(wprojs, req.ProjectKey) {
				return config.ProjectConfig{}, fmt.Errorf("%w: project %q not on worker for runner %q", ErrUnknownProjectOnRunner, req.ProjectKey, req.Runner)
			}
			// An empty agent is left to the executor (unchanged): the host does not
			// resolve agents for remote jobs, so it has no key to check here.
			if gateAgent != "" && !slices.Contains(wagents, gateAgent) {
				return config.ProjectConfig{}, fmt.Errorf("%w: agent %q not on worker for runner %q", ErrAgentNotOnRunner, gateAgent, req.Runner)
			}
		}
	}
	return proj, nil
}

// gateAgentOf returns the agent identity admission checks run against: for a
// resume submission the SOURCE agent (the carrier is mechanically "exec", see
// validate), otherwise the requested agent. Used by both validate (G2) and
// selectTargetWorker (T3.3) so both gates judge the same identity.
func gateAgentOf(req JobRequest) string {
	if req.ResumeSourceAgent != "" {
		return req.ResumeSourceAgent
	}
	return req.Agent
}

// projectKeyRe bounds a REQUEST-supplied project key (worker-only project, G1) to
// one safe path segment. For a configured project the key is a config map key, but
// a worker-only key comes from the request and still becomes a directory name
// (<storage.root>/<key>/<job_id>, …) — so it must not be able to traverse (`..`),
// absolutise or smuggle separators/control chars.
var projectKeyRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func checkProjectKey(key string) error {
	if !projectKeyRe.MatchString(key) {
		return fmt.Errorf("invalid project key %q", key)
	}
	return nil
}

// workerOnlyStoreSubdir is where the HOST keeps its side of a worker-only
// project's jobs (mirrored logs + the DB-indexed result dir) when storage.root is
// unset. See workerOnlyProject.
const workerOnlyStoreSubdir = "remote"

// workerOnlyProject synthesizes the host-side placeholder ProjectConfig for a
// project that exists only on the target worker (G1, R2). The host never executes
// such a job — the worker resolves cwd/agent/exec/allow_exec with ITS OWN config
// (design §10, decision #2) — so the placeholder only has to carry what the HOST
// still does with proj on the worker dispatch path:
//
//   - checkRunnerAllowed(proj, req.Runner): there is no host-side project policy for
//     a project the host does not define, and the worker capability gate (G2) plus
//     the worker's own validate are the real admission boundary — so the requested
//     worker runner is the one this placeholder authorises.
//   - project.ResultBaseDir(cfg, key, proj): the host keeps a local result dir for
//     the mirrored logs / index entry even though the job runs remotely.
//   - proj.MaxConcurrentJobs (0 = unbounded host-side gating; the worker enforces
//     its own max_concurrent).
//
// Every other proj field is read only on the LOCAL branch of Submit (SafeJoin cwd,
// env_files, allow_exec, agent build), which a worker job never takes.
//
// Result dir: with storage.root set, ResultBaseDir is key-driven (<root>/<key>) and
// does not read proj at all. With root unset it falls back to
// <host_path>/<exchange_subdir>/<result_subdir> — and an EMPTY host_path resolves
// (filepath.Abs) to the serve process CWD, which would scatter results into a random
// directory. Mirror Config.ResolveDBPath's fallback instead and keep the host side of
// worker-only projects under the config dir, keyed by project:
// <config-dir>/remote/<project_key>/<job_id>.
func workerOnlyProject(cfg *config.Config, projectKey, runnerKey string) config.ProjectConfig {
	proj := config.ProjectConfig{AllowedRunners: []string{runnerKey}}
	if strings.TrimSpace(cfg.Storage.Root) != "" {
		return proj
	}
	dir, err := config.ConfigDir()
	if err != nil || dir == "" {
		dir = "." // degrade like ResolveDBPath: a usable (if non-ideal) relative path.
	}
	proj.HostPath = dir
	proj.ExchangeSubdir = workerOnlyStoreSubdir
	proj.ResultSubdir = projectKey
	return proj
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
//     AND the job's project+agent (G2, federation P3). No eligible candidate →
//     ErrNoCapableWorker (which wraps ErrNoEligibleWorker, so the HTTP 503 mapping
//     is unchanged).
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
	// G2 (federation): the candidate must also REPORT the job's project + agent —
	// labels/pty alone can pick a worker that cannot run the job. gateAgentOf keeps
	// the resume carrier from being judged as "exec" (validate uses the same identity).
	gateAgent := gateAgentOf(*req)
	picked := selectWorker(cands, req.WorkerLabels, req.Interactive, req.ProjectKey, gateAgent)
	if picked == "" {
		return fmt.Errorf("%w: labels=%v project=%q agent=%q", ErrNoCapableWorker, req.WorkerLabels, req.ProjectKey, gateAgent)
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
