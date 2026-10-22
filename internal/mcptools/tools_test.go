package mcptools

import "testing"

// TestRegistryIsConsistent guards the two properties the registry must have: one entry
// per tool and at least one read-only tool (an empty read-only set would silently turn
// the approval gate's new rule into a no-op).
func TestRegistryIsConsistent(t *testing.T) {
	seen := map[string]bool{}
	readOnly := 0
	for _, tl := range All() {
		if tl.Name == "" {
			t.Fatal("registry has an entry with an empty name")
		}
		if seen[tl.Name] {
			t.Fatalf("registry lists %q twice", tl.Name)
		}
		seen[tl.Name] = true
		switch tl.Class {
		case ReadOnly:
			readOnly++
		case Write, Destructive:
		default:
			t.Fatalf("%s: unknown class %q", tl.Name, tl.Class)
		}
		if tl.Idempotent && tl.Class == ReadOnly {
			t.Fatalf("%s: read-only tools are idempotent implicitly; drop the flag", tl.Name)
		}
	}
	if readOnly == 0 {
		t.Fatal("registry has no read-only tool")
	}
}

// TestToolNameResolution pins how an ACP tool-call title is resolved: bare gofer names
// and the mcp__gofer__<tool> shape are gofer's tools, a name qualified by another
// server is not (the approval gate must not be fooled by a look-alike), and a write
// tool never counts as read-only.
func TestToolNameResolution(t *testing.T) {
	cases := []struct {
		raw  string
		name string
		ok   bool
	}{
		{"gofer_work_list", "gofer_work_list", true},
		{"  gofer_work_list  ", "gofer_work_list", true},
		{`"gofer_today_list"`, "gofer_today_list", true},
		{"Gofer_Work_List", "gofer_work_list", true},
		{"mcp__gofer__gofer_work_list", "gofer_work_list", true},
		{"mcp__gofer__gofer_get_job", "gofer_get_job", true},
		{"mcp__other__gofer_work_list", "", false},
		{"mcp__gofer__not_a_gofer_tool", "", false},
		{"other__gofer_work_list", "", false},
		{"gofer_run_job", "gofer_run_job", true}, // resolves, but see the read-only case
		{"run_job", "", false},
		{"", "", false},
		{"gofer_", "", false},
	}
	for _, tc := range cases {
		got, ok := ToolName(tc.raw)
		if got != tc.name || ok != tc.ok {
			t.Errorf("ToolName(%q) = %q,%v want %q,%v", tc.raw, got, ok, tc.name, tc.ok)
		}
	}
	readOnly := []struct {
		raw string
		ok  bool
	}{
		{"gofer_work_list", true},
		{"mcp__gofer__gofer_today_list", true},
		{"gofer_run_job", false},               // a write tool
		{"gofer_memory_rm", false},             // destructive
		{"mcp__other__gofer_work_list", false}, // another server's tool
		{"gofer_list", false},                  // not a registered tool
		{"gofer_work", false},                  // prefix of a registered tool is not one
	}
	for _, tc := range readOnly {
		name, ok := ReadOnlyToolName(tc.raw)
		if ok != tc.ok {
			t.Errorf("ReadOnlyToolName(%q) = %q,%v want ok=%v", tc.raw, name, ok, tc.ok)
		}
	}
}
