package job

import (
	"strings"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
)

// The work-contract sections Submit appends to an agent's prompt (gofer-3nxa.4 / .3).
// The header lines double as the "already injected" markers: a rerun replays a
// persisted prompt that already carries them, and a peer re-submit hands over the
// submitting side's prompt, so each section is appended only when its header is not
// there yet.
const (
	acceptanceSectionHeader = "## 验收标准"
	acceptanceSectionTail   = "汇报末尾逐条说明是否满足：满足 / 未满足 / 无法验证，并给出依据（测试名、命令输出、文件位置）。"
)

// injectPromptSections appends the acceptance-criteria section to req.Prompt. It runs
// in Submit right after the task book is rendered.
//
// Skipped:
//   - an exec job: its argv is not a prompt (the criteria are still recorded and shown);
//   - a continuation or a job re-entering from the hub (RulesResolved): the session or
//     the forwarded prompt already carries what the submitting side decided;
//   - a prompt that already contains the section (rerun / peer re-submit).
func injectPromptSections(cfg *config.Config, req *JobRequest) {
	if req.RulesResolved || isExecAgentJob(cfg, req) {
		return
	}
	acceptance := strings.TrimSpace(req.Acceptance)
	if acceptance == "" || hasPromptSection(req.Prompt, acceptanceSectionHeader) {
		return
	}
	req.Prompt = appendPromptSection(req.Prompt,
		acceptanceSectionHeader+"\n\n"+acceptance+"\n\n"+acceptanceSectionTail)
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
