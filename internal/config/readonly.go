package config

import "strings"

// builtinReadOnlyArgs is the read-only argv each well-known agent CLI accepts, keyed
// by agent name (bd h-aii-0ql3 / docs/design/2026-09-17-acp-agent-and-approval-gate-
// design.md §S2). A `job run --read-only` on a cli-agent appends these to the argv,
// which is how the flag becomes an enforced sandbox instead of a promise.
//
// Every entry was verified against the CLI's own --help:
//
//   - codex: `-s, --sandbox <SANDBOX_MODE>` [read-only | workspace-write |
//     danger-full-access] — read-only is exactly the requested mode.
//   - claude: `--permission-mode <mode>` (choices acceptEdits, auto,
//     bypassPermissions, manual, dontAsk, plan) — there is no pure read-only mode;
//     plan is the closest (the session proposes changes instead of applying them).
//   - suga: `--read-only, --ro  Disable every mutating tool` — and it WINS over the
//     `--allow-write` the template carries for batch jobs (`AllowWrite =
//     (--allow-write || tools.allow_write) && !--read-only`), so a job admitted with
//     --read-only cannot write even then.
//
// omp is deliberately ABSENT: `omp --help` has no read-only switch (`--plan-yolo`
// forces read-only plan mode but then auto-approves the plan and switches to
// implementing it, which is not what --read-only promises), so the operator must
// declare `read_only_args` for it instead of inheriting a guess.
//
// G031: generic agent CLIs only — no business-specific agent belongs here.
var builtinReadOnlyArgs = map[string][]string{
	"codex":  {"-s", "read-only"},
	"claude": {"--permission-mode", "plan"},
	"suga":   {"--read-only"},
}

// BuiltinReadOnlyArgs returns the built-in read-only argv for an agent name, or nil
// when that name has none. The caller decides which names to try: the agent's config
// key first, then the base name of its command (agent.applyReadOnlyDefaults), so an
// operator-renamed agent still inherits its CLI's sandbox flags. The result is a copy
// — a caller must never be able to mutate the table.
func BuiltinReadOnlyArgs(name string) []string {
	args, ok := builtinReadOnlyArgs[name]
	if !ok || len(args) == 0 {
		return nil
	}
	return append([]string(nil), args...)
}

// builtinModelArgs is the model-selection argv fragment each well-known cli-agent
// accepts (N1 §B), keyed like builtinReadOnlyArgs. Verified against the CLIs' --help:
// claude `--model <model>`, codex `-m, --model <MODEL>`, suga `--model <model>`
// ("Model, <provider>/<model> or a bare model id"). The fragment is spliced before
// the prompt argument, so for codex it lands after the `exec` subcommand.
var builtinModelArgs = map[string][]string{
	"claude": {"--model", "{{model}}"},
	"codex":  {"-m", "{{model}}"},
	"suga":   {"--model", "{{model}}"},
}

// ImageInput values (AgentConfig.ImageInput).
const (
	// ImageInputPath: the agent opens an image the message NAMES by absolute path.
	ImageInputPath = "path"
	// ImageInputArg: the agent takes an image path as an argv flag instead (the agent's
	// image_args template, one expansion per image), so gofer appends it to the argv of
	// the job that carries the message rather than writing the path into the text.
	ImageInputArg = "arg"
	// ImageInputNone: the agent cannot take an image the console hands it at all.
	ImageInputNone = "none"
)

// builtinImageInput is how each well-known agent takes an image the console hands it,
// keyed like builtinModelArgs (and including the -acp variants, which reach the same model
// through their adapters). Claude Code reads images through its file-read tool and Codex
// through its local-image viewer. suga takes them as `run --image <path>` (v0.40+), so its
// built-in template declares image_input: arg with the image_args template. Relay delivery uses the
// separate built-in deliver_image_args template. suga-acp
// (v0.38+) takes images INLINE when its model can see them (it advertises
// promptCapabilities.image), which needs no entry here.
var builtinImageInput = map[string]string{
	"claude":     ImageInputPath,
	"claude-acp": ImageInputPath,
	"codex":      ImageInputPath,
	"codex-acp":  ImageInputPath,
	"suga":       ImageInputArg,
	"suga-acp":   ImageInputNone,
}

