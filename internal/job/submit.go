package job

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/store"
	"github.com/inhere/gofer/internal/util"
	"github.com/inhere/gofer/internal/wsproto"
)

// xferUploadsToRunner projects the request's upload specs onto the runner package's
// type (runner is a leaf and owns no job types).
func xferUploadsToRunner(in []UploadSpec) []runner.XferUpload {
	if len(in) == 0 {
		return nil
	}
	out := make([]runner.XferUpload, 0, len(in))
	for _, u := range in {
		out = append(out, runner.XferUpload{XferID: u.XferID, Dest: u.Dest, Base: u.Base})
	}
	return out
}

// baselineSHARe is what a request's BaselineSHA may look like: a (possibly abbreviated)
// hex commit id, never a ref or an option.
var baselineSHARe = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)

// Submit validates the request, creates the result dir, persists the request and
// starts the job asynchronously. It returns the initial JobResult (status
// running) once the goroutine is launched. Validation/setup failures return an
// error and no job.
func (s *Service) Submit(req JobRequest) (JobResult, error) {
	if req.admissionPermit != nil {
		return s.SubmitWithPermit(req.admissionPermit, req)
	}
	permit, err := s.BeginUpgradeWork()
	if err != nil {
		// gofer-dzv3: while an update drains the server, admission is closed and a
		// submit is normally refused (ErrUpgradeDraining). When THAT update is the one
		// pausing starts, the submit parks instead of being lost — the gap between
		// "nothing left to wait for" and "the new server is up" is exactly where a
		// caller would otherwise get a 503 for a job nobody ever gets to run. A manual
		// `serve upgrade` (no pause) keeps refusing.
		if errors.Is(err, ErrUpgradeDraining) && s.UpdatePauseOwner() != "" {
			return s.submitAdmitted(req)
		}
		return JobResult{}, err
	}
	defer permit.Release()
	return s.submitAdmitted(req)
}

