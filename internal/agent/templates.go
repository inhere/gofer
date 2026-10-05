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
		Type:            TypeCLIAgent,
		Command:         "claude",
		GlobalArgs:      []string{},
		Args:            []string{"-p", "--output-format", "stream-json", "--verbose", "{{prompt}}"},
		InteractiveArgs: []string{},
	},
	// codex: non-interactive run. `codex exec` is the CLI's documented
	// "run Codex non-interactively" subcommand.
	"codex": {
		Type:            TypeCLIAgent,
		Command:         "codex",
		GlobalArgs:      []string{"-s", "danger-full-access", "-a", "never"},
		Args:            []string{"exec", "{{prompt}}"},
		InteractiveArgs: []string{},
	},
	// —— acp-agent adapters (ACP-01 §一.1) ——
	//
	// The four known ACP servers. Args are the ACP SERVER's launch argv: the prompt
	// travels over the protocol, so no template carries {{prompt}} and
	// ValidateConfig rejects one that does.
	//
	// Only the npx-launched adapters (claude-acp, codex-acp) need an explicit detect
	// block: npx's own --version reports Node, not the adapter. The others are probed by the
	// default `<command> --version` (design: detect 用各自 --version).
	// The adapter package was renamed from @zed-industries/claude-code-acp (frozen at
	// 0.16.x, bundling Claude Agent SDK 0.2.x). That old SDK omits the session header
	// newer Anthropic-compatible gateways require (real host failure 2026-10-04:
	// `400 MissingSessionID … x-opencode-session` while the claude CLI itself worked).
	"claude-acp": {
		Type:    TypeACPAgent,
		Command: "npx",
		Args:    []string{"-y", "@agentclientprotocol/claude-agent-acp"},
		Detect:  config.DetectConfig{Command: "npx", Args: []string{"-y", "@agentclientprotocol/claude-agent-acp", "--version"}},
		// F12 (ACP-02 真机验收): claude-code-acp 的会话模式是
		// default / acceptEdits / plan / bypassPermissions，只读对应 plan。没有这条映射，
		// `job run -a claude-acp --read-only` 会被准入直接拒掉（"has no read-only mode"），
		// 只读对 claude-acp 就等于不可用。
		//
		// F14：claude-acp 走的是 Claude Agent SDK，它在会话前自己解析凭据，
		// **读不到** claude CLI 的 `~/.claude/settings.json` 的 env 块（真机症状：
		// `-32000 Authentication required`）。模板默认打开该继承，key 就只需要维护一处；
		// 显式 `claude_settings_env: false` 可关。
		ACP: &config.ACPConfig{
			Modes:             map[string]string{"read_only": "plan"},
			ClaudeSettingsEnv: boolRef(true),
		},
	},
	// codex-acp: @zed-industries/codex-acp is deprecated, replaced by
	// @agentclientprotocol/codex-acp (npm 2.1.x; bundles a compatible @openai/codex).
	// A bare `codex-acp` was never on the host PATH, so the template was never
	// injected. Like claude-acp it runs through npx, hence the explicit detect block
	// (npx's own --version reports Node). Verified from the 2.1.1 package: `--version`
	// prints "@agentclientprotocol/codex-acp 2.1.1"; initialize advertises
	// loadSession:true; the session modes are read-only / workspace-write / agent /
	// agent-full-access, so read-only maps to `read-only`.
	"codex-acp": {
		Type:    TypeACPAgent,
		Command: "npx",
		Args:    []string{"-y", "@agentclientprotocol/codex-acp"},
		Detect:  config.DetectConfig{Command: "npx", Args: []string{"-y", "@agentclientprotocol/codex-acp", "--version"}},
		ACP: &config.ACPConfig{
			Modes: map[string]string{"read_only": "read-only"},
		},
	},
	"gemini-acp": {
		Type:    TypeACPAgent,
		Command: "gemini",
		Args:    []string{"--acp"},
	},
	"omp-acp": {
		Type:    TypeACPAgent,
		Command: "omp",
		Args:    []string{"acp"},
	},
	// jcode-acp: the Jcode CLI's own ACP adapter (`jcode acp`, backed by the Jcode
	// daemon). Verified against jcode 0.85.0 over stdio: initialize answers
	// protocolVersion 1 with loadSession + sessionCapabilities{close,resume},
	// session/new returns a session id plus a model configOptions block, and a
	// session/prompt streams agent_message_chunk and ends with stopReason=end_turn.
	//
	// No detect block (the rule above): the default `<command> --version` probe
	// already yields a clean "jcode v0.85.0 (<hash>)". Note `jcode version` — the
	// subcommand — is NOT a substitute: its first line is "version\tv0.85.0 (<hash>)",
	// noisier for no gain.
	"jcode-acp": {
		Type:    TypeACPAgent,
		Command: "jcode",
		Args:    []string{"acp"},
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

// boolRef returns a pointer to b, for the *bool template fields whose DEFAULT is the
// point (F14: `claude_settings_env: true`). A plain `true` cannot express "explicitly
// on" in those fields — unset and false are the same value — which is exactly the
// distinction an operator's override and the runner both act on.
func boolRef(b bool) *bool { return &b }