// EffectiveImageInput resolves how agent key takes an image a message carries: the
// agent's own `image_input` wins, then the built-in table, then none. path and arg are
// positive (an image can be delivered); every other value (including a typo that
// validation would reject) is none.
func (c *Config) EffectiveImageInput(key string) string {
	if c != nil {
		if v := strings.ToLower(strings.TrimSpace(c.Agents[key].ImageInput)); v != "" {
			return v
		}
	}
	if v, ok := builtinImageInput[strings.ToLower(strings.TrimSpace(key))]; ok {
		return v
	}
	return ImageInputNone
}

// builtinDeliver is the built-in live-process delivery of a well-known cli-agent,
// keyed like builtinModelArgs: the argv that hands a message to a RUNNING session and
// the optional marker that maps a failure to "the session's process is not running".
//
//   - codex: `codex queue --thread <sid> --message <text>`; an unknown thread exits 1
//     with "no rollout found for thread id", which offlineMatch maps to not-running so
//     the relay ladder continues.
//   - suga: `suga send --session <sid>` reads the text on stdin and already reports
//     the outcomes in gofer's own exit-code contract (0 delivered, 3 the session's
//     process is not online, 4 stdin too large), so no offline marker is needed — the
//     exit code itself says the session is gone. `--source gofer` labels the message
//     in the chat window. It accepts one `--image <path>` per attachment; exit 5 means
//     the target process is too old for images and is a terminal failure — the
//     workbench does not retry without the images.
var builtinDeliver = map[string]struct {
	command      []string
	imageArgs    []string
	stdin        bool
	offlineMatch string
}{
	"codex": {command: []string{"queue", "--thread", "{{session_id}}", "--message", "{{text}}"}, offlineMatch: "no rollout found"},
	"suga":  {command: []string{"send", "--session", "{{session_id}}", "--source", "gofer"}, imageArgs: []string{"--image", "{{image}}"}, stdin: true},
}

// BuiltinDeliver returns the built-in delivery argv, image argv template, whether the text
// travels on stdin, and the offline marker for an agent name. Copies — a caller must never
// be able to mutate the table.
func BuiltinDeliver(name string) (command, imageArgs []string, stdin bool, offlineMatch string) {
	def, ok := builtinDeliver[name]
	if !ok || len(def.command) == 0 {
		return nil, nil, false, ""
	}
	return append([]string(nil), def.command...), append([]string(nil), def.imageArgs...), def.stdin, def.offlineMatch
}

// EffectiveDeliverImageArgs returns the image argv template for a live-process delivery.
func (c *Config) EffectiveDeliverImageArgs(key string) []string {
	if c == nil {
		return nil
	}
	ac, ok := c.Agents[key]
	if !ok {
		return nil
	}
	if len(ac.DeliverCommand) > 0 || ac.DeliverStdin {
		return append([]string(nil), ac.DeliverImageArgs...)
	}
	if len(ac.DeliverImageArgs) > 0 {
		return append([]string(nil), ac.DeliverImageArgs...)
	}
	_, imageArgs, _, _ := BuiltinDeliver(strings.ToLower(strings.TrimSpace(key)))
	return imageArgs
}

// BuiltinModelArgs returns the built-in model_args for an agent name, or nil. A copy.
func BuiltinModelArgs(name string) []string {
	args, ok := builtinModelArgs[name]
	if !ok || len(args) == 0 {
		return nil
	}
	return append([]string(nil), args...)
}

// builtinMCP is how the built-in cli-agents take MCP servers (MCP-06): claude reads a
// generated config file through --mcp-config, codex takes -c mcp_servers.* overrides.
var builtinMCP = map[string]struct {
	format string
	args   []string
}{
	"claude": {MCPFormatClaudeJSON, []string{"--mcp-config", "{{mcp_config_file}}"}},
	"codex":  {MCPFormatCodexConfig, nil},
}

// BuiltinMCP returns the built-in mcp_format / mcp_args for an agent name ("" when none).
func BuiltinMCP(name string) (string, []string) {
	b, ok := builtinMCP[name]
	if !ok {
		return "", nil
	}
	return b.format, append([]string(nil), b.args...)
}