func (s *Service) submitAdmitted(req JobRequest) (JobResult, error) {
	// Snapshot the config ONCE for the whole Submit so a concurrent Reload cannot
	// make this single submit observe two different configs (peer classification,
	// validation, result base dir all read the same snapshot).
	cfg := s.config()

	// gofer-9b1b: a held submit keeps the request AS RECEIVED — before any resolution
	// below rewrites it — because approval re-submits exactly that request (and every
	// resolution step runs again then). The internal json:"-" markers a server-side
	// producer set ride the hold record, since request_json cannot carry them.
	// gofer-dzv3: an update waiting for idle parks a job the same way, so the decision
	// whether this submit parks is taken ONCE, here, and the raw request below is the
	// one that gets parked (a pause that ends mid-submit lets the job run normally).
	var holdRaw []byte
	var holdCarry *heldInternals
	// A job gofer submits on its own behalf to reach a session (UpdateExempt) is not
	// parked: it is short, and losing it is exactly the damage the pause is meant to
	// avoid (see UpdateExempt).
	pauseForUpdate := req.heldJobID == "" && s.UpdatePauseOwner() != "" && !req.UpdateExempt
	park := req.holdPending() || pauseForUpdate
	// AUTO-04b: a decision hook may turn this submit into a held one, so the request
	// as received is kept whenever one could run. Neither the approval re-entry (a
	// human already approved what the hooks shaped) nor a worker's re-entry of a job
	// the hub decided asks them again.
	askHooks := req.heldJobID == "" && !req.CredentialExternal && len(cfg.Server.DecisionHooks) > 0
	var received []byte
	if park || askHooks {
		b, err := json.Marshal(req)
		if err != nil {
			return JobResult{}, fmt.Errorf("marshal held request: %w", err)
		}
		received, holdCarry = b, captureHeldInternals(&req)
		if park {
			holdRaw = received
		}
	}

	// gofer-5foz: per-phase timing of this synchronous path; one warn when slow.
	tm := newPhaseTimer()
	timedJobID := ""
	defer func() {
		tm.report("job.submit_slow", submitSlowThreshold, "job_id", timedJobID, "project", req.ProjectKey, "agent", req.Agent)
	}()

	// Runner spelling first: everything below (remote classification, validate's
	// allowlist check, the runner registry lookup, the persisted row) keys on the
	// canonical name. See normalizeRunner.
	req.Runner = config.NormalizeRunnerName(req.Runner)
	req.SourceSessionID = strings.TrimSpace(req.SourceSessionID)
	req.FromSession = strings.TrimSpace(req.FromSession)

	// E35: resolve a role preset BEFORE validate so the role-filled agent/project
	// are still allowlist-checked (and an empty agent does not fail validation
	// first). Explicit request fields always win over the preset's defaults.
	if err := resolveRole(cfg, &req); err != nil {
		return JobResult{}, err
	}

	// SUP-01 P5: render a task-book template into the request. It runs AFTER
	// resolveRole (a role may supply the project key the template is looked up in)
	// and BEFORE resolveVerify / validate, so a template default is itself validated
	// and the executed command, the Forward, request_json and the persisted row all
	// carry one decided prompt.
	tpl, err := s.renderTemplate(cfg, &req)
	if err != nil {
		return JobResult{}, err
	}
	applyTemplateMeta(&req, tpl)
	// AUTO-04b: the job.submit decision hooks see the decided work (role and
	// template applied) before gofer's own sections and rules are added, and may
	// refuse it or make it more conservative. Their patch is carried onto the request
	// kept for a hold, so the approval re-entry describes the same work.
	if askHooks {
		patches, recs, err := s.decideSubmit(cfg, &req)
		if err != nil {
			return JobResult{}, err
		}
		req.decisions = recs
		if len(patches) > 0 && (req.holdPending() || park) {
			if holdRaw, err = patchReceived(received, patches); err != nil {
				return JobResult{}, err
			}
		}
	}
	// gofer-3nxa.4 / .3: the acceptance criteria and the 「交付约定」 section are appended
	// to the RENDERED prompt (a task book is part of the work description, these close
	// it), before the interactive first input is derived from it and before the rules
	// are prepended.
	injectPromptSections(cfg, &req)
	if req.Interactive && req.InitialInput == "" {
		req.InitialInput = interactiveInitialInput(req.Prompt)
	}

	// SUP-01 P2: resolve the verify step (argv + deadline) from the SAME cfg snapshot
	// BEFORE validate, so the admission gates, the Forward, request_json and the
	// persisted row all carry one decided pair — an executing machine (or a rerun)
	// never re-derives the project default. A verify/no_verify contradiction is
	// rejected here, while both are still visible.
	if err := resolveVerify(cfg, &req); err != nil {
		return JobResult{}, err
	}
	req.Secrets = normalizeSecretNames(req.Secrets)

	// gofer-9b1b: refuse the shapes a hold cannot cover before their own admission
	// checks run (role and template have filled the request by now), so the caller is
	// told to drop --hold rather than something else.
	if req.Hold {
		if err := checkHoldCombination(req); err != nil {
			return JobResult{}, err
		}
	}

	// A remote runner (peer-http OR ws-worker) forwards the original request to a
	// remote executor that resolves agent/cwd/command with its OWN config. The host
	// therefore skips local agent/cwd resolution for remote jobs (it still validates
	// the project, the agent allowlist and the runner allowlist).
	remote := IsRemoteRunner(cfg, req.Runner)
	if req.Steward && remote {
		return JobResult{}, fmt.Errorf("%w: the steward runs on the server's own machine", ErrInvalidRequest)
	}

	// JOB-10: expand the four binding levels (server → agent → project → this
	// request) into the job's final skill list from the SAME cfg snapshot, check every
	// name against the library, and stage the files for a worker-bound job. It runs
	// BEFORE validate and before the result dir exists, so an unknown skill name is a
	// rejected submit that creates nothing.
	//
	// CEILING ADMISSION (design 2026-10-18 §准入) runs BEFORE it, because staging a skill
	// for a worker-bound job is already a side effect: nothing is created, staged or locked
	// for a submit a ceiling refuses. It is the single point every submission path funnels
	// through (HTTP, timer, plan item, retry, hold approval), and the refusal is permanent
	// (ErrCeilingDenied), so a re-submission path terminates instead of retrying.
	if err := admitCeiling(cfg, req); err != nil {
		return JobResult{}, err
	}
	if err := s.resolveSkills(cfg, &req, remote); err != nil {
		return JobResult{}, err
	}

	// JOB-06①: same idea for the MANDATORY rules — the four-level union plus the
	// project's `.gofer/RULES.md`, checked against the size cap, rendered into
	// req.Prompt (so request_json owns the text the agent will read) and kept on the
	// request as the {name,sha256} list the row records. It runs BEFORE validate, so an
	// unknown rule or an over-limit set is a rejected submit that creates nothing.
	if err := s.resolveRules(cfg, &req, remote); err != nil {
		return JobResult{}, err
	}

	// MCP-06: the registry MCP servers this job is bound to — the same four-level
	// union, every name checked against the registry and the agent's ability to take
	// them, so a binding that cannot be honoured is a rejected submit.
	if err := resolveMCP(cfg, &req, remote); err != nil {
		return JobResult{}, err
	}

	tm.mark("resolve")
	proj, err := s.validate(cfg, req, remote)
	if err != nil {
		return JobResult{}, err
	}
	// JOB-06②: the secrets are checked against the project and resolved on THIS
	// machine (only a local runner takes them) before anything is created, so a
	// missing variable is a refused submit that names it.
	if err := checkSecrets(cfg, proj, req, remote); err != nil {
		return JobResult{}, err
	}
	var secretEnv map[string]string
	var secretValues []string
	if !req.holdPending() {
		if secretEnv, secretValues, err = resolveSecrets(cfg, req.Secrets); err != nil {
			return JobResult{}, err
		}
	}
	tm.mark("validate")
	if req.Session {
		if remote && !isWorkerRunner(cfg, req.Runner) {
			return JobResult{}, fmt.Errorf("%w: 持续会话目前仅支持本机 runner", ErrInvalidRequest)
		}
		if req.Interactive || req.IdleTimeoutSec < 0 || req.MaxSessionSec < 0 {
			return JobResult{}, fmt.Errorf("%w: ACP session requires non-interactive mode and non-negative session timeouts", ErrInvalidRequest)
		}
		if !remote {
			ac, ok := agent.ResolveAgent(cfg, req.Agent)
			if !ok || ac.Type != agent.TypeACPAgent {
				return JobResult{}, fmt.Errorf("%w: session requires an acp-agent", ErrInvalidRequest)
			}
		}
		if req.IdleTimeoutSec == 0 {
			req.IdleTimeoutSec = 1800
		}
	}
	// SUP-01 C: a todo-attached submit resolves its plan from the todo (and refuses a
	// contradiction) BEFORE anything is persisted, so a bad linkage is a rejected
	// submit rather than a job that silently links nothing.
	if err := s.todoPlanForSubmit(&req); err != nil {
		return JobResult{}, err
	}
	// SUP-01 P3: resolve the failover plan from the SAME cfg snapshot (request >
	// project > agent) and freeze it on the request, so every link of the chain runs
	// the ONE list the root resolved. A job that IS a takeover inherits the plan it
	// was handed (json:"-" internal field) instead of re-reading a config that may
	// have changed while the chain was running.
	fallback := &FallbackState{Candidates: resolveFallbackCandidates(cfg, req.ProjectKey, req.Agent, req.FallbackAgents)}
	if req.Fallback != nil {
		fallback = req.Fallback
	}
	if len(fallback.Candidates) == 0 {
		fallback = nil
	}
	// SUP-01 P3 (pre_dispatch): with server.agent_fallback.pre_dispatch on, an agent
	// that is currently degraded hands the job to the first candidate that is not,
	// BEFORE anything is dispatched. The row keeps the agent the caller asked for
	// (requested_agent) and an event says why.
	substitutedFrom := ""
	if cfg.AgentFallbackPreDispatch() {
		if to, depth := s.substituteDegradedAgent(fallback, req.RequestedAgent); to != "" {
			substitutedFrom = req.Agent
			req.Agent = to
			req.Fallback = &FallbackState{Candidates: fallback.Candidates, Depth: depth}
			fallback = req.Fallback
		}
	}
	req.Fallback = fallback

	// Preserve whether the caller explicitly chose --shared-dir/--exclusive-dir before
	// stamping the resolved three-state decision below. Repo-mode admission needs this
	// distinction; an internally resolved default is not an explicit declaration.
	explicitDirLockMode := req.ExclusiveDir != nil
	// JOB-11: resolve the same-directory lock decision ONCE, from the SAME cfg snapshot
	// that validated the request and AFTER any submit-time agent substitution (the
	// substituted agent's TYPE is what the default rule reads). Stamping it back onto
	// the request keeps one decided value on every surface: request_json, the persisted
	// row, the peer/worker forward and the executing machine.
	// A declared --lock asks for a lock, so it cannot be combined with an explicit
	// --shared-dir. Server-built continuations inherit their source's stamped
	// values and are not re-judged here.
	if len(req.LockPaths) > 0 && req.ExclusiveDir != nil && !*req.ExclusiveDir && req.ResumeSourceAgent == "" {
		return JobResult{}, fmt.Errorf("%w: --lock declares a directory lock and cannot be combined with --shared-dir", ErrInvalidRequest)
	}
	dirExclusive := resolveDirExclusive(cfg, &req)
	req.ExclusiveDir = &dirExclusive

	// AUTO-05: same for the output-stall window — resolved from the SAME snapshot
	// (request > agent > server, exec jobs off unless asked) and stamped, so the
	// executing machine watches with the window this server decided instead of
	// re-deriving one from its own config.
	stallAgentType := ""
	if ac, ok := agent.ResolveAgent(cfg, req.Agent); ok {
		stallAgentType = ac.Type
	}
	stallSec := cfg.EffectiveStallTimeoutSec(req.Agent, stallAgentType, req.StallTimeoutSec, req.Interactive)
	req.StallTimeoutSec = &stallSec

	// JOB-11 (F12): same for the directory-lock WAIT cap — resolved from the SAME
	// snapshot and stamped, so a remote executor applies this server's policy instead of
	// re-deriving one from its own config (the rule ExclusiveDir/StallTimeoutSec follow).
	if req.LockWaitSec != nil && req.DirWaitMaxSec != nil && *req.LockWaitSec != *req.DirWaitMaxSec {
		return JobResult{}, fmt.Errorf("%w: lock_wait_sec conflicts with dir_wait_max_sec", ErrInvalidRequest)
	}
	requestedWait := req.LockWaitSec
	if requestedWait == nil {
		requestedWait = req.DirWaitMaxSec
	}
	if requestedWait != nil && *requestedWait < 0 {
		return JobResult{}, fmt.Errorf("%w: lock_wait_sec must be >= 0", ErrInvalidRequest)
	}
	if requestedWait != nil && *requestedWait == 0 && cfg.Server.DirLockAllowUnboundedWait != nil && !*cfg.Server.DirLockAllowUnboundedWait && !req.CredentialExternal {
		return JobResult{}, fmt.Errorf("%w: lock_wait_sec=0 is disabled by server.dir_lock_allow_unbounded_wait", ErrInvalidRequest)
	}
	dirWaitSec := cfg.EffectiveDirLockMaxWaitSec(requestedWait)
	req.DirWaitMaxSec = &dirWaitSec
	req.LockWaitSec = &dirWaitSec

	// bd h-aii-s9ck: resolve the job's deadline ONCE, from the SAME cfg snapshot as
	// validation (project ceiling > server ceiling > 1h default), BEFORE the entry /
	// forward are built. The running job (execute's ctx), the persisted row, the API
	// response and a remote dispatch then all carry one number, and a truncated
	// request is reported instead of silently clamped.
	timeout, timeoutClamped := admittedTimeout(cfg, &req)
	timeoutSec := int(timeout / time.Second)
	limitSec, limitSource := cfg.EffectiveMaxTimeout(req.ProjectKey)
	if timeoutClamped && req.StrictTimeout {
		return JobResult{}, fmt.Errorf("%w: timeout %ds exceeds the limit %ds set by %s (strict_timeout refuses to clamp)",
			ErrInvalidRequest, req.TimeoutSec, limitSec, config.TimeoutLimitKey(limitSource, req.ProjectKey))
	}
	if timeoutSec == 0 {
		limitSource = "" // no deadline (an interactive session): no ceiling applies
	}
	if len(req.EnvFiles) > 0 && (remote || req.Runner != builtinLocalRunner) {
		return JobResult{}, fmt.Errorf("%w: env_files are supported only for local runner jobs", ErrInvalidRequest)
	}

	// Attempt is 1-based: a first run (no engine-set attempt) is attempt 1 when the
	// job opts into retry (E24) OR belongs to a workflow, so the persisted attempt
	// numbering is meaningful. A plain non-retry job leaves it 0 (omitempty).
	if req.Attempt < 1 && (req.Retry != nil || req.WorkflowID != "") {
		req.Attempt = 1
	}

	// The baseline ends up in git arguments (base..HEAD): only a commit id is accepted.
	if req.BaselineSHA != "" && !baselineSHARe.MatchString(req.BaselineSHA) {
		return JobResult{}, fmt.Errorf("%w: baseline_sha must be a commit id", ErrInvalidRequest)
	}

	// An explicit title is the caller's own text: allow it generously (only the
	// auto-extracted default is kept short). A resumed job inherits its source's
	// title, so it is trimmed rather than rejected.
	if len([]rune(strings.TrimSpace(req.Title))) > TitleMaxRunes && req.ResumedFrom == "" {
		return JobResult{}, fmt.Errorf("%w: title must be at most %d characters", ErrInvalidRequest, TitleMaxRunes)
	}
	if r := []rune(strings.TrimSpace(req.Title)); len(r) > TitleMaxRunes {
		req.Title = string(r[:TitleMaxRunes])
	}
	if strings.TrimSpace(req.Title) == "" {
		req.Title = defaultJobTitle(req)
	}

	// Resolve the target worker for a worker runner when worker_id was not given
	// explicitly (P2: labels → auto-select, else the runner's configured default).
	// Done right after validate so the chosen id rides the Forward + JobResult.
	if err := s.selectTargetWorker(cfg, &req); err != nil {
		return JobResult{}, err
	}
	if req.Session && isWorkerRunner(cfg, req.Runner) {
		workerID := req.WorkerID
		if workerID == "" {
			workerID = cfg.Runners[req.Runner].WorkerID
		}
		if workerID != "" && s.workers != nil {
			if caps, online := s.workers.Candidate(workerID); online && caps.ProtocolKnown && caps.ProtocolVersion < wsproto.SessionJobMinProtocolVersion {
				return JobResult{}, fmt.Errorf("%w: worker %s 协议 v%d 不支持持续会话，请升级", ErrInvalidRequest, workerID, caps.ProtocolVersion)
			}
		}
	}

	// N2 §B GATE-02: resolve the layered budget defaults (agent < project < request) into
	// the request NOW, so the Forward, request_json and the executing machine act on one
	// decided value; then refuse a budget the target worker's protocol cannot carry.
	resolveBudget(cfg, &req, remote)
	if err := s.checkBudgetWorker(cfg, req); err != nil {
		return JobResult{}, err
	}

	// WT-01: resolve the project-level worktree_default into the request NOW, so the
	// Forward, request_json and the executing machine all act on one explicit
	// decision (a worker then never has to re-derive a default from its own config).
	req.Worktree = worktreeRequested(cfg, &req)

	// GATE-01 S3: resolve the人工验收 request NOW (--review / the project's
	// require_review default / a workflow step's explicit override), so the Forward,
	// request_json and the persisted result all carry one decided value — the
	// executing side never has to re-derive a default from its own config.
	req.Review = reviewRequested(cfg, &req)

	// Resolve cwd to an absolute host dir inside the project root. Skipped for
	// remote jobs: the cwd is an opaque relative path the peer SafeJoins against
	// its own project root.
	var workDir string
	if !remote {
		workDir, err = project.SafeJoin(cfg.ExecPath(proj), req.Cwd)
		if err != nil {
			return JobResult{}, err
		}
	}
	if err := s.validateSourceSession(req, workDir, remote); err != nil {
		return JobResult{}, err
	}
	// gofer-3db4: who to tell when this job ends (an explicit --notify-session, else the
	// source session). Resolved and proven BEFORE any row exists: an unusable target
	// refuses the submit for a caller who named the flag, and is DROPPED (with a
	// job.notify_dropped event on the new job) for an internal producer.
	notifyDrop, err := s.resolveNotifyRequest(&req)
	if err != nil {
		return JobResult{}, err
	}
	// C5 idempotency: authenticate the request context first, then reuse the old job.
	// The approval re-entry of a held job would find ITSELF here, so it skips the check.
	if req.RequestID != "" && req.heldJobID == "" {
		if rec, ok, gerr := s.meta.GetJobByRequestID(req.RequestID); gerr != nil {
			return JobResult{}, gerr
		} else if ok {
			return s.reuseRequestJob(rec, req)
		}
	}
	if !remote && len(req.LockPaths) > 0 {
		resolved := make([]string, 0, len(req.LockPaths))
		for _, rel := range req.LockPaths {
			path, pathErr := project.SafeJoin(cfg.ExecPath(proj), rel)
			if pathErr != nil {
				return JobResult{}, fmt.Errorf("invalid lock path %q: %w", rel, pathErr)
			}
			resolved = append(resolved, path)
		}
		req.ResolvedLockPaths = resolved
	}
	if !remote && proj.DirLockMode == "repo" && !req.ReadOnly && !req.Interactive && !req.Worktree &&
		len(req.LockPaths) == 0 && !explicitDirLockMode && req.ResumeSourceAgent == "" {
		roots := nestedGitRoots(context.Background(), workDir)
		if len(roots) > 0 {
			relRoots := make([]string, 0, len(roots))
			for _, root := range roots {
				rel, relErr := filepath.Rel(workDir, root)
				if relErr == nil {
					relRoots = append(relRoots, filepath.ToSlash(rel))
				}
			}
			return JobResult{}, fmt.Errorf("repo lock mode requires an explicit lock declaration for nested repositories (%s); use --lock <repo>, --shared-dir, or --exclusive-dir", strings.Join(relRoots, ", "))
		}
	}

	tm.mark("admit")

	// gofer-9b1b: every admission check has run and nothing has been created yet — the
	// hold point. A held submit parks here (holdJob); the approval re-entry must still
	// describe the work the human approved. gofer-dzv3: an update waiting for idle parks
	// at the same point, so the job is fully validated (the caller gets the same errors)
	// and still starts only when the update releases it.
	//
	// holdPending is re-read HERE rather than taken from the snapshot above: a submission
	// decision hook (AUTO-04b) may have turned this submit into a held one a few lines
	// up (it patches the request before the hold point), and such a job must wait for its
	// human — never for the update, which would run it unapproved.
	if req.holdPending() {
		return s.holdJob(cfg, proj, &req, workDir, holdRaw, holdCarry)
	}
	if park {
		if res, parked, err := s.parkForUpdate(cfg, proj, &req, workDir, holdRaw, holdCarry); parked {
			return res, err
		}
	}
	if req.held != nil && req.held.Digest != "" && holdDigest(&req) != req.held.Digest {
		return JobResult{}, ErrHoldDrift
	}

	// Result base dir + a collision-resistant job id; create the dir up front.
	// The host keeps a local result dir even for proxied jobs so its logs (mirrored
	// from the peer) and DB index entry stay queryable. An approved held job keeps the
	// id (and the dir) it was given when it was held.
	base, err := project.ResultBaseDir(cfg, req.ProjectKey, proj)
	if err != nil {
		return JobResult{}, err
	}
	st := s.newStore(base)
	jobID := req.heldJobID
	if jobID != "" {
		if err := st.Ensure(jobID); err != nil && !errors.Is(err, os.ErrExist) {
			return JobResult{}, err
		}
	} else if jobID, err = s.createJobDir(st); err != nil {
		return JobResult{}, err
	}
	resultDir := st.Dir(jobID)
	// dropDir undoes the dir of a submit that will not run. A held job's dir predates
	// this submit and belongs to its row, so it stays.
	dropDir := func() {
		if req.heldJobID == "" {
			_ = os.RemoveAll(resultDir)
		}
	}
	timedJobID = jobID
	tm.mark("jobdir")

	// WT-01: `--worktree` runs the job in a managed git worktree of this checkout
	// (<top>/tmp/gofer/wt/<job-id>, branch gofer/<job-id>) so parallel jobs stop
	// sharing one index. Created HERE — on the EXECUTING machine (serve-local, or the
	// worker's own job.Service, which re-enters this very function with runner=local)
	// — and only after the job's result dir exists, because the worktree dir is keyed
	// by job id. A remote (forwarding) submit skips it: the flag rides the Forward and
	// the peer creates the worktree against its own project root (设计 §二).
	var wt *worktreeRef
	// `ref:` (design 一·3) and `--worktree` are mutually exclusive: the first is a
	// detached checkout of ONE fixed commit that is thrown away when the job ends, the
	// second a branch meant to be delivered. A job that asked for both has not decided
	// what it produces, so it is refused instead of guessing.
	if req.Ref != "" && req.Worktree {
		dropDir()
		return JobResult{}, fmt.Errorf("%w: ref and worktree are mutually exclusive (ref checks out one commit in a temporary worktree)", ErrInvalidRequest)
	}
	if (req.Worktree || req.Ref != "") && !remote {
		wtOpts, oerr := worktreeOptionsFor(proj, &req, jobID)
		if oerr != nil {
			dropDir()
			return JobResult{}, oerr
		}
		wtCtx, wtCancel := context.WithTimeout(context.Background(), worktreeTimeout)
		if req.Ref != "" {
			// `ref:` (design 一·3): the commit must be in THIS checkout before the temporary
			// worktree can be created — fetch it from the default remote when this machine
			// (typically a worker) has never seen it, and fail with the reason when the
			// remote does not have it either (D3: only pushed commits can be checked out
			// remotely).
			if ferr := fetchCommit(wtCtx, workDir, req.Ref); ferr != nil {
				wtCancel()
				dropDir()
				return JobResult{}, ferr
			}
			wt, err = prepareTempWorktree(wtCtx, workDir, jobID, req.Ref, wtOpts)
		} else {
			wt, err = prepareJobWorktree(wtCtx, workDir, jobID, req.WorktreeBase, wtOpts)
		}
		wtCancel()
		if err != nil {
			// No job was launched, so drop the just-created result dir (nothing else
			// references this id yet).
			dropDir()
			return JobResult{}, err
		}
		// Dependency links (2026-10-21 design 一) land BEFORE the job runs, so the agent
		// finds them from its first command. Best-effort by design: a missing source or a
		// refused link is an event, never a failed job.
		links := linkWorktreeDeps(wt, wtOpts.Links)
		// Keep only what was ACTUALLY linked: cleanup unlinks this list, not whatever the
		// project config says by then. An entry dropped from the config after the checkout
		// was created would otherwise be left for `git worktree remove --force` to follow
		// into the main checkout.
		wt.Links = links.Linked
		if len(links.Linked) > 0 || len(links.Skipped) > 0 {
			s.recordEvent(jobID, EventJobWorktreeLinks, map[string]any{
				"linked": links.Linked, "skipped": links.Skipped,
			})
		}
		if mapped, merr := wt.mapCwd(workDir); merr != nil {
			dropDir()
			return JobResult{}, merr
		} else {
			workDir = mapped
		}
	} else if !remote && req.InheritWorktree && req.ResumedFrom != "" {
		// A continuation of a --worktree job runs inside that job's worktree (see
		// resumeCwd); it carries the same worktree so its commits, review and
		// delivery cover the branch instead of losing it (gofer-glyo).
		wt = s.inheritedWorktree(req.ResumedFrom, workDir)
	}

	tm.mark("worktree")

	// Marshal the original request for audit / re-submit. It rides along on the
	// entry's result so every persist (queued/running/terminal) carries it into the
	// jobs.request_json column (SP5: replaces the on-disk request.json file).
	reqJSON, err := json.Marshal(req)
	if err != nil {
		return JobResult{}, fmt.Errorf("marshal request: %w", err)
	}

	// JOB-10 (决策 1, 2026-09-23): the prompt the job RUNS with — the caller's text
	// with the mounted-skills list in front of it — is assembled HERE, by the machine
	// that mounts the files, so every path in the list is one this machine can read.
	// The old design rendered it at submit with a {{skills_dir}} placeholder for the
	// executing machine to substitute, which handed a protocol-9 worker a list of
	// files it was never given (决策 1) and a peer-http job a list of paths no peer
	// could read (决策 2). request_json was marshalled above and keeps req.Prompt, so
	// audit/rerun replay the caller's ask rather than one machine's result dir.
	prompt := req.Prompt
	var injected string
	switch {
	case len(req.Skills) == 0:
	case remote && isWorkerRunner(cfg, req.Runner):
		// hub → worker: the WORKER renders the list, in its own Submit, after its
		// result dir exists — this machine only decided the binding and staged the
		// bytes (they ride the uploads with Base=result_dir).
	case remote:
		// peer-http (决策 2): a peer's transport carries no files and this hub knows
		// nothing about its paths, so a peer job mounts nothing and lists nothing. The
		// job still runs; the omission goes on its timeline instead of failing it.
		s.recordEvent(jobID, EventJobSkillsSkipped, map[string]any{
			"reason": "peer_runner", "names": req.Skills, "count": len(req.Skills),
		})
	default:
		// JOB-06①: the manifest goes AFTER the injected rules section — rules are the
		// frame, the skills list is content — so insertSkillsManifest locates the
		// section structurally instead of putting the list above it.
		injected = s.skillsPromptPrefix(&req, resultDir)
	}
	// CFG-16: the declared-tools section follows the skills list, by the same rule —
	// only the machine that runs the job knows what is installed on it, so a remote
	// (forwarding) submit renders nothing and the worker's own Submit renders its own.
	if !remote {
		injected += toolsPromptSection(cfg, &req)
	}
	prompt = insertSkillsManifest(prompt, injected)

	// gofer-s2er: the limit this job actually has and the moment it expires — resolved ONCE
	// (from the same ADMITTED timeout the row persists, i.e. after the project ceiling) and
	// used for both the prompt's last line and the gofer metadata env, so the line and the
	// variables can never disagree. It runs HERE, on the side that composes the prompt for
	// this job (the hub for a dispatched job carries it in Forward.Prompt, the worker's own
	// re-entry must not add a second), and BEFORE the runner branch so both cases get it.
	// gofer-drlm: the prompt line quotes the DURATION only — the absolute end is the moment
	// execution starts, and execute re-derives GOFER_JOB_DEADLINE from there (a prompt is
	// written once, here, but a job can queue for a long time before it runs).
	limit := jobTimeLimit(s.nowFn(), timeout, &req, 0)
	prompt = appendJobDeadlineLine(cfg, &req, prompt, limit)

	// JOB-06①: the injected-rules receipt. The result dir exists by now, so the event
	// can be recorded against the job; the DECISION (names, bytes) was taken in
	// resolveRules and is only reported here.
	if len(req.ruleRefs) > 0 {
		names := make([]string, 0, len(req.ruleRefs))
		for _, ref := range req.ruleRefs {
			names = append(names, ref.Name)
		}
		s.recordEvent(jobID, EventJobRulesInjected, map[string]any{
			"names": names, "bytes": req.ruleBytes,
		})
	}
	if req.rulesSkipped != "" {
		s.recordEvent(jobID, EventJobRulesSkipped, map[string]any{"reason": req.rulesSkipped})
	}

	run := s.runner(req.Runner)
	if run == nil {
		return JobResult{}, fmt.Errorf("runner %q is not available", req.Runner)
	}
	// WEB-03 T0: 仅【本机执行】的 interactive job 路由本机 pty runner。worker 远端
	// interactive 保持 worker runner(forward)，pty 在 worker 侧选(P2)。同一份代码在
	// serve 与 worker 都跑：worker 的 handleDispatch 强制 runner=local → !remote → 命中 pty。
	if req.Interactive && !remote {
		if pr := s.runner(builtinPtyRunner); pr != nil {
			run = pr
		}
	}
	// sessionID is the底层 agent CLI 会话标识 bound to this job (session-capture).
	// For a local cli-agent with a SessionInject template (claude) it is generated
	// here and injected into argv so gofer knows it immediately (模式①注入). An
	// explicit req.SessionID (resume path, P2) wins over injection — the job reuses
	// that exact id and capture (T1.4) is suppressed. Empty for codex (captured at
	//终态, T1.4) and for jobs whose agent supports neither.
	var sessionID string

	// Build the runner request. Remote jobs carry the ORIGINAL request in Forward
	// and leave Command/Args/WorkDir unset; local jobs resolve the executable form
	// (exec uses req.Cmd; cli-agent renders the prompt with cwd/job_id/result_dir).
	runReq := runner.Request{JobID: jobID, WorkDir: workDir}
	runReq.LockPaths = req.ResolvedLockPaths
	// XFER-01 X2: the file steps run where the job runs. For a LOCAL job that is this
	// machine (it places the uploads in workDir before the agent and matches the
	// collect globs there); a remote job's copies ride the Forward below instead.
	runReq.Uploads = xferUploadsToRunner(req.Uploads)
	runReq.Collect = req.Collect
	// JOB-10: the skills the executing machine must mount, and whether they are
	// already FINAL (a dispatched job must not re-union its own config's bindings).
	runReq.Skills = req.Skills
	runReq.SkillsResolved = req.SkillsResolved
	runReq.Interactive = req.Interactive
	runReq.Cols = req.Cols
	runReq.Rows = req.Rows
	// SUP-01 P2: the resolved verify step rides the request the job service executes
	// with, so the step runs on this machine in the job's own WorkDir/Env; a remote
	// job's copy of the same pair rides the Forward instead (see Forward.Verify).
	runReq.Verify = req.Verify
	runReq.VerifyTimeoutSec = req.VerifyTimeoutSec
	// Path B priming (session relay §9.1 B): the takeover job's first message is
	// typed into the pty once its terminal settles. The quiet window is resolved
	// HERE, from the same config snapshot as every other admitted value, so a
	// dispatched worker (Forward below) uses the window this server decided rather
	// than re-deriving one from its own config.
	runReq.InitialInput = req.InitialInput
	runReq.InitialInputQuietMs = req.InitialInputQuietMs
	runReq.Stdin = req.Stdin
	if req.InitialInput != "" && runReq.InitialInputQuietMs == 0 {
		runReq.InitialInputQuietMs = cfg.EffectiveSessionTakeoverInputDelayMs()
	}
	if remote {
		runReq.Forward = &runner.Forward{
			ProjectKey: req.ProjectKey,
			Agent:      req.Agent,
			PeerRunner: builtinLocalRunner,
			// Prompt is what the executing machine RUNS with. It equals req.Prompt for
			// every remote job (the list is rendered by the executing machine — JOB-10
			// 决策 1), and prompt is threaded through anyway so a future local forward
			// cannot silently drop the mount.
			Prompt:    prompt,
			AgentArgs: req.AgentArgs,
			Model:     req.Model,
			// gofer-f4z8: the executing machine renders ITS agent's from_session_args.
			FromSession: req.FromSession,
			Budget:      req.Budget,
			Cmd:         req.Cmd,
			Cwd:         req.Cwd,
			// WT-01: the executor creates the worktree (that machine owns the
			// checkout); the submitting side already resolved worktree_default into
			// req.Worktree.
			Worktree:     req.Worktree,
			WorktreeBase: req.WorktreeBase,
			// 2026-10-21 design 一: the name/branch overrides travel with the job so the
			// executor renders the same layout from ITS project templates.
			WorktreeName:   req.WorktreeName,
			WorktreeBranch: req.WorktreeBranch,
			// `ref:` (design 一·3): the executor checks out this commit in a temporary
			// worktree, fetching it first when its own checkout lacks it.
			Ref: req.Ref,
			// The job's extra env (a workflow step / fan env) rides Dispatch.Env; the
			// worker layers it onto its own gofer metadata env.
			Env: req.Env,
			// The ADMITTED deadline (clamped above), not the raw request: the server
			// owns admission, so a remote execution must not re-admit 2h for a request
			// this server already cut to 1h (bd h-aii-s9ck).
			TimeoutSec:     timeoutSec,
			Session:        req.Session,
			IdleTimeoutSec: req.IdleTimeoutSec,
			MaxSessionSec:  req.MaxSessionSec,
			Messenger:      req.Messenger,
			Interactive:    req.Interactive,
			Cols:           req.Cols,
			Rows:           req.Rows,
			// Path B priming rides to the executor, whose pty starts the resumed
			// TUI: the text and the quiet window travel together (see above).
			InitialInput:        runReq.InitialInput,
			InitialInputQuietMs: runReq.InitialInputQuietMs,
			Stdin:               req.Stdin,
			ResumeSourceAgent:   req.ResumeSourceAgent,
			SystemPrompt:        req.SystemPrompt,
			// ACP-01 S2: a continuation reaches a remote executor with its session and
			// lineage so the worker's local job resolves the same session/load. Empty for
			// a plain job (a bare session_id is not a resume).
			SessionID:         req.SessionID,
			ResumedFrom:       req.ResumedFrom,
			AutoResumeAttempt: req.AutoResumeAttempt,
			// bd h-aii-0ql3: read-only rides to the executor, whose own admission +
			// agent config decide how (or whether) it can be honoured.
			ReadOnly: req.ReadOnly,
			// CFG-16: `--no-rules` also turns off the tools section the EXECUTING
			// machine renders, so the switch travels with the job.
			NoRules: req.NoRules,
			// P2: the resolved target worker (explicit req.WorkerID or label-selected
			// in selectTargetWorker). Empty for peer-http and for worker jobs relying
			// on the runner's configured default (D4).
			WorkerID: req.WorkerID,
			// SUP-01 P2: the resolved verify step (argv + its own deadline) rides to
			// the executor, which owns the checkout being verified. The hub refuses a
			// dispatch to a worker that cannot honour it (protocol < 8).
			Verify:           req.Verify,
			VerifyTimeoutSec: req.VerifyTimeoutSec,
			// XFER-01 X2: the file steps run on the EXECUTING machine (it owns the cwd
			// they act on), so the staged upload ids and the collect globs travel with
			// the dispatch. Empty for a job that carries no files — byte-identical to a
			// pre-X2 forward.
			Uploads: xferUploadsToRunner(req.Uploads),
			Collect: req.Collect,
			// JOB-10: the resolved skills travel with the dispatch so the worker mounts
			// exactly what this machine decided (the files themselves ride the uploads
			// above, with Base=result_dir).
			Skills: req.Skills,
			// MCP-06: the bound MCP servers' declarations (secret variable NAMES only).
			MCP: mcpWireSpecs(req.MCP, req.MCPSpecs),
			// JOB-11: the resolved same-directory lock decision travels to the machine
			// that owns the checkout — its job.Service is the one that can actually hold
			// the lock (this process only knows a relative cwd for a remote job).
			ExclusiveDir: req.ExclusiveDir,
			// AUTO-05: so does the output-stall window: the executor owns the process
			// whose silence is being watched.
			StallTimeoutSec: req.StallTimeoutSec,
			// JOB-11 (F12): and so does the directory-lock wait cap — the lock is held
			// on that machine, so the policy this hub resolved is what applies there.
			DirWaitMaxSec: req.DirWaitMaxSec,
			EnvDenyExtra:  req.EnvDenyExtra,
		}
		// gofer-e2x7: a worker mirrors its acp.jsonl into this host job's result dir.
		runReq.ResultDir = resultDir
		// Bridge the peer's running-job interactions (P9) onto this host job.
		runReq.Interactions = remoteInteractionSink{s: s, jobID: jobID}
		// SEC-01: mint the credential the EXECUTING worker will inject as
		// GOFER_JOB_TOKEN. It travels on the Forward (which the ws-worker runner projects
		// onto the dispatch frame); a worker below the credential protocol never receives
		// it, runs the job without one and gets job.credential_skipped recorded against
		// this host job. Minted only for the ws-worker transport — peer-http posts the
		// Forward to another gofer, and a credential minted here must not leave this hub
		// (Forward.JobToken is deliberately not serialisable), so a peer job simply runs
		// without one.
		if isWorkerRunner(cfg, req.Runner) {
			kind, planID := jobCredentialIdentity(req)
			if tok, terr := s.issueJobToken(jobID, kind, planID, timeout); terr != nil {
				slog.Warn("job credential: issue for dispatch", "job_id", jobID, "err", terr)
			} else {
				runReq.Forward.JobToken = tok
			}
		}
	} else {
		// Resolve the agent from the SAME cfg snapshot the request was validated
		// against (BuildFrom/ResolveAgent, not the agent registry): going through the
		// registry re-reads its own config pointer, so a Reload landing between
		// validate and here produced a job that passed the OLD policy but executed the
		// NEW agent's command (mixed config state). ac is resolved once and reused by
		// every argv-injection below for the same reason.
		// MCP-06: prepare the bound MCP servers for this agent (design §3.4) before the
		// argv is built — a claude-json agent's fragment names the config file written
		// here, and a continuation's prebuilt argv has a slot for the fragment.
		mcpInj, mcpFragment, merr := s.prepareMCP(cfg, req, resultDir)
		if merr != nil {
			return JobResult{}, merr
		}
		buildCmd := req.Cmd
		if req.ResumeSourceAgent != "" {
			buildCmd = agent.SpliceMCPArgs(req.Cmd, agent.Render(mcpFragment, agent.Vars{MCPConfigFile: mcpInj.ConfigFile}))
		}
		resolved, berr := agent.BuildFrom(cfg, req.Agent, prompt, buildCmd, agent.Vars{
			Cwd:       workDir,
			JobID:     jobID,
			ResultDir: resultDir,
		}, agent.BuildOptions{AllowEmptyPrompt: req.Interactive || req.Session, Interactive: req.Interactive, AgentArgs: req.AgentArgs, ReadOnly: req.ReadOnly, Model: req.Model, FromSession: req.FromSession, Budget: buildBudget(req),
			MCPArgs: mcpFragment, MCPConfigFile: mcpInj.ConfigFile})
		if berr != nil {
			return JobResult{}, berr
		}
		ac, acOK := agent.ResolveAgent(cfg, req.Agent)
		runReq.Command = resolved.Command
		runReq.Args = resolved.Args
		if acOK && req.Interactive {
			runReq.ExitKeys = append([]string(nil), ac.ExitKeys...)
			if ac.ExitGraceSec != nil {
				runReq.ExitGraceSec = *ac.ExitGraceSec
			}
			if len(ac.SessionInject) == 0 {
				runReq.SessionStoreGlob = ac.SessionStoreGlob
				runReq.SessionStoreIDRegex = ac.SessionStoreIDRegex
			}
		}
		// ACP-01: an acp-agent's local execution is the ACP client runner, not a
		// plain child process — the prompt travels over the protocol, so the local
		// runner would exec an agent that never receives it. The runner is selected
		// by key (like the pty runner) but is REQUIRED: silently falling back to the
		// local runner would run a promptless agent process.
		if acOK && ac.Type == agent.TypeACPAgent {
			ar := s.runner(builtinACPRunner)
			if ar == nil {
				return JobResult{}, fmt.Errorf("%w: agent %q (acp-agent) requires the acp runner", ErrInvalidRequest, req.Agent)
			}
			run = ar
			runReq.ACP = acpRequest(cfg, ac, req, prompt, resultDir)
			runReq.ACP.MCPServers = mergeACPMCP(runReq.ACP.MCPServers, mcpInj.ACP)
			// GATE-01: the approval gate asks THROUGH this job's interaction surface
			// (the card lands in web/CLI/MCP exactly like any other interaction, and a
			// worker's local job mirrors it up to the hub).
			runReq.Approvals = approvalSink{s: s, jobID: jobID}
		}
		// 模式①注入(session-capture §5.1): the resolved agent has a SessionInject
		// template (claude --session-id) → generate a uuid now and append the rendered
		// inject args to argv, so gofer knows the session id without parsing output.
		if acOK && len(ac.SessionInject) > 0 {
			sessionID = newUUID()
			runReq.Args = append(runReq.Args, agent.Render(ac.SessionInject, agent.Vars{SessionID: sessionID})...)
		}
		// E35 system_inject: agent configured a SystemInject template AND the request
		// carries a system prompt (set directly or filled from a role preset) → render
		// {{system_prompt}} and append to argv (e.g. claude --append-system-prompt <p>).
		// Independent of SessionInject (distinct flags, order-free); argv stays
		// element-wise (SR403, no shell join).
		if acOK && len(ac.SystemInject) > 0 && req.SystemPrompt != "" {
			runReq.Args = append(runReq.Args, agent.Render(ac.SystemInject, agent.Vars{SystemPrompt: req.SystemPrompt})...)
		}
		// gap①(issue 7z6j) codex MCP env 注入: for a codex agent carrying job/role env
		// (e.g. --role supervisor → GOFER_AGENT_ROLE=supervisor), ALSO push that env onto
		// the gofer MCP child codex spawns via `-c mcp_servers.<name>.env.<KEY>=<VALUE>`.
		// codex starts MCP stdio servers with a sanitised env that does NOT inherit the
		// codex process env (runReq.Env below), so the MCP child would otherwise never see
		// role.env. McpEnvInjectArgs no-ops for non-codex agents and for empty env, so plain
		// codex jobs and other agents are unaffected. Only req.Env (job/role env) is routed
		// here — the MCP token stays in codex config.toml and never enters the rendered
		// command (SR403). Distinct from SystemInject (own override path); argv stays
		// element-wise (SR403, no shell join).
		if acOK {
			runReq.Args = append(runReq.Args, agent.McpEnvInjectArgs(ac, req.Env)...)
		}
		// 嵌套仓（add_dir_args）：agent 声明了该片段、且 job 的 cwd 落在与项目根不同的 git
		// 仓里时，把项目根作为 {{dir}} 追加（判据见 adddir.go）。只有本机执行在这里构建 argv，
		// 远端 job 的 argv 由执行侧自己的 Submit 渲染，拿到的就是执行机上的项目根。
		if acOK {
			runReq.Args = append(runReq.Args, addDirArgs(cfg, proj, ac, workDir)...)
		}
		secretMap, err := LoadEnvFilesMap(req.EnvFiles, cfg, proj)
		if err != nil {
			return JobResult{}, err
		}
		// Layer env_files under agent-config env and per-job env. The env_files
		// values only enter runReq.Env; they are never copied back into req.Env, so
		// request_json/API responses keep only the non-sensitive file declarations.
		// Precedence low→high:
		// os.Environ < env_files < agent.env < inherited resume env < job.env < gofer metadata.
		// Inject gofer-owned job metadata env so ANY job type (exec or cli-agent)
		// can locate its result dir / cwd / id. exec argv is executed verbatim
		// (no {{result_dir}} templating, unlike cli-agent args) — env is the only
		// channel an exec wrapper has to find <result_dir> for writing E1 artifacts
		// / E6 result.json. Set on the worker/peer side too (they run this same
		// local branch), so remote exec jobs get the executor-local paths.
		// gofer-s2er: a job with a limit also learns it here (GOFER_JOB_TIMEOUT_SEC /
		// GOFER_JOB_DEADLINE, from the limit resolved above) — the executing machine's own
		// start time is the base, so a dispatched job's variables are computed HERE, on the
		// machine that runs it, and not on the hub that wrote its prompt line.
		// gofer-drlm: execute re-derives both variables once the last queue is behind the job
		// (that is the start the agent should count from); the values set here are only the
		// pre-run shape of the same limit.
		// A continuation carrier (ResumeSourceAgent) layers the SOURCE agent's env and the
		// source job's own env between agent.env and the request's explicit env, so the
		// resumed CLI sees the same HOME / model / credentials as the first turn. They
		// are resolved here and only enter runReq.Env (resume_env.go).
		agentEnv := util.MergeEnv(resolved.Env, resumeAgentEnv(cfg, req))
		inherited := s.resumeInheritedEnv(req)
		runReq.Env = goferJobEnv(util.MergeEnv(util.MergeEnv(util.MergeEnv(secretMap, agentEnv), inherited), req.Env), jobID, workDir, resultDir, limit)
		// MCP-06: secret values for the bound MCP servers live only in the agent
		// process's environment (never in argv, request_json or a file).
		if len(mcpInj.Env) > 0 {
			runReq.Env = util.EnvWith(runReq.Env, mcpInj.Env)
		}
		// JOB-06②: the granted secrets, under the names the project's jobs expect.
		if len(secretEnv) > 0 {
			runReq.Env = util.EnvWith(runReq.Env, secretEnv)
		}
		if len(req.MCP) > 0 {
			s.recordEvent(jobID, EventJobMCPInjected, map[string]any{"servers": req.MCP, "notes": mcpInj.Notes})
		}
		// SEC-01: this job's environment-privacy decision. The denylist strips inherited
		// credentials from the child's environment (each runner applies it through
		// util.EnvironWithout) and the PROJECT's allow list re-admits the ones an operator
		// deliberately keeps. Resolved here, from the same config snapshot as the rest of
		// admission, and carried on the request so the machine that actually spawns the
		// child (a worker's own job.Service) applies exactly this decision.
		runReq.EnvDeny = append(effectiveJobEnvDeny(cfg), req.EnvDenyExtra...)
		runReq.EnvAllow = proj.JobEnvAllow
		s.recordEnvAllowedEvents(jobID, runReq.EnvAllow, runReq.EnvDeny)
		// SEC-01: and the job's OWN credential, injected so the job can still reach this
		// hub now that the inherited server token is gone. A hub-local job is minted one
		// here — before execute starts — while a dispatched job already carries the hub's
		// token and must not mint a second, unrevokable one (CredentialExternal).
		// W2b: the steward's marker is durable (steward_jobs) BEFORE its credential is minted,
		// so a recovered resident session finds it again.
		if req.Steward {
			if merr := s.meta.MarkStewardJob(jobID); merr != nil {
				return JobResult{}, merr
			}
		}
		runReq.Env = util.EnvWith(runReq.Env, s.jobCredentialEnv(cfg, jobID, req, sessionCredentialTTL(req, timeout)))
		// A bare `gofer` program (the tracker-sync job, `job run -- gofer …`) means the gofer
		// running this job. The program is resolved against THIS process's PATH, not the
		// child's, and Windows refuses a hit found relative to the job's cwd.
		runReq.Command = selfProgram(runReq.Command, runReq.Env)
		if req.Steward {
			if serr := applyStewardRun(&runReq); serr != nil {
				return JobResult{}, fmt.Errorf("%w: %v", ErrInvalidRequest, serr)
			}
		}
		// MCP-05 阶段 B: a LEADER job tells its agent process (and the gofer MCP child
		// that process spawns) which plan it leads, so the MCP surface can narrow itself
		// to the leader tool whitelist. Server-set from the request's marker (which is
		// never on the wire), and only for a leader job: an ordinary job carries no such
		// variable at all.
		if req.LeaderOfPlan != "" {
			runReq.Env = util.EnvWith(runReq.Env, map[string]string{
				goferLeaderPlanEnv: req.LeaderOfPlan,
			})
		}
		// JOB-10: the mount root, so an agent (or a wrapper script) can find the
		// skills without parsing the prompt list. Set only when something is mounted —
		// an always-present var would point at a directory that does not exist.
		if len(req.Skills) > 0 {
			runReq.Env = util.EnvWith(runReq.Env, map[string]string{
				goferSkillsDirEnv: filepath.Join(resultDir, skillsDirName),
			})
		}
		// WT-01: the job also learns WHERE its worktree/branch/base are. Applied after
		// goferJobEnv so a user-supplied Env key can never shadow them (same rule as
		// the gofer metadata vars).
		if wt != nil {
			runReq.Env = wt.worktreeEnv(runReq.Env)
		}
	}
	// An explicit req.SessionID (resume path, P2) wins over auto-injection and is
	// honoured for both local and remote branches: the job binds to that exact
	// session id and capture (T1.4) is suppressed (entry.result.SessionID non-empty).
	if req.SessionID != "" {
		sessionID = req.SessionID
	}

	// WT-01: flat the managed worktree (nil for a plain job) onto the result row —
	// the deliverable location is known from the moment the worktree exists, so even a
	// job that dies before its terminal capture advertises where its branch lives.
	var wtPath, wtBranch, wtBase string
	var wtLinks []string
	if wt != nil {
		wtPath, wtBranch, wtBase = wt.Path, wt.Branch, wt.BaseSHA
		wtLinks = wt.Links
	}
	// SUP-01 P3: whether the row should carry a requested_agent — the caller's own
	// agent when this submit substituted it, or the chain root's when this job is a
	// takeover. A plain job keeps it empty ("ran exactly what was asked").
	requestedAgent := req.RequestedAgent
	if substitutedFrom != "" {
		requestedAgent = substitutedFrom
	}

	now := s.nowFn().Unix()
	startedAt := now
	if remote {
		// Remote execution has not started when the host publishes the queued row;
		// the worker/peer supplies the real timestamp after its own queue clears.
		startedAt = 0
	}
	// ResumeSourceAgent stays an internal admission marker. Only an exec carrier
	// needs a separate persisted display identity; ACP runs under its own agent.
	resumeDisplayAgent := ""
	if req.Agent == agent.ExecAgentKey && req.ResumedFrom != "" {
		resumeDisplayAgent = req.ResumeSourceAgent
	}
	var sessionCommands chan runner.SessionCommand
	if req.Session && !remote {
		sessionCommands = make(chan runner.SessionCommand, 1)
	}
	var heldLockPaths []string
	if req.Session && dirExclusive && wt == nil {
		heldLockPaths = append([]string(nil), req.ResolvedLockPaths...)
		if len(heldLockPaths) == 0 {
			heldLockPaths = []string{workDir}
		}
	}
	entry := &jobEntry{
		store:           st,
		done:            make(chan struct{}),
		wt:              wt,
		baselineSHA:     req.BaselineSHA,
		secretValues:    secretValues,
		sessionCommands: sessionCommands,
		result: JobResult{
			ID:          jobID,
			ProjectKey:  req.ProjectKey,
			Agent:       req.Agent,
			ResumeAgent: resumeDisplayAgent,
			Runner:      req.Runner,
			Messenger:   req.MessengerMeta,
			Interactive: req.Interactive,
			Model:       req.Model,
			FromSession: req.FromSession,
			Acceptance:  req.Acceptance,
			Scope:       req.Scope,
			// gofer-3nxa.2: decided by injectPromptSections above.
			KnowledgeCapture: req.KnowledgeCapture,
			Budget:           req.Budget,
			// bd h-aii-0ql3：只读是 job 的持久属性（jobs.read_only），resume 继承、show/web 可见。
			ReadOnly: req.ReadOnly,
			// JOB-10: the decided skill bindings (jobs.skills_json) — the row answers
			// "which rules did this run have" without re-reading a config that may have
			// changed since.
			Skills: req.Skills,
			// MCP-06: the bound MCP server names (jobs.mcp_json).
			MCP: req.MCP,
			// JOB-06①: the injected rules with the sha256 of each text (jobs.rules_json),
			// so the row answers "which discipline — and which version of it — did this
			// run carry?" after the library changed. The prompt itself (request_json)
			// says what the agent actually read.
			Rules: req.ruleRefs,
			// JOB-11：同 cwd 独占决策（jobs.dir_exclusive）——提交期定死，show/web 与
			// 事后排查据此回答"这次运行当初是否（被允许）独占这棵工作树"。
			DirExclusive:  dirExclusive,
			LockPaths:     req.LockPaths,
			HeldLockPaths: heldLockPaths,
			// GATE-01 S3：人工验收同样是 job 的持久属性（jobs.require_review），决定
			// finish 是落 done 还是 needs_review，resume 继承、show/web 可见。
			RequireReview: req.Review,
			// MCP-05 阶段 B：leader job 的 plan 标记（jobs.leader_of_plan）。服务端盖章，
			// 客户端不可设（json:"-"），重启后仍在行上——唤醒规则与验收闸门都读它。
			LeaderOfPlan: req.LeaderOfPlan,
			// bd h-aii-s9ck: the deadline this job actually runs under, plus the
			// clamp report (requested > ceiling) so it is never a silent truncation.
			TimeoutSec:          timeoutSec,
			RequestedTimeoutSec: req.TimeoutSec,
			TimeoutClamped:      timeoutClamped,
			TimeoutLimitSource:  limitSource,
			TimeoutLimitKey:     config.TimeoutLimitKey(limitSource, req.ProjectKey),
			Session:             req.Session,
			IdleTimeoutSec:      req.IdleTimeoutSec,
			MaxSessionSec:       req.MaxSessionSec,
			Title:               req.Title,
			WorkerID:            req.WorkerID,
			Status:              StatusQueued,
			Cwd:                 workDir,
			ResultDir:           resultDir,
			StartedAt:           startedAt,
			// gofer-drlm：这一行被接受、开始排队等槽位/目录锁的时刻；StartedAt 是真正开跑的时刻，
			// 两者之差就是「排队 X」（排队时间不计入执行时限，F12）。
			QueuedAt:        now,
			RequestJSON:     string(reqJSON),
			CallerID:        req.CallerID,
			OriginCaller:    req.OriginCaller,
			SourceSessionID: req.SourceSessionID,
			// gofer-3db4: the decided end-notification (request_json carries it; the
			// snapshot fields are what the terminal notify reads).
			Notify:        req.Notify,
			NotifySession: req.NotifySession,
			RequestID:     req.RequestID,
			Tags:          req.Tags,
			// 工作流(job 链)：引擎起 step-job 时已在 req 上设好；普通 job 为 ""/0。
			WorkflowID: req.WorkflowID,
			StepIndex:  req.StepIndex,
			Attempt:    req.Attempt,
			FanIndex:   req.FanIndex, // P2: fan-out 并行序号（非 fan-out 为 0）
			// session 捕获：注入式(claude)立即知 id；显式 req.SessionID(resume)优先；
			// 捕获式(codex)此处为空，终态由 captureOutcomes 填充(T1.4)。
			SessionID: sessionID,
			// 提交来源（provenance）：渠道(cli/web/mcp/im) + 来源主机/IP，入口已盖章在 req 上。
			Channel: req.Channel,
			Client:  req.Client,
			// 套娃防护（supervisor-routing P2.2）：记录该 job 的角色预设名（如 supervisor），
			// 供监督路由器识别"sup 自身产生的 interaction"，对其永不自动答/回投 sup。
			Role:   req.Role,
			PlanID: req.PlanID,
			// SUP-01 C：该 job 挂接的 checklist 项（空=不挂）。落 jobs.todo_id，终态由
			// linkTodoOutcome 把结果写回该 todo。
			TodoID:      req.TodoID,
			IssueID:     req.IssueID,
			TrackerID:   req.TrackerID,
			TodoForeign: req.TodoForeign,
			// 血缘（P5）：ResumeJob/RebuildJob 内部盖在 req 上（源 job id）；普通 job 为空。
			// json:"-" 不影响此 Go 赋值——落 jobs.source_job_id（血缘的真源，不进 request_json）。
			SourceJobID: req.SourceJobID,
			// agent 故障转移（SUP-01 P3）：调用方原本要求的 agent（提交期改派才有值，并沿
			// 转移链继承）、本条 job 从哪条 job 接管（FellBackFrom）、已冻结的候选计划。
			// fell_back_to 在提交接管 job 成功后由 fallBack 回填。
			RequestedAgent:    requestedAgent,
			FellBackFrom:      req.FellBackFrom,
			Fallback:          fallback,
			ResumedFrom:       req.ResumedFrom,
			AutoResumeAttempt: req.AutoResumeAttempt,
			// WT-01：受管 worktree 的交付物位置与基线（终态时 captureOutcomes 再补
			// head sha / commits_ahead / 两段 diff）。远端 job 由执行机经 Outcome 回传。
			WorktreePath:    wtPath,
			WorktreeBranch:  wtBranch,
			WorktreeLinks:   wtLinks,
			WorktreeBaseSHA: wtBase,
		},
	}
	// gofer-9b1b: an approved held job keeps its hold record (the decision, the
	// digest) on the row through every later persist.
	if req.held != nil {
		state := req.held.HoldState
		entry.result.Hold = &state
		entry.result.holdSecret = &holdSecret{Digest: req.held.Digest}
	}
	if runReq.ACP != nil && sessionCommands != nil {
		s.configureResidentACP(entry, runReq.ACP, timeoutSec)
	}
	s.mu.Lock()
	s.jobs[jobID] = entry
	s.mu.Unlock()
	// Register the authenticated source before the job is persisted or launched.
	// This uses the existing watch owner and remains idempotent on request retries.
	if req.SourceSessionID != "" {
		if _, err := s.meta.AddSessionJobWatch(req.SourceSessionID, jobID); err != nil {
			s.mu.Lock()
			delete(s.jobs, jobID)
			s.mu.Unlock()
			dropDir()
			return JobResult{}, fmt.Errorf("register source session watch: %w", err)
		}
	}
	// gofer-3db4: the notify target's watch is the pending record the end-notification
	// claims. Added here (not at the end) for the same reason as the source watch: the
	// row must exist before the job can end.
	if err := s.watchNotifyTarget(req, jobID); err != nil {
		s.dropNotifyTargetWatch(req, jobID)
		if req.SourceSessionID != "" {
			_, _ = s.meta.RemoveSessionJobWatch(req.SourceSessionID, jobID)
		}
		s.mu.Lock()
		delete(s.jobs, jobID)
		s.mu.Unlock()
		dropDir()
		return JobResult{}, err
	}

	// Record the initial (queued) snapshot in the metadata store. Capture the
	// error so the C5 concurrent-submit race can be recovered: if a competing
	// submit with the SAME request_id won the unique index, this insert returns
	// ErrRequestIDConflict and we hand back the winner instead of launching a
	// duplicate job. For the no-request_id case the write stays best-effort
	// (legacy behaviour: ignore the error, the entry lives in memory).
	tm.mark("build")
	persistErr := s.persist(entry.snapshot())
	tm.mark("persist")
	if req.RequestID != "" && errors.Is(persistErr, jobstore.ErrRequestIDConflict) {
		if req.SourceSessionID != "" {
			_, _ = s.meta.RemoveSessionJobWatch(req.SourceSessionID, jobID)
		}
		s.dropNotifyTargetWatch(req, jobID)
		// Lost the race: drop our just-created entry + dir and return the winner.
		s.mu.Lock()
		delete(s.jobs, jobID)
		s.mu.Unlock()
		dropDir()
		if rec, ok, gerr := s.meta.GetJobByRequestID(req.RequestID); gerr != nil {
			return JobResult{}, gerr
		} else if ok {
			return s.reuseRequestJob(rec, req)
		}
		// The winner's row is unexpectedly absent (should not happen, since the
		// conflict means a row with this request_id exists); surface the conflict.
		return JobResult{}, persistErr
	}
	if persistErr != nil && req.SourceSessionID != "" {
		_, _ = s.meta.RemoveSessionJobWatch(req.SourceSessionID, jobID)
		s.dropNotifyTargetWatch(req, jobID)
		s.mu.Lock()
		delete(s.jobs, jobID)
		s.mu.Unlock()
		dropDir()
		return JobResult{}, fmt.Errorf("persist watched job: %w", persistErr)
	}

	// SUP-01 P3: the submit-time substitution is recorded once the job is a fact, and
	// the row's requested_agent keeps the caller's original choice visible.
	if substitutedFrom != "" {
		s.recordEvent(jobID, EventJobAgentSubstituted, map[string]any{
			"from": substitutedFrom, "to": req.Agent, "reason": "degraded",
		})
	}
	// E13: the queued snapshot is durably persisted (or best-effort for the
	// no-request_id case) — record the lifecycle event now that submission is a
	// fact. Detail carries the identity/routing fields (no secrets, SR403). An approved
	// held job recorded its job.submitted (and linked its todo) when it was held.
	if req.heldJobID == "" {
		s.recordEvent(jobID, EventJobSubmitted, map[string]any{
			"project":   req.ProjectKey,
			"agent":     req.Agent,
			"runner":    req.Runner,
			"caller_id": req.CallerID,
			"tags":      req.Tags,
		})
		s.recordDecisions(jobID, req.decisions)
		// gofer-3db4: an internal producer asked to be told when this job ends and its
		// target could not be addressed — the work runs anyway, and this event is where
		// the missing notice is explained instead of staying a silent mystery.
		if notifyDrop != nil {
			s.recordEvent(jobID, EventNotifyDropped, map[string]any{
				"reason": notifyDrop.Reason,
				"target": notifyDrop.Target,
			})
		}
	}
	if len(req.Secrets) > 0 {
		s.recordEvent(jobID, EventJobSecretsGranted, map[string]any{"names": req.Secrets})
	}
	if req.heldJobID == "" {
		// SUP-01 C: the job is a fact, so its checklist item can follow it (best-effort —
		// a job must never fail to start because the todo could not be updated).
		if req.TodoID != "" && !req.TodoForeign {
			s.linkTodoSubmit(req.TodoID, jobID)
		}
	}
	// E13: a remote runner (peer-http / ws-worker) forwards the job to a remote
	// executor — record the dispatch with the resolved target.
	if remote {
		s.recordEvent(jobID, EventJobDispatched, map[string]any{
			"runner":    req.Runner,
			"worker_id": req.WorkerID,
		})
	}

	// E16: count the submission (nil-safe). Labels are the bounded routing
	// identity (caller/project/agent/runner); no high-cardinality fields. A held job
	// was counted when it was held.
	if s.metrics != nil && req.heldJobID == "" {
		s.metrics.JobSubmitted(req.CallerID, req.ProjectKey, req.Agent, req.Runner)
	}

	sem := s.semaphore(req.ProjectKey, proj.MaxConcurrentJobs)
	// E17: per-caller concurrency slot (design §7.2). Resolved from the SAME cfg
	// snapshot (caller override > governance default > unlimited). nil when the
	// caller has no cap or no id — then execute does not gate on it.
	callerSem := s.callerSemaphore(req.CallerID, cfg.Server.CallerConcurrencyLimit(req.CallerID))
	// JOB-11: per-agent concurrency slot (agents.<key>.max_concurrent). Resolved from
	// the SAME snapshot as the request that was validated, and keyed on the RESOLVED
	// agent (a role/template may have filled it above).
	agentSem := s.agentSemaphore(req.Agent, cfg.Agents[req.Agent].MaxConcurrent)
	go s.execute(entry, run, execGates{
		project:   sem,
		caller:    callerSem,
		agent:     agentSem,
		exclusive: req.ExclusiveDir != nil && *req.ExclusiveDir,
		stall:     stallSec,
		dirWait:   dirWaitSec,
	}, runReq, timeout)
	tm.mark("dispatch")

	snapshot := entry.snapshot()
	tm.mark("snapshot")
	return snapshot, nil
}

