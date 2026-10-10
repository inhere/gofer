package job

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/util"
)

// Resume-path sentinels (session-capture P2, design §5.2 / §8). They wrap the
// well-formed-but-not-permitted cases so the HTTP layer can errors.Is them to a
// status (400) instead of string-matching. ErrUnknownJob (404) is reused from
// interaction.go for an absent source job.
var (
	// ErrNoSession marks a resume of a source job that never captured a session
	// id (claude not injected / codex regex未命中 / unsupported agent). HTTP: 400.
	ErrNoSession = errors.New("job has no captured session_id")
	// ErrJobNotTerminal marks a derive attempt (resume / rebuild) whose SOURCE job is
	// still queued/running. Resuming a live job would hand the SAME session_id to a
	// second process while the source still holds it (concurrent writes to one agent
	// session). Rebuild of a live job is technically harmless but rejected too, for a
	// consistent rule (P6/D2 — this intentionally changes `job rerun <running-id>`,
	// which historically ignored the source status, to a 400).
	// Do not confuse with ErrJobTerminal (terminal jobs cannot take interactions).
	ErrJobNotTerminal = errors.New("source job is not in a terminal state")
	// ErrResumeUnsupported marks a source job whose agent has no SessionResume
	// template (it cannot be续接). HTTP: 400.
	ErrResumeUnsupported = errors.New("agent does not support resume")
	// ErrCrossRunner marks a resume that asks for a runner different from the
	// source job's. Session state lives on the original runner's filesystem
	// (design §8 同 runner 约束), so续接必须落同一 runner. HTTP: 400.
	ErrCrossRunner = errors.New("resume must use the same runner")
)

// ResumeJob starts a NEW job that续接 the底层 agent CLI 会话 of an existing job
// (session-capture P2, design §5.2). It looks up the source job's captured
// SessionID / agent / runner / cwd, renders the agent's SessionResume template
// into an exec argv ([command] + resume args), and submits it as an exec job on
// the SAME runner with SessionID set so the new job链 round-trips to the same
// session. The编排 lives here in the job Service (G021): HTTP/CLI入口 only bind +
// 转发 + map the sentinels below to status codes.
//
// callerID is the authenticated submitter (stamped by the入口 from its auth
// context, anti-spoof, mirroring Submit). runner is the OPTIONAL caller-supplied
// target runner: when empty the source runner is used; when non-empty it must
// equal the source runner (同 runner 约束) — a mismatch is ErrCrossRunner.
func (s *Service) ResumeJob(jobID, prompt, runner, callerID string) (JobResult, error) {
	return s.resumeJob(jobID, prompt, runner, callerID, 0, nil, ResumeOptions{})
}

// Resume modes: how the continuation is started. Empty keeps the form the source
// job implies (ACP -> resident ACP session, cli batch -> `--resume -p`, cli pty ->
// pty), which is the only behaviour ResumeJob offers.
const (
	// ResumeModeSession continues as a resident ACP session (needs an acp-agent).
	ResumeModeSession = "session"
	// ResumeModeInteractive continues as a pty job running the agent's interactive
	// resume argv (needs a cli-agent with a session_resume_interactive template).
	ResumeModeInteractive = "interactive"
	// ResumeModeBatch continues as a one-shot `--resume -p <prompt>` job (needs a
	// cli-agent with a session_resume template and a prompt).
	ResumeModeBatch = "batch"
)

// ResumeOptions are the optional knobs of ResumeJobWith. Agent switches the
// continuation to another agent, but only inside the source agent's session family
// (agent.SessionCompatible): e.g. a claude-acp session continued by the claude CLI.
type ResumeOptions struct {
	admissionPermit *AdmissionPermit
	Mode            string
	Agent           string
	// Model overrides the model the continuation inherits from its source (N1 §B).
	// Empty keeps the source's model; the continuation cannot be reset to "agent
	// default" this way (start a new job for that).
	Model string
	// Budget overrides, per dimension, the budget the continuation inherits from its
	// source (N2 §B). A resume is a NEW job: its meter starts from zero.
	Budget *Budget
	// Env is the continuation's OWN explicit env: it overrides everything the
	// continuation inherits (source agent env, source job env) and, like a plain
	// submit's env, is recorded in the new request_json. Inherited values are never
	// copied there (see resume_env.go).
	Env map[string]string
}

