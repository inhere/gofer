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
// silently flip them). The cost is that overriding one field (say a command path)
// means restating the whole entry, args included — deliberate: a narrow
// command-path override would be a new config surface for unclear gain.
//
// Injected entries are marked on the config (config.MarkInjectedAgents) and stripped
// again before any save, so a template can never be frozen into the operator's file.
//
// `exec` is ALREADY built in (see ExecAgentKey / builtinExecAgent) — it is NOT
// redeclared here: a second definition would make it a config-declared key and thus
// an escape hatch, changing its resolution semantics.
//
// NO `detect` BLOCK IS SET on purpose. Availability comes from a PATH lookup of
// Command (a child process' exit code would false-negative on a slow start / first-run
// wizard / auth prompt, and a false negative silently drops the agent from a worker's
// caps). `detect.command/args` only overrides the best-effort VERSION probe, whose
// default is already `<command> --version` — restating that here would be duplication
// that can drift away from Command, and, on the per-request probe paths, an extra
// child process per agent per call.
//
// Session/system fields are omitted too: applySessionDefaults fills them from
// builtinSessionDefaults, matching on the agent key and falling back to the base name
// of Command for interactive agents — which is how tty-claude / tty-codex inherit the
// claude / codex session defaults without restating them.
var builtinTemplates = map[string]config.AgentConfig{
	// claude: non-interactive run. `-p` (print) plus the stream-json trio so a long
	// run streams progress instead of printing only the final result at the end.
	"claude": {
		Type:    TypeCLIAgent,
		Command: "claude",
		Args:    []string{"-p", "--output-format", "stream-json", "--verbose", "{{prompt}}"},
	},
	// codex: non-interactive run. `codex exec` is the CLI's documented
	// "run Codex non-interactively" subcommand.
	"codex": {
		Type:    TypeCLIAgent,
		Command: "codex",
		Args:    []string{"exec", "{{prompt}}"},
	},
	// opencode: non-interactive run via the `run <prompt>` subcommand.
	"opencode": {
		Type:    TypeCLIAgent,
		Command: "opencode",
		Args:    []string{"run", "{{prompt}}"},
	},
	// tty-claude: the SAME CLI driven interactively. Bare `claude` with no args enters
	// the REPL and the pty owns the session, so there is no {{prompt}} to render — the
	// prompt is typed into the terminal. Interactive+NoRawCmd is not decoration: the
	// job gate rejects an interactive agent that is not no-raw-cmd (or is type exec).
	"tty-claude": {
		Type:        TypeCLIAgent,
		Command:     "claude",
		Interactive: true,
		NoRawCmd:    true,
	},
	// tty-codex: symmetric to tty-claude. Per `codex --help`, "if no subcommand is
	// specified, options will be forwarded to the interactive CLI", so a bare `codex`
	// is the interactive CLI; its `resume` subcommand is what the built-in interactive
	// session-resume template drives.
	//
	// Verified end to end on a real codex (v0.144.1) over the ConPTY backend: the TUI
	// starts, typed input reaches the composer, Enter submits, and the model answers.
	// Two behaviours worth knowing, both codex's own, neither a gofer bug:
	//   - The TUI takes ~20-30s to come up (it starts its MCP servers first). Input
	//     typed before the composer is ready echoes but does not submit.
	//   - On a host that has never signed in, a bare codex lands in codex's sign-in
	//     wizard rather than a session. Pick "Device Code" there: "Sign in with
	//     ChatGPT" opens a browser on the WORKER host, which is useless for a remote
	//     one. This is not specific to tty-codex — `codex exec` fails on such a host
	//     too, and availability is a PATH lookup that says nothing about auth state.
	"tty-codex": {
		Type:        TypeCLIAgent,
		Command:     "codex",
		Interactive: true,
		NoRawCmd:    true,
	},
}