// interactiveInitialInput turns an interactive job's prompt into the first
// submitted terminal message. The pty runner writes the body and carriage
// return separately through writeSubmitted, so paste-detecting TUIs submit it
// as a message instead of inserting a newline into the composer.
func interactiveInitialInput(prompt string) string {
	if strings.TrimSpace(prompt) == "" {
		return ""
	}
	if strings.HasSuffix(prompt, "\r") {
		return prompt
	}
	return prompt + "\r"
}

// resolveDirExclusive decides whether a job takes the exclusive same-directory lock
// (JOB-11, design §二 + decision 3) and STAMPS the decision onto the request, so
// request_json, the persisted row, the peer/worker forward and the executing machine
// all carry ONE value instead of each re-deriving one from its own config:
//
//   - server.dir_lock off → every job shares (the operator's escape hatch);
//   - an explicit --exclusive-dir / --shared-dir wins either way;
//   - otherwise a WRITABLE agent job is exclusive: a cli-agent/acp-agent that is not
//     read-only and not interactive. exec jobs are shared by default (git log, go
//     test, a巡检 must not queue behind an agent), and so are read-only ones;
//   - an agent this server cannot resolve (peer-only) is NOT assumed exclusive: the
//     decision then belongs to the machine that runs it, which can resolve it.
func resolveDirExclusive(cfg *config.Config, req *JobRequest) bool {
	switch {
	case !cfg.EffectiveDirLock():
		return false
	case req.ExclusiveDir != nil:
		return *req.ExclusiveDir
	case len(req.LockPaths) > 0:
		// Declaring --lock is an explicit request for the lock, whatever the agent
		// type: an exec job with --lock must not silently run unlocked.
		return true
	}
	ac, ok := agent.ResolveAgent(cfg, req.Agent)
	if !ok {
		return false
	}
	return ac.Type != agent.TypeExec && !req.ReadOnly && !req.Interactive
}

