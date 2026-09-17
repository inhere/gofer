package config

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
