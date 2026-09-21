package job

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/store"
	"github.com/inhere/gofer/internal/util"
)

// xferUploadsToRunner projects the request's upload specs onto the runner package's
// type (runner is a leaf and owns no job types).
func xferUploadsToRunner(in []UploadSpec) []runner.XferUpload {
	if len(in) == 0 {
		return nil
	}
	out := make([]runner.XferUpload, 0, len(in))
	for _, u := range in {
		out = append(out, runner.XferUpload{XferID: u.XferID, Dest: u.Dest})
	}
	return out
}

// Submit validates the request, creates the result dir, persists the request and
// starts the job asynchronously. It returns the initial JobResult (status
// running) once the goroutine is launched. Validation/setup failures return an
// error and no job.
func (s *Service) Submit(req JobRequest) (JobResult, error) {
	// Snapshot the config ONCE for the whole Submit so a concurrent Reload cannot
	// make this single submit observe two different configs (peer classification,
	// validation, result base dir all read the same snapshot).
	cfg := s.config()

	// Runner spelling first: everything below (remote classification, validate's
	// allowlist check, the runner registry lookup, the persisted row) keys on the
	// canonical name. See normalizeRunner.
	req.Runner = normalizeRunner(cfg, req.Runner)

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
	if err := s.applyTemplate(cfg, &req); err != nil {
		return JobResult{}, err
	}

	// SUP-01 P2: resolve the verify step (argv + deadline) from the SAME cfg snapshot
	// BEFORE validate, so the admission gates, the Forward, request_json and the
	// persisted row all carry one decided pair — an executing machine (or a rerun)
	// never re-derives the project default. A verify/no_verify contradiction is
	// rejected here, while both are still visible.
	if err := resolveVerify(cfg, &req); err != nil {
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

	// JOB-11: resolve the same-directory lock decision ONCE, from the SAME cfg snapshot
	// that validated the request and AFTER any submit-time agent substitution (the
	// substituted agent's TYPE is what the default rule reads). Stamping it back onto
	// the request keeps one decided value on every surface: request_json, the persisted
	// row, the peer/worker forward and the executing machine.
	dirExclusive := resolveDirExclusive(cfg, &req)
	req.ExclusiveDir = &dirExclusive

	// bd h-aii-s9ck: resolve the job's deadline ONCE, from the SAME cfg snapshot as
	// validation (project ceiling > server ceiling > 1h default), BEFORE the entry /
	// forward are built. The running job (execute's ctx), the persisted row, the API
	// response and a remote dispatch then all carry one number, and a truncated
	// request is reported instead of silently clamped.
	timeout, timeoutClamped := normalizeTimeout(req.TimeoutSec, req.Interactive,
		isCLIAgent(cfg, req.Agent), cfg.EffectiveMaxTimeoutSec(req.ProjectKey))
	timeoutSec := int(timeout / time.Second)
	if len(req.EnvFiles) > 0 && (remote || req.Runner != builtinLocalRunner) {
		return JobResult{}, fmt.Errorf("%w: env_files are supported only for local runner jobs", ErrInvalidRequest)
	}

	// Attempt is 1-based: a first run (no engine-set attempt) is attempt 1 when the
	// job opts into retry (E24) OR belongs to a workflow, so the persisted attempt
	// numbering is meaningful. A plain non-retry job leaves it 0 (omitempty).
	if req.Attempt < 1 && (req.Retry != nil || req.WorkflowID != "") {
		req.Attempt = 1
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

	// WT-01: resolve the project-level worktree_default into the request NOW, so the
	// Forward, request_json and the executing machine all act on one explicit
	// decision (a worker then never has to re-derive a default from its own config).
	req.Worktree = worktreeRequested(cfg, &req)

	// GATE-01 S3: resolve the人工验收 request NOW (--review / the project's
	// require_review default / a workflow step's explicit override), so the Forward,
	// request_json and the persisted result all carry one decided value — the
	// executing side never has to re-derive a default from its own config.
	req.Review = reviewRequested(cfg, &req)

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

	// WT-01: `--worktree` runs the job in a managed git worktree of this checkout
	// (<top>/tmp/gofer/wt/<job-id>, branch gofer/<job-id>) so parallel jobs stop
	// sharing one index. Created HERE — on the EXECUTING machine (serve-local, or the
	// worker's own job.Service, which re-enters this very function with runner=local)
	// — and only after the job's result dir exists, because the worktree dir is keyed
	// by job id. A remote (forwarding) submit skips it: the flag rides the Forward and
	// the peer creates the worktree against its own project root (设计 §二).
	var wt *worktreeRef
	if req.Worktree && !remote {
		wtCtx, wtCancel := context.WithTimeout(context.Background(), worktreeTimeout)
		wt, err = prepareJobWorktree(wtCtx, workDir, jobID, req.WorktreeBase)
		wtCancel()
		if err != nil {
			// No job was launched, so drop the just-created result dir (nothing else
			// references this id yet).
			_ = os.RemoveAll(resultDir)
			return JobResult{}, err
		}
		if mapped, merr := wt.mapCwd(workDir); merr != nil {
			_ = os.RemoveAll(resultDir)
			return JobResult{}, merr
		} else {
			workDir = mapped
		}
	}

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
	// WEB-03 T0: 仅【本机执行】的 interactive job 路由本机 pty runner。worker 远端
	// interactive 保持 worker runner(forward)，pty 在 worker 侧选(P2)。同一份代码在
	// serve 与 worker 都跑：worker 的 handleDispatch 强制 runner=local → !remote → 命中 pty。
	if req.Interactive && !remote {
		if pr := s.runners[builtinPtyRunner]; pr != nil {
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
	// XFER-01 X2: the file steps run where the job runs. For a LOCAL job that is this
	// machine (it places the uploads in workDir before the agent and matches the
	// collect globs there); a remote job's copies ride the Forward below instead.
	runReq.Uploads = xferUploadsToRunner(req.Uploads)
	runReq.Collect = req.Collect
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
	if req.InitialInput != "" && runReq.InitialInputQuietMs == 0 {
		runReq.InitialInputQuietMs = cfg.EffectiveSessionTakeoverInputDelayMs()
	}
	if remote {
		runReq.Forward = &runner.Forward{
			ProjectKey: req.ProjectKey,
			Agent:      req.Agent,
			PeerRunner: builtinLocalRunner,
			Prompt:     req.Prompt,
			AgentArgs:  req.AgentArgs,
			Cmd:        req.Cmd,
			Cwd:        req.Cwd,
			// WT-01: the executor creates the worktree (that machine owns the
			// checkout); the submitting side already resolved worktree_default into
			// req.Worktree.
			Worktree:     req.Worktree,
			WorktreeBase: req.WorktreeBase,
			// The ADMITTED deadline (clamped above), not the raw request: the server
			// owns admission, so a remote execution must not re-admit 2h for a request
			// this server already cut to 1h (bd h-aii-s9ck).
			TimeoutSec:  timeoutSec,
			Interactive: req.Interactive,
			Cols:        req.Cols,
			Rows:        req.Rows,
			// Path B priming rides to the executor, whose pty starts the resumed
			// TUI: the text and the quiet window travel together (see above).
			InitialInput:        runReq.InitialInput,
			InitialInputQuietMs: runReq.InitialInputQuietMs,
			ResumeSourceAgent:   req.ResumeSourceAgent,
			SystemPrompt:        req.SystemPrompt,
			// ACP-01 S2: a continuation reaches a remote executor with its session and
			// lineage so the worker's local job resolves the same session/load. Empty for
			// a plain job (a bare session_id is not a resume).
			SessionID:   req.SessionID,
			ResumedFrom: req.ResumedFrom,
			// bd h-aii-0ql3: read-only rides to the executor, whose own admission +
			// agent config decide how (or whether) it can be honoured.
			ReadOnly: req.ReadOnly,
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
			// JOB-11: the resolved same-directory lock decision travels to the machine
			// that owns the checkout — its job.Service is the one that can actually hold
			// the lock (this process only knows a relative cwd for a remote job).
			ExclusiveDir: req.ExclusiveDir,
		}
		// Bridge the peer's running-job interactions (P9) onto this host job.
		runReq.Interactions = remoteInteractionSink{s: s, jobID: jobID}
	} else {
		// Resolve the agent from the SAME cfg snapshot the request was validated
		// against (BuildFrom/ResolveAgent, not the agent registry): going through the
		// registry re-reads its own config pointer, so a Reload landing between
		// validate and here produced a job that passed the OLD policy but executed the
		// NEW agent's command (mixed config state). ac is resolved once and reused by
		// every argv-injection below for the same reason.
		resolved, berr := agent.BuildFrom(cfg, req.Agent, req.Prompt, req.Cmd, agent.Vars{
			Cwd:       workDir,
			JobID:     jobID,
			ResultDir: resultDir,
		}, agent.BuildOptions{AllowEmptyPrompt: req.Interactive, Interactive: req.Interactive, AgentArgs: req.AgentArgs, ReadOnly: req.ReadOnly})
		if berr != nil {
			return JobResult{}, berr
		}
		ac, acOK := agent.ResolveAgent(cfg, req.Agent)
		runReq.Command = resolved.Command
		runReq.Args = resolved.Args
		// ACP-01: an acp-agent's local execution is the ACP client runner, not a
		// plain child process — the prompt travels over the protocol, so the local
		// runner would exec an agent that never receives it. The runner is selected
		// by key (like the pty runner) but is REQUIRED: silently falling back to the
		// local runner would run a promptless agent process.
		if acOK && ac.Type == agent.TypeACPAgent {
			ar := s.runners[builtinACPRunner]
			if ar == nil {
				return JobResult{}, fmt.Errorf("%w: agent %q (acp-agent) requires the acp runner", ErrInvalidRequest, req.Agent)
			}
			run = ar
			runReq.ACP = acpRequest(cfg, ac, req, resultDir)
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
		runReq.Env = goferJobEnv(util.MergeEnv(util.MergeEnv(secretMap, resolved.Env), req.Env), jobID, workDir, resultDir)
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
	if wt != nil {
		wtPath, wtBranch, wtBase = wt.Path, wt.Branch, wt.BaseSHA
	}
	// SUP-01 P3: whether the row should carry a requested_agent — the caller's own
	// agent when this submit substituted it, or the chain root's when this job is a
	// takeover. A plain job keeps it empty ("ran exactly what was asked").
	requestedAgent := req.RequestedAgent
	if substitutedFrom != "" {
		requestedAgent = substitutedFrom
	}

	now := s.nowFn().Unix()
	entry := &jobEntry{
		store: st,
		done:  make(chan struct{}),
		wt:    wt,
		result: JobResult{
			ID:          jobID,
			ProjectKey:  req.ProjectKey,
			Agent:       req.Agent,
			Runner:      req.Runner,
			Interactive: req.Interactive,
			// bd h-aii-0ql3：只读是 job 的持久属性（jobs.read_only），resume 继承、show/web 可见。
			ReadOnly: req.ReadOnly,
			// JOB-11：同 cwd 独占决策（jobs.dir_exclusive）——提交期定死，show/web 与
			// 事后排查据此回答"这次运行当初是否（被允许）独占这棵工作树"。
			DirExclusive: dirExclusive,
			// GATE-01 S3：人工验收同样是 job 的持久属性（jobs.require_review），决定
			// finish 是落 done 还是 needs_review，resume 继承、show/web 可见。
			RequireReview: req.Review,
			// bd h-aii-s9ck: the deadline this job actually runs under, plus the
			// clamp report (requested > ceiling) so it is never a silent truncation.
			TimeoutSec:          timeoutSec,
			RequestedTimeoutSec: req.TimeoutSec,
			TimeoutClamped:      timeoutClamped,
			Title:               req.Title,
			WorkerID:            req.WorkerID,
			Status:              StatusQueued,
			Cwd:                 workDir,
			ResultDir:           resultDir,
			StartedAt:           now,
			RequestJSON:         string(reqJSON),
			CallerID:            req.CallerID,
			RequestID:           req.RequestID,
			Tags:                req.Tags,
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
			Role:   req.Role,
			PlanID: req.PlanID,
			// SUP-01 C：该 job 挂接的 checklist 项（空=不挂）。落 jobs.todo_id，终态由
			// linkTodoOutcome 把结果写回该 todo。
			TodoID:      req.TodoID,
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
			WorktreeBaseSHA: wtBase,
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

	// SUP-01 P3: the submit-time substitution is recorded once the job is a fact, and
	// the row's requested_agent keeps the caller's original choice visible.
	if substitutedFrom != "" {
		s.recordEvent(jobID, EventJobAgentSubstituted, map[string]any{
			"from": substitutedFrom, "to": req.Agent, "reason": "degraded",
		})
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
	// SUP-01 C: the job is a fact, so its checklist item can follow it (best-effort —
	// a job must never fail to start because the todo could not be updated).
	if req.TodoID != "" && !req.TodoForeign {
		s.linkTodoSubmit(req.TodoID, jobID)
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
	// identity (caller/project/agent/runner); no high-cardinality fields.
	if s.metrics != nil {
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
	}, runReq, timeout)

	return entry.snapshot(), nil
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
	}
	ac, ok := agent.ResolveAgent(cfg, req.Agent)
	if !ok {
		return false
	}
	return ac.Type != agent.TypeExec && !req.ReadOnly && !req.Interactive
}

// titleMaxRunes caps an auto-extracted job title (defaultJobTitle).
const titleMaxRunes = 32

// isCLIAgent reports whether the named agent is a cli-agent (claude/codex
// style long-running session). Unknown agents (e.g. peer-only agents on a
// remote submit the host cannot resolve) are not cli-agents.
func isCLIAgent(cfg *config.Config, name string) bool {
	a, ok := cfg.Agents[name]
	return ok && a.Type == "cli-agent"
}

// acpRequest builds the acp runner payload (ACP-01) from an acp-agent's config and
// the request being submitted: the prompt, the result dir the runner writes
// acp.jsonl under, the RESOLVED approval policy of this job (GATE-01: the project's
// approval block tightened by the agent's acp.permission_policy), the MCP servers it
// advertises in session/new, whether the agent's thinking goes to the logs
// (acp.log_thoughts), and — for a continuation — the session to LOAD. A nil acp
// sub-block yields the defaults (auto-allow permissions, no MCP servers, thoughts kept).
func acpRequest(cfg *config.Config, ac config.AgentConfig, req JobRequest, resultDir string) *runner.ACPRequest {
	r := &runner.ACPRequest{
		Prompt:        req.Prompt,
		ResultDir:     resultDir,
		Approval:      cfg.EffectiveApproval(req.ProjectKey, req.Agent),
		LoadSessionID: resumeLoadSessionID(req),
		LogThoughts:   ac.ACP.LogsThoughts(),
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
	return trimTitleRunes(req.Prompt)
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
// while an explicit timeout_sec still bounds the session. cli-agent jobs default
// to DefaultAgentTimeoutSec (agents run long); everything else defaults to
// DefaultTimeoutSec. max <= 0 means the caller resolved no ceiling and falls back
// to DefaultMaxTimeoutSec.
//
// The second return reports whether the clamp actually truncated an EXPLICIT
// request (requested > max). A default that happens to exceed the ceiling is not
// a clamp of the request — the caller never asked for that value — so it does not
// raise the flag and does not warn.
func normalizeTimeout(sec int, interactive bool, cliAgent bool, max int) (time.Duration, bool) {
	if max <= 0 {
		max = DefaultMaxTimeoutSec
	}
	if interactive && sec <= 0 {
		return 0, false
	}
	requested := sec
	if sec <= 0 {
		if cliAgent {
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