// TitleMaxRunes caps an explicit job title (submit and later edits). It is
// generous on purpose: before titles became editable any length was accepted.
const TitleMaxRunes = 200

// titleMaxRunes caps an auto-extracted job title (defaultJobTitle).
const titleMaxRunes = 32

// isAgentJob reports whether the named agent is one of the long-running AI
// agents — a cli-agent (claude/codex style session) or an acp-agent (an ACP
// server driven over stdio). Both run whole model turns and share the larger
// default timeout; an exec job is a plain command and keeps the short one.
// Unknown agents (e.g. peer-only agents on a remote submit the host cannot
// resolve) are not AI agents.
func isAgentJob(cfg *config.Config, name string) bool {
	a, ok := cfg.Agents[name]
	return ok && (a.Type == agent.TypeCLIAgent || a.Type == agent.TypeACPAgent)
}

// validateSourceSession proves the submitting session from the authenticated caller
// context and the already-resolved job execution context. It never treats the
// agent-target SessionID as caller identity and runs before result/job writes.
func (s *Service) validateSourceSession(req JobRequest, workDir string, remote bool) error {
	sid := strings.TrimSpace(req.SourceSessionID)
	if sid == "" {
		return nil
	}
	if strings.TrimSpace(req.CallerID) == "" {
		return fmt.Errorf("%w: source_session_id requires an authenticated caller", ErrInvalidRequest)
	}
	session, ok, err := s.meta.GetAgentSession(sid)
	if err != nil {
		return fmt.Errorf("load source session: %w", err)
	}
	if !ok {
		// The session is not registered HERE. That is the same fact as "it lives on
		// another machine" for the caller's purposes, so it is classified as
		// ErrSourceSessionElsewhere: a plan dispatch then drops the claim and still runs
		// the item (a supervisor whose session was cleaned up must not block the chain),
		// while an explicit submit is refused with the same 400 as before. Ownership is
		// NOT part of this branch — a session that exists but is somebody else's is a
		// trust failure, never "elsewhere".
		return fmt.Errorf("%w: %w: source session is not registered on this server", ErrInvalidRequest, ErrSourceSessionElsewhere)
	}
	if session.CallerID == "" || session.CallerID != req.CallerID {
		return fmt.Errorf("%w: source session is unknown or not owned by the authenticated caller", ErrInvalidRequest)
	}
	if session.ProjectKey == "" || session.ProjectKey != req.ProjectKey {
		return fmt.Errorf("%w: %w: source session project does not match job project", ErrInvalidRequest, ErrSourceSessionElsewhere)
	}
	if session.Runner == "" || config.NormalizeRunnerName(session.Runner) != config.NormalizeRunnerName(req.Runner) {
		return fmt.Errorf("%w: %w: source session runner does not match job runner", ErrInvalidRequest, ErrSourceSessionElsewhere)
	}
	// A remote runner's resolved absolute cwd belongs to that executor. This server
	// has no trustworthy mapping to compare it with, so a source-bound remote submit
	// is rejected until the existing runner path resolver can prove the same path.
	if remote || req.Worktree || workDir == "" || session.Cwd == "" || !sameExecutionPath(session.Cwd, workDir) {
		return fmt.Errorf("%w: %w: source session cwd does not match effective job cwd", ErrInvalidRequest, ErrSourceSessionElsewhere)
	}
	if req.PlanID != "" {
		plan, found, err := s.meta.GetPlan(req.PlanID)
		if err != nil {
			return fmt.Errorf("load source plan: %w", err)
		}
		if !found {
			return fmt.Errorf("%w: source plan does not exist", ErrInvalidRequest)
		}
		if plan.Owner == "" || plan.Owner != req.CallerID {
			return fmt.Errorf("%w: source plan is not owned by the authenticated caller", ErrInvalidRequest)
		}
		if plan.SupervisorSessionID != "" && plan.SupervisorSessionID != sid {
			return fmt.Errorf("%w: source session does not match the plan supervisor session", ErrInvalidRequest)
		}
	}
	return nil
}

