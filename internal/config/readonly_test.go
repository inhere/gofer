package config

import (
	"reflect"
	"testing"
)

// TestReadOnlyArgsBuiltinDefaults pins the built-in read-only argv table (bd
// h-aii-0ql3): the flags are the ones the CLIs actually document, they are copied
// out (a caller cannot mutate the table), and agents with no read-only mode get
// nothing rather than a guess. omp is deliberately absent — `omp --help` offers no
// pure read-only/plan switch (`--plan-yolo` forces read-only plan mode but then
// auto-approves and switches to implementation), so a user must declare its own.
func TestReadOnlyArgsBuiltinDefaults(t *testing.T) {
	cases := []struct {
		name string
		want []string
	}{
		// codex: `codex --help` → `-s, --sandbox <SANDBOX_MODE>` [read-only |
		// workspace-write | danger-full-access].
		{"codex", []string{"-s", "read-only"}},
		// claude: `claude --help` → `--permission-mode <mode>` (choices acceptEdits,
		// auto, bypassPermissions, manual, dontAsk, plan); plan is the read-only-ish
		// mode (no edits are proposed).
		{"claude", []string{"--permission-mode", "plan"}},
		{"omp", nil},
		{"gemini", nil},
		{"exec", nil},
		{"claude-acp", nil},
		{"nosuchagent", nil},
	}
	for _, tc := range cases {
		got := BuiltinReadOnlyArgs(tc.name)
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("BuiltinReadOnlyArgs(%q) = %#v, want %#v", tc.name, got, tc.want)
		}
		if len(got) > 0 {
			got[0] = "mutated"
			if again := BuiltinReadOnlyArgs(tc.name); again[0] == "mutated" {
				t.Fatalf("BuiltinReadOnlyArgs(%q) exposed the built-in table to mutation", tc.name)
			}
		}
	}
}