// ResumeJobWith is ResumeJob with an explicit continuation form (opts.Mode) and/or
// target agent (opts.Agent). A zero ResumeOptions is exactly ResumeJob.
func (s *Service) ResumeJobWith(jobID, prompt, runner, callerID string, opts ResumeOptions) (JobResult, error) {
	return s.resumeJob(jobID, prompt, runner, callerID, 0, nil, opts)
}

// extraTags are added to the source job's tags on the continuation. Only the wakeup
// path (JOB-09) uses it — a continuation must be findable by reason
// (`wakeup:<id>`) without losing its original tags.
func (s *Service) resumeJob(jobID, prompt, runner, callerID string, autoAttempt int, extraTags []string, opts ResumeOptions) (JobResult, error) {
	opts.Mode = strings.ToLower(strings.TrimSpace(opts.Mode))
	switch opts.Mode {
	case "", ResumeModeSession, ResumeModeInteractive, ResumeModeBatch:
	default:
		return JobResult{}, fmt.Errorf("%w: unknown resume mode %q (want session|interactive|batch)", ErrInvalidRequest, opts.Mode)
	}
	src, ok := s.Get(jobID)
	if !ok {
		return JobResult{}, fmt.Errorf("%w: %q", ErrUnknownJob, jobID)
	}
	if !IsTerminal(src.Status) {
		// GATE-01 S3: a job parked in needs_review is intentionally not terminal, so
		// resume refuses it — but the caller's actual next step is accept/reject, so
		// say that instead of leaving them with the bare state name.
		if src.Status == StatusNeedsReview {
			return JobResult{}, fmt.Errorf("%w: %q is %s (accept or reject it first)", ErrJobNotTerminal, jobID, src.Status)
		}
		return JobResult{}, fmt.Errorf("%w: %q is %s", ErrJobNotTerminal, jobID, src.Status)
	}
	if src.SessionID == "" {
		return JobResult{}, fmt.Errorf("%w: %q", ErrNoSession, jobID)
	}

	// A CLI continuation runs through an exec carrier. Resuming that carrier again
	// must resolve the original CLI definition, not the carrier's reserved "exec"
	// agent (which intentionally has no resume template). The durable link is the
	// ResumedFrom chain: walk it up to the job that named the agent (the same walk
	// fallbackBase does). OriginAgent is an owner routing id, not an agent key, and
	// plain CLI submissions never set it, so it cannot be relied on here.
	resumeAgent := src.Agent
	if src.Agent == agent.ExecAgentKey && src.ResumedFrom != "" {
		if base := s.fallbackBase(src); !isExecCarrier(base) {
			resumeAgent = base.Agent
		}
	}
	srcCfg, ok := s.agents.Get(resumeAgent)
	if !ok {
		// Jobs recorded before the built-in tty-claude / tty-codex templates were removed
		// still name them. They were the same CLI driven in a pty, so continue them with
		// the dual-mode claude / codex (the form follows src.Interactive, i.e. a pty
		// resume) instead of keeping a whole retired template just for old history.
		if alias, legacy := legacyTTYAgent(resumeAgent); legacy {
			if ac, found := s.agents.Get(alias); found {
				resumeAgent, srcCfg, ok = alias, ac, true
			}
		}
	}
	if !ok {
		return JobResult{}, fmt.Errorf("%w: agent %q", ErrResumeUnsupported, resumeAgent)
	}

	// Target agent: the source's own unless the caller names another one of the
	// same session family (the session store must be shared, see agent.SessionFamily).
	targetAgent, ac := resumeAgent, srcCfg
	if want := strings.TrimSpace(opts.Agent); want != "" && want != resumeAgent {
		tc, found := s.agents.Get(want)
		if !found {
			return JobResult{}, fmt.Errorf("%w: unknown agent %q", ErrResumeUnsupported, want)
		}
		if !agent.SessionCompatible(resumeAgent, srcCfg, want, tc) {
			return JobResult{}, fmt.Errorf("%w: a %q session cannot be continued by agent %q (not the same session family)", ErrResumeUnsupported, resumeAgent, want)
		}
		targetAgent, ac = want, tc
	}

	// 同 runner 约束 (design §8): an explicit, differing runner is rejected; an
	// empty runner defaults to the source runner (the common case). The spelling is
	// normalized first so `--runner server` on a job that ran on the canonical
	// "local" is the SAME runner (an alias, not a cross-runner request).
	runner = config.NormalizeRunnerName(runner)
	if runner != "" && runner != config.NormalizeRunnerName(src.Runner) {
		return JobResult{}, fmt.Errorf("%w: session bound to runner %q, not %q", ErrCrossRunner, src.Runner, runner)
	}

	// The form follows the explicit mode; with none it follows the target agent
	// type, and for a cli agent the source's interactivity.
	form := opts.Mode
	if form == "" {
		switch {
		case ac.Type == agent.TypeACPAgent:
			form = ResumeModeSession
		case src.Interactive:
			form = ResumeModeInteractive
		default:
			form = ResumeModeBatch
		}
	}
	base := s.continuationBase(src, jobID, callerID, autoAttempt, extraTags)
	base.admissionPermit = opts.admissionPermit
	// N1 §B: the continuation keeps the source job's model; --model overrides it. An
	// INHERITED model that the target agent cannot take (a cli-agent with no
	// model_args, e.g. after switching family member) is dropped rather than refusing
	// the whole continuation; an EXPLICIT one is refused so a typo is never ignored.
	if m := strings.TrimSpace(opts.Model); m != "" {
		if err := CheckModel(m); err != nil {
			return JobResult{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
		}
		base.Model = m
	}
	if base.Model != "" && ac.Type != agent.TypeACPAgent && len(ac.ModelArgs) == 0 {
		if strings.TrimSpace(opts.Model) != "" {
			return JobResult{}, fmt.Errorf("%w: agent %q has no model_args (set agents.%s.model_args with {{model}})", ErrInvalidRequest, targetAgent, targetAgent)
		}
		base.Model = ""
	}
	// N2 §B: the continuation keeps the source job's budget (a fresh meter — it is a new
	// job); opts.Budget overrides per dimension. As with the model, an INHERITED ceiling
	// the target form cannot enforce (an interactive pty continuation) is dropped, while
	// an EXPLICIT one is refused so a requested limit is never silently ignored.
	if opts.Budget != nil {
		if err := CheckBudget(opts.Budget); err != nil {
			return JobResult{}, err
		}
	}
	base.Budget = opts.Budget.Over(src.Budget)
	if !base.Budget.IsZero() {
		if ok, why := budgetSupport(s.config(), targetAgent, form == ResumeModeInteractive); !ok {
			if !opts.Budget.IsZero() {
				return JobResult{}, fmt.Errorf("%w: budget cannot be enforced: %s", ErrInvalidRequest, why)
			}
			base.Budget = nil
		}
	}
	if len(opts.Env) > 0 {
		base.Env = util.MergeEnv(nil, opts.Env)
	}
	switch form {
	case ResumeModeSession:
		return s.resumeACPSession(base, src, ac, targetAgent, prompt, opts.Mode != "", autoAttempt)
	default:
		return s.resumeCLICarrier(base, src, ac, targetAgent, prompt, form == ResumeModeInteractive, opts.Mode != "")
	}
}

// continuationBase builds the JobRequest fields every continuation shares — the
// continuation is governed like the run it continues and keeps its provenance and
// lineage. The per-form callers add only what differs (agent, argv/prompt, session
// or pty shape).
func (s *Service) continuationBase(src JobResult, jobID, callerID string, autoAttempt int, extraTags []string) JobRequest {
	return JobRequest{
		ProjectKey: src.ProjectKey,
		Runner:     src.Runner,
		WorkerID:   src.WorkerID,
		// The carrier is an exec job, whose default timeout is the 5-minute
		// DefaultTimeoutSec — far below what the agent run it continues was given
		// (a resumed codex run died at 300s while its source had 3600s). Inherit
		// the source's effective timeout, tags and title so the continuation is
		// governed like the run it continues.
		TimeoutSec: src.TimeoutSec,
		Tags:       wakeupTagList(src.Tags, extraTags),
		Title:      resumedTitle(src.Title),
		// 续接落原 job 的相对 cwd（从 RequestJSON 还原；JobResult.Cwd 是已解析的绝对路径）。
		// A --worktree source keeps its own checkout: continue INSIDE that worktree
		// (its path is under the project root, so it is a valid relative cwd) rather
		// than back in the main checkout where the branch's work is not visible.
		Cwd:         s.resumeCwd(src),
		LockPaths:   lockPathsFromRequest(src.RequestJSON),
		LockWaitSec: lockWaitFromRequest(src.RequestJSON),
		CallerID:    callerID,
		// 显式带 SessionID：new job 复用同会话 id（注入/捕获均跳过），链回原会话、可再续。
		// For an acp-agent the explicit id plus ResumedFrom is what makes submit fill
		// the runner's LoadSessionID (a plain job's session_id never loads).
		SessionID: src.SessionID,
		// JOB-06①: a continuation is NOT re-injected with rules — the session it
		// continues already carries them, and repeating the section every turn would
		// spend the context twice on the same text (design §一.3).
		RulesResolved: true,
		// bd h-aii-0ql3: read-only is a property of the work, so it is inherited.
		ReadOnly: src.ReadOnly,
		// N1 §B: so is the model; ResumeOptions.Model overrides it.
		Model: src.Model,
		// gofer-3nxa.2: the session was asked for 「## 可复用经验」, so the continuation's
		// report is captured like its source's.
		KnowledgeCapture: src.KnowledgeCapture,
		// JOB-11: the continuation works in the SAME directory as the run it
		// continues, so it inherits that run's lock decision instead of re-deriving
		// one from its own carrier shape.
		ExclusiveDir: &src.DirExclusive,
		// GATE-01 S3: so is人工验收 — the continuation delivers the same work to the
		// same reviewer; ReviewFixed pins the SOURCE's resolved decision.
		Review:      src.RequireReview,
		ReviewFixed: true,
		// 续接沿用源 job 的提交来源（provenance）与 owner 路由（supervisor-routing P1.1）。
		Channel:     src.Channel,
		Client:      src.Client,
		OriginAgent: src.OriginAgent,
		EscalateTo:  src.EscalateTo,
		// 续跑归组（plan-orchestration P4）：继承源 job 的 plan_id（后端继承，非客户端声明）。
		PlanID: src.PlanID,
		// SUP-01 C：续投继承 checklist 挂接（TodoForeign 一并继承）。
		TodoID:      src.TodoID,
		TodoForeign: src.TodoForeign,
		// SUP-01 P3：续投继承源 job 的转移计划，不重新读可能已变的配置。
		Fallback: src.Fallback,
		// 血缘（P5）：续投 job 指回源 job。resume 语义 = source_job_id=源 id 且 SessionID 与源相同。
		SourceJobID: jobID,
		ResumedFrom: jobID,
		// The env DECLARATION (file paths) is inherited by reference; env VALUES are
		// resolved at execution time (resume_env.go) and never copied into this request.
		EnvFiles:          resumeEnvFiles(src),
		AutoResumeAttempt: autoAttempt,
	}
}

// resumeACPSession continues the session as a NEW acp-agent job that LOADS the
// source session (session/load) and drives the new prompt as its turn (ACP-01 S2).
// The exec SessionResume templates do not apply: nothing re-runs a CLI here.
// explicit is true when the caller asked for this form (--mode session): then a
// runner that cannot host a resident session is an error instead of a quiet
// fall back to a one-shot continuation.
func (s *Service) resumeACPSession(req JobRequest, src JobResult, ac config.AgentConfig, targetAgent, prompt string, explicit bool, autoAttempt int) (JobResult, error) {
	if ac.Type != agent.TypeACPAgent {
		return JobResult{}, fmt.Errorf("%w: agent %q is not an acp-agent; --mode session needs one (use --agent <acp agent of the same family>)", ErrResumeUnsupported, targetAgent)
	}
	// An agent that declares acp.load_session: false is not resumable at all — say
	// so up front rather than submitting a job the agent will refuse.
	if !ac.ACP.AllowsLoadSession() {
		return JobResult{}, fmt.Errorf("%w: agent %q declares acp.load_session: false", ErrResumeUnsupported, targetAgent)
	}
	// User requested resumes always create a resident session. An empty prompt is
	// intentional: session/load succeeds and the new job parks in awaiting_input.
	// Automatic provider-error retries remain one-shot continuations, so they can
	// finish and let the retry policy classify the result without parking forever.
	// Resident ACP control is available locally and over the protocol-v13
	// worker transport. DEPRECATED(v0.89): remove in v0.92 after peer-http
	// supports resident ACP command forwarding; it currently keeps one-shot
	// ACP requests for compatibility with that transport.
	residentRunner := config.NormalizeRunnerName(src.Runner) == config.BuiltinLocalRunner || isWorkerRunner(s.config(), src.Runner)
	if explicit && !residentRunner {
		return JobResult{}, fmt.Errorf("%w: resident ACP session needs the local runner or a worker (v13+), not %q", ErrInvalidRequest, src.Runner)
	}
	continuous := autoAttempt == 0 && residentRunner
	if !continuous && strings.TrimSpace(prompt) == "" {
		return JobResult{}, fmt.Errorf("%w: resume requires a prompt", ErrInvalidRequest)
	}
	if continuous {
		req.IdleTimeoutSec, req.MaxSessionSec = src.IdleTimeoutSec, src.MaxSessionSec
	}
	req.Agent = targetAgent
	req.Prompt = prompt
	req.Session = continuous
	req.ResumeSourceAgent = targetAgent
	return s.Submit(req)
}

// resumeCLICarrier continues the session by running the agent CLI's own resume argv
// through an exec carrier job: the non-interactive `--resume -p` template, or with
// interactive the pty one (进 TUI, no -p). explicit is true when the caller asked
// for the form (--mode), which adds the project's interactive switch up front.
func (s *Service) resumeCLICarrier(req JobRequest, src JobResult, ac config.AgentConfig, targetAgent, prompt string, interactive, explicit bool) (JobResult, error) {
	if ac.Type == agent.TypeACPAgent {
		return JobResult{}, fmt.Errorf("%w: agent %q is an acp-agent, whose session cannot be resumed as a CLI job; pass --agent <cli agent of the same family>", ErrResumeUnsupported, targetAgent)
	}
	tmpl := ac.SessionResume
	if interactive {
		tmpl = ac.SessionResumeInteractive
		if len(tmpl) == 0 && !explicit {
			// A pty source whose agent declares no interactive template keeps the
			// historical behaviour: the batch template, run inside the pty.
			tmpl = ac.SessionResume
		}
		if explicit {
			if proj, ok := s.config().Projects[src.ProjectKey]; ok && !proj.IsInteractiveAllowed() {
				return JobResult{}, fmt.Errorf("%w: project %q does not allow interactive jobs (allow_interactive)", ErrInvalidRequest, src.ProjectKey)
			}
		}
	}
	if len(tmpl) == 0 {
		kind := "batch"
		if interactive {
			kind = "interactive"
		}
		return JobResult{}, fmt.Errorf("%w: agent %q has no %s resume template", ErrResumeUnsupported, targetAgent, kind)
	}
	// 非交互 resume 需要非空 prompt：claude `-p ""` / 空续投无意义会崩。交互形态不看 prompt。
	if !interactive && strings.TrimSpace(prompt) == "" {
		return JobResult{}, fmt.Errorf("%w: non-interactive resume requires a prompt", ErrInvalidRequest)
	}

	// argv = [agentConfig.Command] + rendered resume template (design T2.1). The new
	// job runs as the built-in exec agent so the resume argv executes verbatim; the
	// agent's own Command (e.g. "claude"/"codex") is argv[0].
	argv := []string{ac.Command}
	argv = append(argv, agent.GlobalArgs(ac)...)
	// N1 §B: the model rides the same slot as a fresh run — before the prompt argument
	// (appended for the interactive template, which carries none).
	argv = append(argv, agent.Render(agent.WithModelArgs(tmpl, modelArgsFor(ac, req.Model)), agent.Vars{SessionID: src.SessionID, Prompt: prompt, Model: req.Model})...)
	// bd h-aii-0ql3: a read-only source continues read-only. The carrier is an exec job,
	// whose argv is passed through verbatim (BuildFrom never appends for exec), so the
	// SOURCE agent's sandbox flags are baked in here — the continuation cannot be
	// upgraded to writable, and the flag still rides JobRequest.ReadOnly so the next
	// link of the chain inherits it too.
	if src.ReadOnly {
		argv = append(argv, ac.ReadOnlyArgs...)
	}

	// E35 (review #5, 实测定稿 2026-06-29 / design §5 结论 / §12 已实测): the role system
	// prompt is deliberately NOT re-injected on resume — BOTH built-ins restore it
	// natively (`claude --resume`, `codex exec resume`). Re-rendering SystemInject
	// here would only duplicate the prompt.
	req.Agent = agent.ExecAgentKey
	req.Cmd = argv
	// 交互形态走 pty runner，命令用交互模板；前端据新 job 的 interactive 决定是否 ?attach=1。
	req.Interactive = interactive
	// 访问门按 SOURCE agent 判定：resume 只是用 exec 载体跑原 agent 的受限续接 argv，
	// 故豁免 exec/allow_exec 门（2026-06-26 决策）。仅 ResumeJob 设置，不入 request_json、不可伪造。
	req.ResumeSourceAgent = targetAgent
	return s.Submit(req)
}

// resumable reports whether an agent's session can be continued, which is the
// question the automatic continuation asks BEFORE re-submitting. An acp-agent does
// it over the protocol (session/load) unless it declares acp.load_session: false; a
// cli-agent needs the resume argv template that continuation would render.
func resumable(ac config.AgentConfig) bool {
	if ac.Type == agent.TypeACPAgent {
		return ac.ACP.AllowsLoadSession()
	}
	return len(ac.SessionResume) > 0
}

// resumedTitle marks a continuation in the title so a plan or board reads
// "<title> (resumed)" instead of two identical rows; repeated resumes keep one
// suffix.
func resumedTitle(title string) string {
	if title == "" || strings.HasSuffix(title, " (resumed)") {
		return title
	}
	return title + " (resumed)"
}

// resumeCwd picks the continuation's cwd: the source's worktree (as a path
// relative to the project root, which is what Submit expects) when the source
// ran with --worktree, else the source's original relative cwd. A worktree that
// cannot be expressed under the project root (remote job, unknown project,
// checkout above the root) falls back to the original cwd.
func (s *Service) resumeCwd(src JobResult) string {
	orig := cwdFromRequestJSON(src.RequestJSON)
	if src.WorktreePath == "" || src.Cwd == "" {
		return orig
	}
	cfg := s.config()
	proj, ok := cfg.Projects[src.ProjectKey]
	if !ok {
		return orig
	}
	// Compare through symlinks (and, on Windows, through 8.3 short names): the
	// worktree path comes from `git rev-parse --show-toplevel`, which reports the
	// RESOLVED path, while the project root is whatever the operator configured
	// (macOS /var vs /private/var, Windows RUNNER~1 vs runneradmin). A raw Rel
	// would see the worktree as outside the project and silently continue in the
	// main checkout instead of the source's worktree.
	rel, err := filepath.Rel(util.RealPath(cfg.ExecPath(proj)), util.RealPath(src.Cwd))
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return orig
	}
	return filepath.ToSlash(rel)
}

