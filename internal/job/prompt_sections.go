package job

import (
	"strings"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/tracker"
)

// The work-contract sections Submit appends to an agent's prompt (gofer-3nxa.4 / .3).
// The header lines double as the "already injected" markers: a rerun replays a
// persisted prompt that already carries them, and a peer re-submit hands over the
// submitting side's prompt, so each section is appended only when its header is not
// there yet.
const (
	acceptanceSectionHeader = "## 验收标准"
	acceptanceSectionTail   = "汇报末尾逐条说明是否满足：满足 / 未满足 / 无法验证，并给出依据（测试名、命令输出、文件位置）。"

	scopeSectionHeader = "## 交付约定"
	scopeSectionBody   = "- 只改与本任务直接相关的代码和文档；不要顺手重构、改名或清理范围外的内容。\n" +
		"- 范围外发现的问题（bug、坏味道、过时文档等）不要修改，写进汇报末尾的「## 发现但不碰」小节，每条一行：`- <位置>：<问题>`。没有就省略该小节。"
	scopeSectionScopeLine = "- 本任务的改动范围："

	// knowledgeSectionLine asks for the 「## 可复用经验」 report section (gofer-3nxa.2):
	// the server turns its items into memory candidates (see captureKnowledge).
	knowledgeSectionLine = "- 跨任务可复用的经验（踩坑、约定、验证技巧）写进汇报末尾的「## " + KnowledgeSectionTitle +
		"」小节，每条一行；只写以后其他任务也用得上的，不写本次业务细节。没有就省略该小节。"
	// memoryFlagLine asks the agent to report injected knowledge that turned out wrong
	// instead of silently working around it (gofer-3nxa.1 ships `memory flag`).
	memoryFlagLine = "- " + tracker.MemoryFlagHint
)

// injectPromptSections appends the acceptance-criteria section and then the
// 「交付约定」 section to req.Prompt. It runs in Submit right after the task book is
// rendered. The 「交付约定」 section carries the scope-discipline lines
// (scope_discipline), the 「## 可复用经验」 request (knowledge_capture) and the
// memory-flag line; it is appended when either of the two modes applies. The
// knowledge-capture decision is recorded on req.KnowledgeCapture, which is what makes
// the job's finish parse the report into memory candidates.
//
// Skipped for both:
//   - an exec job: its argv is not a prompt (the criteria are still recorded and shown);
//   - a continuation or a job re-entering from the hub (RulesResolved): the session or
//     the forwarded prompt already carries what the submitting side decided (a
//     continuation inherits KnowledgeCapture from its source; a worker's copy never
//     carries it — the hub captures);
//   - a prompt that already contains the section (rerun / peer re-submit). Its
//     KnowledgeCapture is still decided, so a rerun captures like the original.
func injectPromptSections(cfg *config.Config, req *JobRequest) {
	if req.RulesResolved {
		return
	}
	req.KnowledgeCapture = false
	if isExecAgentJob(cfg, req) {
		return
	}
	acceptance := strings.TrimSpace(req.Acceptance)
	if acceptance != "" && !hasPromptSection(req.Prompt, acceptanceSectionHeader) {
		req.Prompt = appendPromptSection(req.Prompt,
			acceptanceSectionHeader+"\n\n"+acceptance+"\n\n"+acceptanceSectionTail)
	}
	if !batchContractJob(req) {
		return
	}
	project := cfg.Projects[req.ProjectKey]
	scoped := wantContractMode(cfg, req, project.EffectiveScopeDiscipline())
	req.KnowledgeCapture = wantContractMode(cfg, req, project.EffectiveKnowledgeCapture())
	if (!scoped && !req.KnowledgeCapture) || hasPromptSection(req.Prompt, scopeSectionHeader) {
		return
	}
	var lines []string
	if scoped {
		lines = append(lines, scopeSectionBody)
	}
	if req.KnowledgeCapture {
		lines = append(lines, knowledgeSectionLine)
	}
	lines = append(lines, memoryFlagLine)
	if scope := cleanScope(req.Scope); scoped && len(scope) > 0 {
		lines = append(lines, scopeSectionScopeLine+strings.Join(scope, ", "))
	}
	req.Prompt = appendPromptSection(req.Prompt, scopeSectionHeader+"\n\n"+strings.Join(lines, "\n"))
}

// batchContractJob reports whether a (non-exec, not re-entering) job can get the
// 「交付约定」 section at all (gofer-3nxa.3): only BATCH agent jobs do — an interactive
// pty or an ACP --session is a conversation, and messenger relays / the steward are
// gofer's own plumbing — and the job-level opt-out (--no-scope-discipline) drops the
// whole section, knowledge capture included.
func batchContractJob(req *JobRequest) bool {
	if req.Interactive || req.Session || req.Steward || req.MessengerMeta != nil || req.Messenger != nil {
		return false
	}
	return !req.NoScopeDiscipline
}

// wantContractMode applies one project mode (scope_discipline / knowledge_capture,
// both auto|on|off) to a batch agent job: off = never, on = always, auto (default) =
// plan-todo jobs, review-gated jobs and jobs that carry acceptance criteria or a
// declared scope — the work somebody will check.
func wantContractMode(cfg *config.Config, req *JobRequest, mode string) bool {
	switch mode {
	case config.ScopeDisciplineOff:
		return false
	case config.ScopeDisciplineOn:
		return true
	}
	return req.TodoID != "" || reviewRequested(cfg, req) ||
		strings.TrimSpace(req.Acceptance) != "" || len(cleanScope(req.Scope)) > 0
}

// cleanScope trims the declared globs and drops empty ones.
func cleanScope(in []string) []string {
	out := make([]string, 0, len(in))
	for _, g := range in {
		if g = strings.TrimSpace(g); g != "" {
			out = append(out, g)
		}
	}
	return out
}

// isExecAgentJob reports whether the job runs an exec agent (an argv, no prompt). An
// agent this machine does not know (a worker-only agent) counts as exec only when the
// request carries an argv.
func isExecAgentJob(cfg *config.Config, req *JobRequest) bool {
	if ac, ok := agent.ResolveAgent(cfg, req.Agent); ok {
		return ac.Type == agent.TypeExec
	}
	return len(req.Cmd) > 0
}

// hasPromptSection reports whether prompt has a line that is exactly header.
func hasPromptSection(prompt, header string) bool {
	for _, line := range strings.Split(prompt, "\n") {
		if strings.TrimRight(line, " \t\r") == header {
			return true
		}
	}
	return false
}

// appendPromptSection appends section to prompt separated by one blank line.
func appendPromptSection(prompt, section string) string {
	prompt = strings.TrimRight(prompt, " \t\r\n")
	if prompt == "" {
		return section
	}
	return prompt + "\n\n" + section
}
