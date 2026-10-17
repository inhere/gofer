package job

import (
	"fmt"
	"strings"
	"time"

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

	// deadlineLinePrefix opens the one-line time-limit notice the executing machine
	// appends to a batch job's prompt (gofer-s2er) and doubles as its "already
	// appended" marker, like the section headers above: a rerun or a peer re-submit of
	// a prompt that already carries the line must not stack a second copy.
	deadlineLinePrefix = "本 job 时限"
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
	parts := promptSectionParts(cfg, req, req.Agent, jobLimit{}, req.Prompt)
	req.KnowledgeCapture = parts.knowledgeCapture
	for _, section := range []string{parts.acceptance, parts.contract} {
		if section != "" {
			req.Prompt = appendPromptSection(req.Prompt, section)
		}
	}
}

// promptParts is what promptSectionParts decided: the text of each work-contract
// section ("" = that section does not apply) and the knowledge-capture decision.
type promptParts struct {
	acceptance       string
	contract         string
	deadline         string
	knowledgeCapture bool
}

// promptSectionParts decides the acceptance, 「交付约定」 and time-limit sections of a
// job without touching the request. It is the one place those rules live: Submit's
// injectPromptSections / appendJobDeadlineLine apply the parts at their own moments
// (after the task book / as the prompt's last line), and buildPromptSections joins
// them for a caller that assembles a whole prompt itself.
//
// realAgent is the agent that really runs the work, which decides "exec job?" — a
// continuation may travel through an exec carrier whose own agent says nothing about
// the work. existing is the prompt the sections would be appended to; a section whose
// marker it already carries is not produced again. limit is the job's resolved time
// limit (zero = no deadline line). The submit-context skips (RulesResolved, resumed
// requests) are the callers' business, not decided here.
func promptSectionParts(cfg *config.Config, req *JobRequest, realAgent string, limit jobLimit, existing string) promptParts {
	var parts promptParts
	if isExecRealAgent(cfg, realAgent, req) {
		return parts
	}
	cur := existing
	acceptance := strings.TrimSpace(req.Acceptance)
	if acceptance != "" && !hasPromptSection(cur, acceptanceSectionHeader) {
		parts.acceptance = acceptanceSectionHeader + "\n\n" + acceptance + "\n\n" + acceptanceSectionTail
		cur = appendPromptSection(cur, parts.acceptance)
	}
	if batchContractJob(req) {
		project := cfg.Projects[req.ProjectKey]
		scoped := wantContractMode(cfg, req, project.EffectiveScopeDiscipline())
		parts.knowledgeCapture = wantContractMode(cfg, req, project.EffectiveKnowledgeCapture())
		if (scoped || parts.knowledgeCapture) && !hasPromptSection(cur, scopeSectionHeader) {
			var lines []string
			if scoped {
				lines = append(lines, scopeSectionBody)
			}
			if parts.knowledgeCapture {
				lines = append(lines, knowledgeSectionLine)
			}
			lines = append(lines, memoryFlagLine)
			if scope := cleanScope(req.Scope); scoped && len(scope) > 0 {
				lines = append(lines, scopeSectionScopeLine+strings.Join(scope, ", "))
			}
			parts.contract = scopeSectionHeader + "\n\n" + strings.Join(lines, "\n")
		}
	}
	if limit.Sec > 0 && !limit.Deadline.IsZero() && batchPromptJob(req) &&
		!strings.Contains(existing, deadlineLinePrefix) {
		parts.deadline = jobDeadlineLine(limit)
	}
	return parts
}

// buildPromptSections returns the text of the work-contract sections for a job —
// acceptance, 「交付约定」 (scope discipline, knowledge capture, memory flag) and the
// time-limit line, in the order Submit appends them, joined by blank lines — plus
// whether the job's report is captured as knowledge candidates. realAgent is the agent
// that really runs the work (decides "exec job?"); limit comes from effectiveTimeout.
// req.Prompt is the task book the sections would follow: a section it already carries
// is not repeated.
func buildPromptSections(cfg *config.Config, req *JobRequest, realAgent string, limit jobLimit) (text string, knowledgeCapture bool) {
	parts := promptSectionParts(cfg, req, realAgent, limit, req.Prompt)
	var out []string
	for _, section := range []string{parts.acceptance, parts.contract, parts.deadline} {
		if section != "" {
			out = append(out, section)
		}
	}
	return strings.Join(out, "\n\n"), parts.knowledgeCapture
}

// effectiveTimeout resolves the limit a job is ADMITTED with — normalizeTimeout under the
// project ceiling, then jobTimeLimit at now — exactly the numbers Submit persists and
// quotes in the prompt's time-limit line. clamped reports that the request was truncated
// to the ceiling.
func effectiveTimeout(cfg *config.Config, req *JobRequest, now time.Time) (timeout time.Duration, clamped bool, limit jobLimit) {
	timeout, clamped = admittedTimeout(cfg, req)
	return timeout, clamped, jobTimeLimit(now, timeout, req, 0)
}

