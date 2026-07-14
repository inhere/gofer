package agent

import "github.com/inhere/gofer/internal/config"

// builtinTemplates are the built-in agent definitions Resolve materializes into a
// config at runtime, so a fresh host needs no hand-written `agents:` block: a
// template key is injected only when (a) the operator did NOT declare that key and
// (b) the Detector reports its CLI is actually present on this host.
//
// IRON RULE — an ESCAPE-HATCH agent (one the operator declared in the config) is
// NEVER removed because a probe failed; only template-injected agents are
// detect-gated. A name clash is won by the escape hatch ENTIRELY (whole-entry
// override, never a field-level merge: Interactive/NoRawCmd are plain bools, so
// "unset" is indistinguishable from "explicit false" and a partial merge would
// silently flip them).
//
// Injected entries are marked on the config (config.MarkInjectedAgents) and stripped
// again before any save, so a template can never be frozen into the operator's file.
//
// TODO(T1): complete the table — codex / opencode / tty-claude / tty-codex.
// `exec` is ALREADY built in (see ExecAgentKey / builtinExecAgent) — do NOT redeclare
// it here.
var builtinTemplates = map[string]config.AgentConfig{
	// claude: non-interactive run. `-p` (print) plus the stream-json trio so a long
	// run streams progress instead of printing only the final result at the end.
	"claude": {
		Type:    TypeCLIAgent,
		Command: "claude",
		Args:    []string{"-p", "--output-format", "stream-json", "--verbose", "{{prompt}}"},
	},
}