// cwdFromRequestJSON recovers the original RELATIVE cwd from a job's persisted
// request_json blob (the JobResult.Cwd field is the resolved ABSOLUTE host path,
// which would mis-SafeJoin on re-submit). Mirrors TitleFromRequestJSON. A blank /
// unparseable blob yields "" → Submit treats it as the project root.
func cwdFromRequestJSON(s string) string {
	if s == "" {
		return ""
	}
	var r struct {
		Cwd string `json:"cwd"`
	}
	_ = json.Unmarshal([]byte(s), &r)
	return r.Cwd
}

func lockPathsFromRequest(raw string) []string {
	var req JobRequest
	if json.Unmarshal([]byte(raw), &req) != nil {
		return nil
	}
	return append([]string(nil), req.LockPaths...)
}

func lockWaitFromRequest(raw string) *int {
	var req JobRequest
	if json.Unmarshal([]byte(raw), &req) != nil {
		return nil
	}
	if req.LockWaitSec != nil {
		return req.LockWaitSec
	}
	return req.DirWaitMaxSec
}

// legacyTTYAgent maps the retired built-in interactive templates to the dual-mode
// agent that replaced them. DEPRECATED(v0.107): remove in v0.110 — by then no job that
// names tty-claude / tty-codex is worth resuming.
func legacyTTYAgent(key string) (string, bool) {
	switch key {
	case "tty-claude":
		return "claude", true
	case "tty-codex":
		return "codex", true
	}
	return "", false
}

// modelArgsFor returns the agent's model_args when a model was asked for, else nil
// (so WithModelArgs leaves the template untouched).
func modelArgsFor(ac config.AgentConfig, model string) []string {
	if model == "" {
		return nil
	}
	return ac.ModelArgs
}