func sameExecutionPath(a, b string) bool {
	aa, errA := filepath.Abs(filepath.Clean(a))
	bb, errB := filepath.Abs(filepath.Clean(b))
	if errA != nil || errB != nil {
		return false
	}
	if os.PathSeparator == '\\' {
		return strings.EqualFold(aa, bb)
	}
	return aa == bb
}

func (s *Service) reuseRequestJob(rec jobstore.JobRecord, req JobRequest) (JobResult, error) {
	if rec.SourceSessionID != req.SourceSessionID || (req.SourceSessionID != "" && rec.CallerID != req.CallerID) {
		return JobResult{}, fmt.Errorf("%w: request_id already belongs to a different source session context", ErrInvalidRequest)
	}
	if req.SourceSessionID != "" {
		if _, err := s.meta.AddSessionJobWatch(req.SourceSessionID, rec.ID); err != nil {
			return JobResult{}, fmt.Errorf("restore source session watch for existing job: %w", err)
		}
	}
	// gofer-3db4: the reused job's notify request was already decided; make sure its
	// pending record exists (a retried POST must not silently lose the notification).
	if err := s.watchNotifyTarget(req, rec.ID); err != nil {
		return JobResult{}, err
	}
	return fromRecord(rec), nil
}

// acpRequest builds the acp runner payload (ACP-01) from an acp-agent's config and
// the request being submitted: the prompt, the result dir the runner writes
// acp.jsonl under, the RESOLVED approval policy of this job (GATE-01: the project's
// approval block tightened by the agent's acp.permission_policy), the MCP servers it
// advertises in session/new, whether the agent's thinking goes to the logs
// (acp.log_thoughts), and — for a continuation — the session to LOAD. A nil acp
// sub-block yields the defaults (auto-allow permissions, no MCP servers, thoughts kept).
//
// prompt is passed in rather than read off req because an acp agent's turn is the
// prompt itself: it has to be the RUNNING text (the JOB-10 skills list in front of the
// caller's own words), not the audit copy request_json keeps.
func acpRequest(cfg *config.Config, ac config.AgentConfig, req JobRequest, prompt, resultDir string) *runner.ACPRequest {
	r := &runner.ACPRequest{
		Prompt:        prompt,
		ResultDir:     resultDir,
		Approval:      cfg.EffectiveApproval(req.ProjectKey, req.Agent),
		LoadSessionID: resumeLoadSessionID(req),
		ModelID:       req.Model,
		LogThoughts:   ac.ACP.LogsThoughts(),
		// F14: read claude's user settings file on the EXECUTING machine. For a
		// dispatched job this resolves against the WORKER's own agent config (the
		// worker builds its own acp request), which is the machine that has the file.
		ClaudeSettingsEnv: ac.ACP.InheritsClaudeSettingsEnv(),
	}
	if ac.ACP == nil {
		return r
	}
	if req.ReadOnly {
		// The id is guaranteed non-empty here: admission refuses a read-only acp job
		// whose agent maps no read-only mode.
		r.ReadOnlyModeID = ac.ACP.Modes["read_only"]
	}
	for _, s := range ac.ACP.MCPServers {
		r.MCPServers = append(r.MCPServers, runner.ACPMCPServer{
			Name:    s.Name,
			Command: s.Command,
			Args:    s.Args,
			Env:     s.Env,
		})
	}
	return r
}

