package job

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/store"
)

// Submit validates the request, creates the result dir, persists the request and
// starts the job asynchronously. It returns the initial JobResult (status
// running) once the goroutine is launched. Validation/setup failures return an
// error and no job.
func (s *Service) Submit(req JobRequest) (JobResult, error) {
	// Snapshot the config ONCE for the whole Submit so a concurrent Reload cannot
	// make this single submit observe two different configs (peer classification,
	// validation, result base dir all read the same snapshot).
	cfg := s.config()

	// E35: resolve a role preset BEFORE validate so the role-filled agent/project
	// are still allowlist-checked (and an empty agent does not fail validation
	// first). Explicit request fields always win over the preset's defaults.
	if err := resolveRole(cfg, &req); err != nil {
		return JobResult{}, err
	}

	// A remote runner (peer-http OR ws-worker) forwards the original request to a
	// remote executor that resolves agent/cwd/command with its OWN config. The host
	// therefore skips local agent/cwd resolution for remote jobs (it still validates
	// the project, the agent allowlist and the runner allowlist).
	remote := IsRemoteRunner(cfg, req.Runner)

	proj, err := s.validate(cfg, req, remote)
	if err != nil {
		return JobResult{}, err
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

	// Resolve the target worker for a worker runner when worker_id was not given
	// explicitly (P2: labels → auto-select, else the runner's configured default).
	// Done right after validate so the chosen id rides the Forward + JobResult.
	if err := s.selectTargetWorker(cfg, &req); err != nil {
		return JobResult{}, err
	}

	// C5 idempotency: if this request carries an idempotency key already claimed
	// by an earlier job, reuse it (no new job/dir). The concurrent-submit race
	// (two submits both miss this lookup) is caught below by the unique-index
	// conflict on the first persist.
	if req.RequestID != "" {
		if rec, ok, gerr := s.meta.GetJobByRequestID(req.RequestID); gerr != nil {
			return JobResult{}, gerr
		} else if ok {
			return fromRecord(rec), nil
		}
	}

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

	// Result base dir + a collision-resistant job id; create the dir up front.
	// The host keeps a local result dir even for proxied jobs so its logs (mirrored
	// from the peer) and DB index entry stay queryable.
	base, err := project.ResultBaseDir(cfg, req.ProjectKey, proj)
	if err != nil {
		return JobResult{}, err
	}
	st := s.newStore(base)
	jobID, err := s.createJobDir(st)
	if err != nil {
		return JobResult{}, err
	}
	resultDir := st.Dir(jobID)

	// Marshal the original request for audit / re-submit. It rides along on the
	// entry's result so every persist (queued/running/terminal) carries it into the
	// jobs.request_json column (SP5: replaces the on-disk request.json file).
	reqJSON, err := json.Marshal(req)
	if err != nil {
		return JobResult{}, fmt.Errorf("marshal request: %w", err)
	}

	run := s.runners[req.Runner]
	if run == nil {
		return JobResult{}, fmt.Errorf("runner %q is not available", req.Runner)
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
	if remote {
		runReq.Forward = &runner.Forward{
			ProjectKey: req.ProjectKey,
			Agent:      req.Agent,
			PeerRunner: builtinLocalRunner,
			Prompt:     req.Prompt,
			Cmd:        req.Cmd,
			Cwd:        req.Cwd,
			TimeoutSec: req.TimeoutSec,
			// P2: the resolved target worker (explicit req.WorkerID or label-selected
			// in selectTargetWorker). Empty for peer-http and for worker jobs relying
			// on the runner's configured default (D4).
			WorkerID: req.WorkerID,
		}
		// Bridge the peer's running-job interactions (P9) onto this host job.
		runReq.Interactions = remoteInteractionSink{s: s, jobID: jobID}
	} else {
		resolved, berr := s.agents.Build(req.Agent, req.Prompt, req.Cmd, agent.Vars{
			Cwd:       workDir,
			JobID:     jobID,
			ResultDir: resultDir,
		})
		if berr != nil {
			return JobResult{}, berr
		}
		runReq.Command = resolved.Command
		runReq.Args = resolved.Args
		// 模式①注入(session-capture §5.1): the resolved agent has a SessionInject
		// template (claude --session-id) → generate a uuid now and append the rendered
		// inject args to argv, so gofer knows the session id without parsing output.
		if ac, ok := s.agents.Get(req.Agent); ok && len(ac.SessionInject) > 0 {
			sessionID = newUUID()
			runReq.Args = append(runReq.Args, agent.Render(ac.SessionInject, agent.Vars{SessionID: sessionID})...)
		}
		// E35 system_inject: agent configured a SystemInject template AND the request
		// carries a system prompt (set directly or filled from a role preset) → render
		// {{system_prompt}} and append to argv (e.g. claude --append-system-prompt <p>).
		// Independent of SessionInject (distinct flags, order-free); argv stays
		// element-wise (SR403, no shell join).
		if ac, ok := s.agents.Get(req.Agent); ok && len(ac.SystemInject) > 0 && req.SystemPrompt != "" {
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
		if ac, ok := s.agents.Get(req.Agent); ok {
			runReq.Args = append(runReq.Args, agent.McpEnvInjectArgs(ac, req.Env)...)
		}
		secretMap, err := LoadEnvFilesMap(req.EnvFiles, cfg, proj)
		if err != nil {
			return JobResult{}, err
		}
		// Layer env_files under agent-config env and per-job env. The env_files
		// values only enter runReq.Env; they are never copied back into req.Env, so
		// request_json/API responses keep only the non-sensitive file declarations.
		// Precedence low→high:
		// os.Environ < env_files < agent.env < job.env < gofer metadata.
		// Inject gofer-owned job metadata env so ANY job type (exec or cli-agent)
		// can locate its result dir / cwd / id. exec argv is executed verbatim
		// (no {{result_dir}} templating, unlike cli-agent args) — env is the only
		// channel an exec wrapper has to find <result_dir> for writing E1 artifacts
		// / E6 result.json. Set on the worker/peer side too (they run this same
		// local branch), so remote exec jobs get the executor-local paths.
		runReq.Env = goferJobEnv(mergeEnv(mergeEnv(secretMap, resolved.Env), req.Env), jobID, workDir, resultDir)
	}
	// An explicit req.SessionID (resume path, P2) wins over auto-injection and is
	// honoured for both local and remote branches: the job binds to that exact
	// session id and capture (T1.4) is suppressed (entry.result.SessionID non-empty).
	if req.SessionID != "" {
		sessionID = req.SessionID
	}

	now := s.nowFn().Unix()
	entry := &jobEntry{
		store: st,
		done:  make(chan struct{}),
		result: JobResult{
			ID:          jobID,
			ProjectKey:  req.ProjectKey,
			Agent:       req.Agent,
			Runner:      req.Runner,
			Title:       req.Title,
			WorkerID:    req.WorkerID,
			Status:      StatusQueued,
			Cwd:         workDir,
			ResultDir:   resultDir,
			StartedAt:   now,
			RequestJSON: string(reqJSON),
			CallerID:    req.CallerID,
			RequestID:   req.RequestID,
			Tags:        req.Tags,
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
			// 监督分层升级路由（supervisor-routing P1.1）：owner agent_id + 可选 job 级覆盖，
			// 入口（MCP 自注册/显式入参）已盖章在 req 上；普通入口为空。
			OriginAgent: req.OriginAgent,
			EscalateTo:  req.EscalateTo,
			// 套娃防护（supervisor-routing P2.2）：记录该 job 的角色预设名（如 supervisor），
			// 供监督路由器识别"sup 自身产生的 interaction"，对其永不自动答/回投 sup。
			Role: req.Role,
		},
	}
	s.mu.Lock()
	s.jobs[jobID] = entry
	s.mu.Unlock()

	// Record the initial (queued) snapshot in the metadata store. Capture the
	// error so the C5 concurrent-submit race can be recovered: if a competing
	// submit with the SAME request_id won the unique index, this insert returns
	// ErrRequestIDConflict and we hand back the winner instead of launching a
	// duplicate job. For the no-request_id case the write stays best-effort
	// (legacy behaviour: ignore the error, the entry lives in memory).
	persistErr := s.persist(entry.snapshot())
	if req.RequestID != "" && errors.Is(persistErr, jobstore.ErrRequestIDConflict) {
		// Lost the race: drop our just-created entry + dir and return the winner.
		s.mu.Lock()
		delete(s.jobs, jobID)
		s.mu.Unlock()
		_ = os.RemoveAll(resultDir)
		if rec, ok, gerr := s.meta.GetJobByRequestID(req.RequestID); gerr != nil {
			return JobResult{}, gerr
		} else if ok {
			return fromRecord(rec), nil
		}
		// The winner's row is unexpectedly absent (should not happen, since the
		// conflict means a row with this request_id exists); surface the conflict.
		return JobResult{}, persistErr
	}

	// E13: the queued snapshot is durably persisted (or best-effort for the
	// no-request_id case) — record the lifecycle event now that submission is a
	// fact. Detail carries the identity/routing fields (no secrets, SR403).
	s.recordEvent(jobID, EventJobSubmitted, map[string]any{
		"project":   req.ProjectKey,
		"agent":     req.Agent,
		"runner":    req.Runner,
		"caller_id": req.CallerID,
		"tags":      req.Tags,
	})
	// E13: a remote runner (peer-http / ws-worker) forwards the job to a remote
	// executor — record the dispatch with the resolved target.
	if remote {
		s.recordEvent(jobID, EventJobDispatched, map[string]any{
			"runner":    req.Runner,
			"worker_id": req.WorkerID,
		})
	}

	// E16: count the submission (nil-safe). Labels are the bounded routing
	// identity (caller/project/agent/runner); no high-cardinality fields.
	if s.metrics != nil {
		s.metrics.JobSubmitted(req.CallerID, req.ProjectKey, req.Agent, req.Runner)
	}

	sem := s.semaphore(req.ProjectKey, proj.MaxConcurrentJobs)
	// E17: per-caller concurrency slot (design §7.2). Resolved from the SAME cfg
	// snapshot (caller override > governance default > unlimited). nil when the
	// caller has no cap or no id — then execute does not gate on it.
	callerSem := s.callerSemaphore(req.CallerID, cfg.Server.CallerConcurrencyLimit(req.CallerID))
	timeout := normalizeTimeout(req.TimeoutSec)
	go s.execute(entry, run, sem, callerSem, runReq, timeout)

	return entry.snapshot(), nil
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
		req.Env = mergeEnv(rc.Env, req.Env)
	}
	return nil
}

// mergeEnv returns base with override layered on top (override wins on key
// collision). nil-safe; returns a fresh map (inputs unchanged). When override is
// empty it returns base unchanged (no needless copy).
func mergeEnv(base, override map[string]string) map[string]string {
	if len(override) == 0 {
		return base
	}
	out := make(map[string]string, len(base)+len(override))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range override {
		out[k] = v
	}
	return out
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

// normalizeTimeout applies the default and clamps to the max (plan §9 P4).
func normalizeTimeout(sec int) time.Duration {
	if sec <= 0 {
		sec = DefaultTimeoutSec
	}
	if sec > MaxTimeoutSec {
		sec = MaxTimeoutSec
	}
	return time.Duration(sec) * time.Second
}
