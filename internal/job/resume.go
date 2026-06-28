package job

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/inhere/gofer/internal/agent"
)

// Resume-path sentinels (session-capture P2, design §5.2 / §8). They wrap the
// well-formed-but-not-permitted cases so the HTTP layer can errors.Is them to a
// status (400) instead of string-matching. ErrUnknownJob (404) is reused from
// interaction.go for an absent source job.
var (
	// ErrNoSession marks a resume of a source job that never captured a session
	// id (claude not injected / codex regex未命中 / unsupported agent). HTTP: 400.
	ErrNoSession = errors.New("job has no captured session_id")
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
	src, ok := s.Get(jobID)
	if !ok {
		return JobResult{}, fmt.Errorf("%w: %q", ErrUnknownJob, jobID)
	}
	if src.SessionID == "" {
		return JobResult{}, fmt.Errorf("%w: %q", ErrNoSession, jobID)
	}

	ac, ok := s.agents.Get(src.Agent)
	if !ok || len(ac.SessionResume) == 0 {
		return JobResult{}, fmt.Errorf("%w: agent %q", ErrResumeUnsupported, src.Agent)
	}

	// 同 runner 约束 (design §8): an explicit, differing runner is rejected; an
	// empty runner defaults to the source runner (the common case).
	if runner != "" && runner != src.Runner {
		return JobResult{}, fmt.Errorf("%w: session bound to runner %q, not %q", ErrCrossRunner, src.Runner, runner)
	}

	// argv = [agentConfig.Command] + rendered SessionResume (design T2.1). The new
	// job runs as the built-in exec agent so the resume argv executes verbatim; the
	// agent's own Command (e.g. "claude"/"codex") is argv[0].
	argv := append([]string{ac.Command}, agent.Render(ac.SessionResume, agent.Vars{SessionID: src.SessionID, Prompt: prompt})...)

	// E35 (review #5): re-apply the source job's role system prompt on resume. The
	// resume runs via the exec carrier, so Submit's system_inject block (keyed on
	// req.Agent="exec") does NOT fire; and claude's --resume does not by itself
	// restore a previously --append-system-prompt'd prompt. We therefore re-render
	// SystemInject onto the argv explicitly — else the role's behaviour is silently
	// lost across resume. Conservative default (design §5 结论 / §12 待实测): if a
	// real-CLI check later shows --resume already restores the system prompt, drop
	// this and document it.
	if sysPrompt := systemPromptFromRequestJSON(src.RequestJSON); len(ac.SystemInject) > 0 && sysPrompt != "" {
		argv = append(argv, agent.Render(ac.SystemInject, agent.Vars{SystemPrompt: sysPrompt})...)
	}

	return s.Submit(JobRequest{
		ProjectKey: src.ProjectKey,
		Agent:      agent.ExecAgentKey,
		Cmd:        argv,
		Runner:     src.Runner,
		WorkerID:   src.WorkerID,
		// 续接落原 job 的相对 cwd（从 RequestJSON 还原；JobResult.Cwd 是已解析的绝对路径）。
		Cwd:      cwdFromRequestJSON(src.RequestJSON),
		CallerID: callerID,
		// 显式带 SessionID：new job 复用同会话 id（注入/捕获均跳过），链回原会话、可再续。
		SessionID: src.SessionID,
		// 访问门按 SOURCE agent 判定：resume 只是用 exec 载体跑原 agent 的受限续接 argv，
		// 故豁免 exec/allow_exec 门（2026-06-26 决策）。仅 ResumeJob 设置，不入 request_json、不可伪造。
		ResumeSourceAgent: src.Agent,
		// 续接沿用源 job 的提交来源（provenance），保留会话链的原始渠道/来源主机。
		Channel: src.Channel,
		Client:  src.Client,
	})
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

// systemPromptFromRequestJSON recovers the resolved system prompt (role-filled or
// direct) from a job's persisted request_json blob (E35 resume re-application).
// Mirrors cwdFromRequestJSON. A blank / unparseable blob yields "".
func systemPromptFromRequestJSON(s string) string {
	if s == "" {
		return ""
	}
	var r struct {
		SystemPrompt string `json:"system_prompt"`
	}
	_ = json.Unmarshal([]byte(s), &r)
	return r.SystemPrompt
}