// admittedTimeout is the normalizeTimeout half of effectiveTimeout: the default applied
// and the request clamped to the configured ceiling.
func admittedTimeout(cfg *config.Config, req *JobRequest) (time.Duration, bool) {
	return normalizeTimeout(req.TimeoutSec, req.Interactive,
		isAgentJob(cfg, req.Agent), cfg.EffectiveMaxTimeoutSec(req.ProjectKey))
}

// batchContractJob reports whether a (non-exec, not re-entering) job can get the
// 「交付约定」 section at all (gofer-3nxa.3): only BATCH agent jobs do — an interactive
// pty or an ACP --session is a conversation, and messenger relays / the steward are
// gofer's own plumbing — and the job-level opt-out (--no-scope-discipline) drops the
// whole section, knowledge capture included.
func batchContractJob(req *JobRequest) bool {
	return batchPromptJob(req) && !req.NoScopeDiscipline
}

// batchPromptJob reports whether req is a batch agent job — a single work order the
// agent reads once: not a conversation (an interactive pty or a resident ACP session)
// and not one of gofer's own plumbing jobs (the steward, a messenger relay). It is the
// shape every prompt section is written for; batchContractJob adds the 「交付约定」
// opt-out on top of it, and a caller that must also exclude an exec argv checks
// isExecAgentJob itself (an exec job's command is not a prompt either way).
func batchPromptJob(req *JobRequest) bool {
	return !req.Interactive && !req.Session && !req.Steward && req.MessengerMeta == nil && req.Messenger == nil
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
	return isExecRealAgent(cfg, req.Agent, req)
}

// isExecRealAgent is isExecAgentJob for an explicitly named agent: the one that really
// runs the work, which is not req.Agent when the request travels through an exec
// carrier. req only supplies the argv fallback for an agent this machine does not know.
func isExecRealAgent(cfg *config.Config, realAgent string, req *JobRequest) bool {
	if ac, ok := agent.ResolveAgent(cfg, realAgent); ok {
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

// appendJobDeadlineLine appends the job's own time limit as the LAST line of the prompt
// the executing machine runs with (gofer-s2er). An agent has no other way to learn how
// long the run has: without this line the field reported a step-limit wrap-up as
// 「超时提前收尾」 on a job that still had half an hour to go.
//
// It is deliberately independent of the 「交付约定」 modes (scope_discipline /
// knowledge_capture): the limit is a fact of THIS run, not the discipline somebody will
// check. Skipped for:
//   - a job with no limit at all (an interactive pty without an explicit timeout) and
//     an exec job (its argv is not a prompt);
//   - an interactive pty, a resident ACP session, the steward and a messenger relay —
//     a conversation's clock restarts every turn, and gofer's own plumbing is not the
//     reader this line is written for;
//   - RulesResolved — the submitting side already fixed this prompt (a continuation's
//     session carries what it was told; a dispatch re-entry runs the prompt the hub just
//     wrote, line included), the same reading resolveRules gives it;
//   - a continuation (belt and braces: it also sets RulesResolved);
//   - a prompt that already carries the line (a peer re-submitting the hub's own prompt).
func appendJobDeadlineLine(cfg *config.Config, req *JobRequest, prompt string, limit jobLimit) string {
	if req.RulesResolved || req.ResumedFrom != "" || req.ResumeSourceAgent != "" {
		return prompt
	}
	if line := promptSectionParts(cfg, req, req.Agent, limit, prompt).deadline; line != "" {
		return prompt + "\n\n" + line
	}
	return prompt
}

// jobDeadlineLine renders the notice: the limit the way a person reads it (minutes)
// plus the wall clock it ends at, in the job machine's own timezone — the agent can
// then look at its own clock instead of doing arithmetic on the env var.
func jobDeadlineLine(limit jobLimit) string {
	return fmt.Sprintf("%s %s，截止 %s（%s）。剩余时间看 $%s 或运行 gofer job show $GOFER_JOB_ID；接近时限时先提交已完成的部分，汇报里写清未完成项。",
		deadlineLinePrefix, limitText(limit.Sec), limit.Deadline.Format("15:04"), limit.Deadline.Format("-07:00"), jobDeadlineEnv)
}

// limitText renders a limit as whole minutes — the unit a job's timeout is written in —
// falling back to seconds for a limit that is not a whole minute (`--timeout 90`).
func limitText(sec int64) string {
	if sec%60 == 0 {
		return fmt.Sprintf("%d 分钟", sec/60)
	}
	return fmt.Sprintf("%d 秒", sec)
}

// appendPromptSection appends section to prompt separated by one blank line.
func appendPromptSection(prompt, section string) string {
	prompt = strings.TrimRight(prompt, " \t\r\n")
	if prompt == "" {
		return section
	}
	return prompt + "\n\n" + section
}
