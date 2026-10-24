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
		if tl.Advisory && tl.Class != Write {
			t.Fatalf("%s: only a write can be advisory (class %q)", tl.Name, tl.Class)
		}
	}
	if readOnly == 0 {
		t.Fatal("registry has no read-only tool")
	}
}

// TestAutoApprovedTools pins the default no-approval list (the approval gate's default
// auto_allow_tools): every read-only tool plus exactly the advice-only writes, each
// entry an exact registry name.
func TestAutoApprovedTools(t *testing.T) {
	want := map[string]bool{
		"gofer_work_list":              true, // read-only
		"gofer_today_list":             true, // read-only
		"gofer_session_mail_read":      true, // read-only bookkeeping
		"gofer_today_advise":           true, // advisory
		"gofer_work_merge_suggest":     true, // advisory
		"gofer_memory_suggest":         true, // advisory
		"gofer_run_job":                false,
		"gofer_work_note":              false, // a write that changes state
		"gofer_steward_notes":          false,
		"gofer_session_mail_reply":     false, // allowed only for a resident session
		"gofer_memory_candidate_adopt": false,
	}
	got := map[string]bool{}
	for _, name := range AutoApprovedTools() {
		if _, ok := Lookup(name); !ok {
			t.Fatalf("AutoApprovedTools lists %q, which is not a registry tool", name)
		}
		if got[name] {
			t.Fatalf("AutoApprovedTools lists %q twice", name)
		}
		got[name] = true
	}
	for name, wantOK := range want {
		if got[name] != wantOK {
			t.Errorf("AutoApprovedTools contains %q = %v, want %v", name, got[name], wantOK)
		}
	}
	// Every read-only tool is in the list: nothing to ask a human about.
	for _, tl := range All() {
		if tl.Class == ReadOnly && !got[tl.Name] {
			t.Errorf("read-only tool %q is missing from AutoApprovedTools", tl.Name)
		}
		if tl.Class != ReadOnly && !tl.Advisory && got[tl.Name] {
			t.Errorf("tool %q (class %q) must stay gated by default", tl.Name, tl.Class)
		}
	}
}

// TestServerOfAndBaseName pins the identity split a permission request's tool name goes
// through: an mcp__<server>__<tool> name reports its server, a bare name reports none,
// and BaseName is the tool segment either way.
func TestServerOfAndBaseName(t *testing.T) {
	cases := []struct {
		raw    string
		server string
		ok     bool
		base   string
	}{
		{"mcp__gofer__gofer_work_list", "gofer", true, "gofer_work_list"},
		{"mcp__other__gofer_work_list", "other", true, "gofer_work_list"},
		{"MCP__Gofer__Gofer_Today_List", "gofer", true, "gofer_today_list"},
		{"gofer_work_list", "", false, "gofer_work_list"},
		{"  gofer_work_list  ", "", false, "gofer_work_list"},
		{"other__gofer_work_list", "", false, "other__gofer_work_list"},
		{"mcp__gofer__", "gofer", true, ""}, // no tool segment: an empty tool name
		{"", "", false, ""},
	}
	for _, tc := range cases {
		if srv, ok := ServerOf(tc.raw); srv != tc.server || ok != tc.ok {
			t.Errorf("ServerOf(%q) = %q,%v want %q,%v", tc.raw, srv, ok, tc.server, tc.ok)
		}
		if got := BaseName(tc.raw); got != tc.base {
			t.Errorf("BaseName(%q) = %q want %q", tc.raw, got, tc.base)
		}
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
		name, ok := ToolName(tc.raw)
		if got := ok && IsReadOnly(name); got != tc.ok {
			t.Errorf("IsReadOnly(ToolName(%q)) = %v want %v", tc.raw, got, tc.ok)
		}
	}
}
