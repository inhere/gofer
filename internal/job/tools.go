package job

import (
	"strings"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/i18n"
)

// toolsPromptHeader opens the declared-tools section (CFG-16), in lang. Like the rules and
// skills headers it is written for any project: it names what the list is and that
// using it is optional, nothing about gofer's own repository.
func toolsPromptHeader(lang i18n.Lang) string { return i18n.T(lang, "job.tools.header") }

// toolsPromptSection is the declared-tools section for a job THIS machine runs, or
// "" when the job gets none. It is rendered by the executing machine only (the
// caller passes !remote), because only that machine's config knows what is
// installed there: serve-local reads config.yaml's tools, a worker reads its
// worker.yaml's (mapped onto cfg.Tools), and a hub forwarding to a worker or a peer
// renders nothing.
//
// Skipped for:
//   - an exec job — its command is not read by a model;
//   - a continuation (ResumedFrom / ResumeSourceAgent) — the session it continues
//     already saw the section;
//   - `--no-rules` — the existing "no gofer-injected context" switch;
//   - a runner that declares no tools.
//
// RulesResolved / SkillsResolved are deliberately NOT consulted: the worker's
// dispatch re-entry sets both, and that is exactly the path that must render.
func toolsPromptSection(cfg *config.Config, req *JobRequest) string {
	if len(cfg.Tools) == 0 || req.NoRules || req.ResumedFrom != "" || req.ResumeSourceAgent != "" {
		return ""
	}
	ac, ok := agent.ResolveAgent(cfg, req.Agent)
	if !ok || ac.Type == agent.TypeExec {
		return ""
	}
	// A dispatched job carries the id of the worker it runs on in its env (set by
	// the worker's dispatch, which is also what marks CredentialExternal); a
	// server-local job has none, so a caller-supplied env value is not trusted here.
	workerID := ""
	if req.CredentialExternal {
		workerID = strings.TrimSpace(req.Env[EnvWorkerID])
	}
	return renderToolsSection(promptLanguage(cfg, req), cfg.Tools, workerID)
}

// renderToolsSection renders the header, one intro line (naming the worker when
// there is one) and one line per tool (config.ToolLine, the same text the config's
// size cap measured).
func renderToolsSection(lang i18n.Lang, tools []config.Tool, workerID string) string {
	var b strings.Builder
	b.WriteString(toolsPromptHeader(lang))
	b.WriteString("\n\n")
	if workerID != "" {
		b.WriteString(i18n.T(lang, "job.tools.intro_worker", "worker", workerID))
		b.WriteString("\n\n")
	} else {
		b.WriteString(i18n.T(lang, "job.tools.intro_local"))
		b.WriteString("\n\n")
	}
	for _, t := range tools {
		b.WriteString(config.ToolLine(t))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}
