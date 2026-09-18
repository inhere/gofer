package job

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
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
	return s.resumeJob(jobID, prompt, runner, callerID, 0)
}

func (s *Service) resumeJob(jobID, prompt, runner, callerID string, autoAttempt int) (JobResult, error) {
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

	ac, ok := s.agents.Get(src.Agent)
	if !ok {
		return JobResult{}, fmt.Errorf("%w: agent %q", ErrResumeUnsupported, src.Agent)
	}

	// 同 runner 约束 (design §8): an explicit, differing runner is rejected; an
	// empty runner defaults to the source runner (the common case).
	if runner != "" && runner != src.Runner {
		return JobResult{}, fmt.Errorf("%w: session bound to runner %q, not %q", ErrCrossRunner, src.Runner, runner)
	}

	// ACP-01 S2: an acp-agent's session lives behind the ACP protocol, so the
	// continuation is NOT an exec carrier — it is a new acp-agent job that LOADS the
	// source session (session/load) and drives the new prompt as its turn. The exec
	// SessionResume templates below do not apply: nothing re-runs a CLI here.
	if ac.Type == agent.TypeACPAgent {
		// An agent that declares acp.load_session: false is not resumable at all — say
		// so up front rather than submitting a job the agent will refuse.
		if !ac.ACP.AllowsLoadSession() {
			return JobResult{}, fmt.Errorf("%w: agent %q declares acp.load_session: false", ErrResumeUnsupported, src.Agent)
		}
		if strings.TrimSpace(prompt) == "" {
			return JobResult{}, fmt.Errorf("%w: resume requires a prompt", ErrInvalidRequest)
		}
		// Same inheritance as the exec carrier (below): the continuation is governed
		// like the run it continues and keeps the source's provenance/lineage. An
		// acp-agent is batch-only by definition, so Interactive stays false.
		return s.Submit(JobRequest{
			ProjectKey: src.ProjectKey,
			Agent:      src.Agent,
			Runner:     src.Runner,
			WorkerID:   src.WorkerID,
			Prompt:     prompt,
			TimeoutSec: src.TimeoutSec,
			Tags:       src.Tags,
			Title:      resumedTitle(src.Title),
			Cwd:        s.resumeCwd(src),
			CallerID:   callerID,
			// Explicit SessionID: the new job binds to the SAME session, and
			// ResumedFrom marks it a continuation — which is what makes submit fill
			// the runner's LoadSessionID (a plain job's session_id never loads).
			SessionID:         src.SessionID,
			ResumeSourceAgent: src.Agent,
			// bd h-aii-0ql3: read-only is a property of the work, so it is inherited —
			// the executor switches the loaded session back into the read-only mode.
			ReadOnly: src.ReadOnly,
			// GATE-01 S3: so is人工验收 — the continuation delivers the same work to the
			// same reviewer, and a reviewed chain never becomes self-accepting halfway.
			// ReviewFixed pins the SOURCE's resolved decision, so a project default that
			// has since changed cannot rewrite it.
			Review:      src.RequireReview,
			ReviewFixed: true,
			Channel:     src.Channel,
			Client:      src.Client,
			OriginAgent: src.OriginAgent,
			EscalateTo:  src.EscalateTo,
			PlanID:      src.PlanID,
			// SUP-01 C：续投继承 checklist 挂接（TodoForeign 一并继承：worker 上的本地行
			// 同样只显示不联动），整条链的 round 自然串在同一个 todo 的 note 上。
			TodoID:            src.TodoID,
			TodoForeign:       src.TodoForeign,
			SourceJobID:       jobID,
			ResumedFrom:       jobID,
			AutoResumeAttempt: autoAttempt,
		})
	}

	// 交互源走交互模板（进 TUI，无 -p/exec）；非交互源走 SessionResume。
	tmpl := ac.SessionResume
	if src.Interactive && len(ac.SessionResumeInteractive) > 0 {
		tmpl = ac.SessionResumeInteractive
	}
	if len(tmpl) == 0 {
		return JobResult{}, fmt.Errorf("%w: agent %q", ErrResumeUnsupported, src.Agent)
	}
	// 非交互 resume 需要非空 prompt：claude `-p ""` / 空续投无意义会崩。交互源不看 prompt。
	if !src.Interactive && strings.TrimSpace(prompt) == "" {
		return JobResult{}, fmt.Errorf("%w: non-interactive resume requires a prompt", ErrInvalidRequest)
	}

	// argv = [agentConfig.Command] + rendered SessionResume (design T2.1). The new
	// job runs as the built-in exec agent so the resume argv executes verbatim; the
	// agent's own Command (e.g. "claude"/"codex") is argv[0].
	argv := append([]string{ac.Command}, agent.Render(tmpl, agent.Vars{SessionID: src.SessionID, Prompt: prompt})...)
	// bd h-aii-0ql3: a read-only source continues read-only. The carrier is an exec job,
	// whose argv is passed through verbatim (BuildFrom never appends for exec), so the
	// SOURCE agent's sandbox flags are baked in here — the continuation cannot be
	// upgraded to writable, and the flag still rides JobRequest.ReadOnly (below) so the
	// next link of the chain inherits it too.
	if src.ReadOnly {
		argv = append(argv, ac.ReadOnlyArgs...)
	}

	// E35 (review #5, 实测定稿 2026-06-29 / design §5 结论 / §12 已实测): the role system
	// prompt is deliberately NOT re-injected on resume — BOTH built-ins restore it
	// natively. claude-cli 2.1.191 `claude --resume <sid>` restores the system prompt set
	// by `--append-system-prompt`; codex-cli 0.142 `codex exec resume <sid>` likewise
	// restores the `-c developer_instructions=` set on the source session (both verified:
	// a marker token forced by the source job's system prompt reappears in the resumed
	// turn WITHOUT re-passing the flag; a fresh session never emits it). Re-rendering
	// SystemInject here would only duplicate the prompt.

	return s.Submit(JobRequest{
		ProjectKey: src.ProjectKey,
		Agent:      agent.ExecAgentKey,
		Cmd:        argv,
		Runner:     src.Runner,
		WorkerID:   src.WorkerID,
		// The carrier is an exec job, whose default timeout is the 5-minute
		// DefaultTimeoutSec — far below what the agent run it continues was given
		// (a resumed codex run died at 300s while its source had 3600s). Inherit
		// the source's effective timeout, tags and title so the continuation is
		// governed like the run it continues.
		TimeoutSec: src.TimeoutSec,
		Tags:       src.Tags,
		Title:      resumedTitle(src.Title),
		// 交互源续接为交互 job：走 pty runner，命令用交互模板（上面已选）。前端跳转后
		// ?attach=1 自动接入终端（P7 选 A）。非交互源 Interactive 为 false，行为不变。
		Interactive: src.Interactive,
		// 续接落原 job 的相对 cwd（从 RequestJSON 还原；JobResult.Cwd 是已解析的绝对路径）。
		// A --worktree source keeps its own checkout: continue INSIDE that worktree
		// (its path is under the project root, so it is a valid relative cwd) rather
		// than back in the main checkout where the branch's work is not visible.
		Cwd:      s.resumeCwd(src),
		CallerID: callerID,
		// 显式带 SessionID：new job 复用同会话 id（注入/捕获均跳过），链回原会话、可再续。
		SessionID: src.SessionID,
		// bd h-aii-0ql3：只读随链继承（argv 已带沙箱参数，这里同时记录在 job 行上）。
		ReadOnly: src.ReadOnly,
		// GATE-01 S3：人工验收同样随链继承（与上面 acp 路径同一规则）。
		Review:      src.RequireReview,
		ReviewFixed: true,
		// 访问门按 SOURCE agent 判定：resume 只是用 exec 载体跑原 agent 的受限续接 argv，
		// 故豁免 exec/allow_exec 门（2026-06-26 决策）。仅 ResumeJob 设置，不入 request_json、不可伪造。
		ResumeSourceAgent: src.Agent,
		// 续接沿用源 job 的提交来源（provenance），保留会话链的原始渠道/来源主机。
		Channel: src.Channel,
		Client:  src.Client,
		// 续接沿用源 job 的 owner 路由（supervisor-routing P1.1），续接 job 的 escalation
		// 仍回投原 owner。
		OriginAgent: src.OriginAgent,
		EscalateTo:  src.EscalateTo,
		// 续跑归组（plan-orchestration P4，design §7）：续投 job 继承源 job 的 plan_id，
		// 使"一次会话里多轮续接"天然归入同一 plan 血缘（源 job 未归组时为空）。plan_id 是
		// 客户端可设的归组键（区别引擎私有 workflow_id），这里由后端从源 job 继承而非客户端声明。
		PlanID: src.PlanID,
		// SUP-01 C：续投继承 checklist 挂接（含 TodoForeign，见 acp 分支同一规则）。
		TodoID:      src.TodoID,
		TodoForeign: src.TodoForeign,
		// 血缘（P5，本次追加）：续投 job 指回源 job。resume 语义 = source_job_id=源 id 且
		// SessionID 与源相同（上面 :84 已带 SessionID=src.SessionID）——据此区分"续会话"
		// （rebuild 则 session 空/新）。
		SourceJobID:       jobID,
		ResumedFrom:       jobID,
		AutoResumeAttempt: autoAttempt,
	})
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
	rel, err := filepath.Rel(cfg.ExecPath(proj), src.Cwd)
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