// resumeLoadSessionID returns the session an acp-agent job must LOAD, or "" for a
// fresh session. Only a continuation LOADS: ResumedFrom is stamped by ResumeJob and
// never travels through a client body (json:"-"), so a plain `job run` that carries a
// session_id binds to that id without replaying the agent's history (design §S2).
func resumeLoadSessionID(req JobRequest) string {
	if req.ResumedFrom == "" || req.SessionID == "" {
		return ""
	}
	return req.SessionID
}

func defaultJobTitle(req JobRequest) string {
	if len(req.Cmd) > 0 {
		return trimTitleRunes(strings.Join(req.Cmd, " "))
	}
	// JOB-06①: the prompt opens with gofer's injected rules section, which is the same
	// text for every job on this deployment — titling a job after it would name every
	// row identically and hide the caller's actual ask. The title is the caller's own
	// prompt, so the section is stripped first.
	return trimTitleRunes(stripRulesSection(req.Prompt))
}

func trimTitleRunes(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	r := []rune(s)
	if len(r) > titleMaxRunes {
		r = r[:titleMaxRunes]
	}
	return strings.TrimSpace(string(r))
}

// createJobDir generates a unique job id and creates its result dir, retrying on
// collision with a fresh suffix (plan §9 P4 cross-restart uniqueness).
func (s *Service) createJobDir(st store.Store) (string, error) {
	var lastErr error
	for i := 0; i < jobIDCreateRetries; i++ {
		id := s.genJobID()
		err := st.Ensure(id)
		if err == nil {
			return id, nil
		}
		if errors.Is(err, os.ErrExist) {
			lastErr = err
			continue
		}
		// A real filesystem error (permission, etc.) is not retryable.
		return "", err
	}
	return "", fmt.Errorf("could not allocate unique job id: %w", lastErr)
}

