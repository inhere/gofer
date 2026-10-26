package job

import (
	"strings"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/i18n/prompts"
)

// The work-contract sections Submit appends to an agent's prompt (gofer-3nxa.4 / .3).
// The header lines double as the "already injected" markers: a rerun replays a
// persisted prompt that already carries them, and a peer re-submit hands over the
// submitting side's prompt, so each section is appended only when its header is not
// there yet.
//
// The text itself lives in internal/i18n: the sections are prompt templates
// (internal/i18n/prompts, one per language) and the headings — protocol text gofer
// reads back — are in internal/i18n/protocol.go, which also lists every spelling
// that counts as "the section is there" (design 2026-10-27-i18n-design.md §4 / §5).
// The variables below are the zh-CN spellings of those headings, for the code that
// composes or recognizes a prompt written in the deployment language.
var (
	acceptanceSectionHeader = i18n.Title(i18n.ZhCN, i18n.SectionAcceptance)
	scopeSectionHeader      = i18n.Title(i18n.ZhCN, i18n.SectionContract)
	deadlineLinePrefix      = i18n.Title(i18n.ZhCN, i18n.SectionDeadline)
)

// promptLanguage is the language gofer writes req's text in: the one fixed when
// the job was admitted (so a worker agrees with the hub), else the job's project /
// server language.
func promptLanguage(cfg *config.Config, req *JobRequest) i18n.Lang {
	if l, ok := i18n.Normalize(req.Language); ok {
		return l
	}
	return cfg.LanguageFor(req.ProjectKey)
}

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
	lang := promptLanguage(cfg, req)
	acceptance := strings.TrimSpace(req.Acceptance)
	if acceptance != "" && !hasPromptSection(cur, i18n.SectionAcceptance) {
		parts.acceptance = prompts.MustRender(lang, prompts.NameAcceptance, prompts.AcceptanceData{Acceptance: acceptance})
		cur = appendPromptSection(cur, parts.acceptance)
	}
	if batchContractJob(req) {
		project := cfg.Projects[req.ProjectKey]
		scoped := wantContractMode(cfg, req, project.EffectiveScopeDiscipline())
		parts.knowledgeCapture = wantContractMode(cfg, req, project.EffectiveKnowledgeCapture())
		if (scoped || parts.knowledgeCapture) && !hasPromptSection(cur, i18n.SectionContract) {
			data := prompts.ContractData{Scope: scoped, Knowledge: parts.knowledgeCapture}
			if scope := cleanScope(req.Scope); scoped && len(scope) > 0 {
				data.ScopeList = strings.Join(scope, ", ")
			}
			parts.contract = prompts.MustRender(lang, prompts.NameContract, data)
		}
	}
	if limit.Sec > 0 && !limit.Deadline.IsZero() && batchPromptJob(req) &&
		!containsSectionTitle(existing, i18n.SectionDeadline) {
		parts.deadline = jobDeadlineLine(lang, limit)
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

// hasPromptSection reports whether prompt already carries a heading of section,
// in ANY known language: a rerun replays a prompt written under the language of
// its submit, which the executing machine may since have changed.
func hasPromptSection(prompt string, section i18n.Section) bool {
	for _, line := range strings.Split(prompt, "\n") {
		line = strings.TrimRight(line, " \t\r")
		for _, title := range i18n.Titles(section) {
			if line == title {
				return true
			}
		}
	}
	return false
}

// containsSectionTitle reports whether prompt carries any known spelling of
// section, anywhere in a line (a marker that is a line OPENING, not a heading).
func containsSectionTitle(prompt string, section i18n.Section) bool {
	for _, title := range i18n.Titles(section) {
		if strings.Contains(prompt, title) {
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

// jobDeadlineLine renders the notice: the limit the way a person reads it (minutes) and
// WHEN it starts counting — from the moment the job actually runs, not from its submit.
//
// gofer-drlm: the line deliberately names no wall clock. The prompt is fixed at submit and
// never rewritten, while the absolute end belongs to the start of execution (execute
// re-derives GOFER_JOB_DEADLINE there); printing a submit-time clock would hand the agent a
// deadline that a long queue — a saturated concurrency slot or a held directory lock, time
// that F12 keeps out of the run — already ate into. The env var is the one source of the
// instant, and it is the executing machine's own.
func jobDeadlineLine(lang i18n.Lang, limit jobLimit) string {
	return prompts.MustRender(lang, prompts.NameDeadline, prompts.DeadlineData{
		Limit: limitText(lang, limit.Sec),
		Env:   jobDeadlineEnv,
	})
}

// limitText renders a limit as whole minutes — the unit a job's timeout is written in —
// falling back to seconds for a limit that is not a whole minute (`--timeout 90`).
func limitText(lang i18n.Lang, sec int64) string {
	if sec%60 == 0 {
		return i18n.T(lang, "job.deadline.minutes", "n", sec/60)
	}
	return i18n.T(lang, "job.deadline.seconds", "n", sec)
}

// appendPromptSection appends section to prompt separated by one blank line.
func appendPromptSection(prompt, section string) string {
	prompt = strings.TrimRight(prompt, " \t\r\n")
	if prompt == "" {
		return section
	}
	return prompt + "\n\n" + section
}