// genJobID returns a timestamp-prefixed id with a random hex suffix, e.g.
// 20060102-150405-1a2b3c4d. The random suffix guarantees uniqueness across
// process restarts (a seconds+in-memory-seq scheme would collide on restart).
func (s *Service) genJobID() string {
	ts := s.nowFn().Format(JobIDLayout)
	return ts + "-" + RandomSuffix()
}

// resolveRole expands a JobRequest.Role preset (E35, design §8.5) IN PLACE: an
// empty Role is a no-op; an unknown Role is rejected with ErrUnknownRole. The
// preset fills ONLY the request fields the caller left empty (explicit input wins),
// so `--role reviewer --agent codex` still runs on codex. ProjectKey/Tags get the
// preset defaults too. The filled values are then validated/allowlisted by the
// normal Submit path (role does not bypass any gate).
func resolveRole(cfg *config.Config, req *JobRequest) error {
	if req.Role == "" {
		return nil
	}
	rc, ok := cfg.Roles[req.Role]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownRole, req.Role)
	}
	if req.Agent == "" {
		req.Agent = rc.Agent
	}
	if req.SystemPrompt == "" {
		req.SystemPrompt = rc.SystemPrompt
	}
	if req.ProjectKey == "" {
		req.ProjectKey = rc.Project
	}
	if len(req.Tags) == 0 {
		req.Tags = rc.Tags
	}
	// role.Env fills env DEFAULTS; an explicit per-job key wins (same precedence as
	// the other role fields above). Merged into the job process env at Submit.
	if len(rc.Env) > 0 {
		req.Env = util.MergeEnv(rc.Env, req.Env)
	}
	return nil
}

// RandomSuffix returns 8 lowercase hex chars from crypto/rand, falling back to a
// nanosecond-derived value if the RNG is unavailable.
func RandomSuffix() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%08x", time.Now().UnixNano()&0xffffffff)
	}
	return hex.EncodeToString(b[:])
}

// newUUID returns a random RFC 4122 version-4 UUID string
// (xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx). claude's --session-id requires a legal
// UUID, so we set the version (4) and variant (10xx) bits explicitly. There is no
// uuid dependency in this工具库; 16 crypto/rand bytes are formatted by hand. On a
// (practically impossible) RNG failure it falls back to a time-derived value so a
// non-empty id is always produced.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Deterministic fallback: seed all 16 bytes from the nanosecond clock so the
		// version/variant fixup below still yields a syntactically valid UUID.
		ns := uint64(time.Now().UnixNano())
		for i := range b {
			b[i] = byte(ns >> (8 * (uint(i) % 8)))
		}
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10xx
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// normalizeTimeout applies the default and clamps to the CONFIGURED ceiling
// (plan §9 P4; bd h-aii-s9ck: the ceiling is no longer hard-coded — it is
// config.Config.EffectiveMaxTimeoutSec for the job's project). Interactive
// sessions are resident terminals: an omitted timeout means no job deadline,
// while an explicit timeout_sec still bounds the session. AI agent jobs —
// cli-agent and acp-agent alike (isAgentJob) — default to
// DefaultAgentTimeoutSec (a model turn runs long); everything else defaults to
// DefaultTimeoutSec. max <= 0 means the caller resolved no ceiling and falls back
// to DefaultMaxTimeoutSec.
//
// The second return reports whether the clamp actually truncated an EXPLICIT
// request (requested > max). A default that happens to exceed the ceiling is not
// a clamp of the request — the caller never asked for that value — so it does not
// raise the flag and does not warn.
func normalizeTimeout(sec int, interactive bool, agentJob bool, max int) (time.Duration, bool) {
	if max <= 0 {
		max = DefaultMaxTimeoutSec
	}
	if interactive && sec <= 0 {
		return 0, false
	}
	requested := sec
	if sec <= 0 {
		if agentJob {
			sec = DefaultAgentTimeoutSec
		} else {
			sec = DefaultTimeoutSec
		}
	}
	clamped := requested > max
	if sec > max {
		sec = max
	}
	return time.Duration(sec) * time.Second, clamped
}

// buildBudget is the budget BuildFrom renders into the agent's argv / env. A resume
// carrier (ResumeSourceAgent set) runs as the exec agent with an argv that already
// carries the soft budget (cliCarrierRequest rendered it), so nothing is rendered again.
func buildBudget(req JobRequest) *Budget {
	if req.ResumeSourceAgent != "" {
		return nil
	}
	return req.Budget
}
