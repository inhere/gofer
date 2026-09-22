package config

import (
	"reflect"
	"strings"
	"testing"
)

// serverYAMLNames maps every exported ServerConfig field name to its yaml name.
func serverYAMLNames(t *testing.T) map[string]string {
	t.Helper()
	rt := reflect.TypeOf(ServerConfig{})
	out := make(map[string]string, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		name := strings.Split(f.Tag.Get("yaml"), ",")[0]
		if name == "" || name == "-" {
			t.Fatalf("ServerConfig.%s has no usable yaml tag %q", f.Name, f.Tag.Get("yaml"))
		}
		out[f.Name] = name
	}
	return out
}

// TestEveryServerFieldHasPolicy is the guard the design's risk section asks for
// (R3): the field policy table is hand-maintained, so a NEW ServerConfig field
// nobody classified would show up on the console with neither an "editable" nor a
// "needs a restart" answer — and worse, the write endpoint would refuse it with a
// message that does not explain why. Every exported field must therefore have an
// explicit entry.
//
// The table is deliberately NOT built by reflection (that would auto-classify a new
// field as editable); this test only proves the two never drift.
func TestEveryServerFieldHasPolicy(t *testing.T) {
	names := serverYAMLNames(t)
	if len(names) < 20 {
		t.Fatalf("reflection found only %d ServerConfig fields — the guard is not looking at the struct", len(names))
	}
	for field, yamlName := range names {
		fp, ok := FieldPolicyFor("server." + yamlName)
		if !ok {
			t.Errorf("no field policy for server.%s (ServerConfig.%s): classify it in editable.go", yamlName, field)
			continue
		}
		if !fp.Editable && !fp.RestartRequired {
			t.Errorf("server.%s is neither editable nor restart-required — that pair is the whole contract", yamlName)
		}
	}

	// Reverse direction: a `server.<x>` top-level entry that names no field is a
	// typo, and a typo means the REAL field it was meant for is unclassified (or
	// silently non-editable). Nested paths (server.notification.*) are the blocks'
	// own business and are not held to this check.
	for path := range fieldPolicies {
		rest, ok := strings.CutPrefix(path, "server.")
		if !ok || strings.Contains(rest, ".") {
			continue
		}
		found := false
		for _, yamlName := range names {
			if yamlName == rest {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("field policy %q names no ServerConfig field", path)
		}
	}
}

// TestFieldPolicyLookupByPath pins the lookup contract the write endpoint and the
// console both sit on: dotted yaml paths, `*` standing in for an agent key, and a
// miss (ok=false) for anything the table does not know — a miss must NEVER be read
// as "not editable", or an unknown path would silently pass the whitelist gate.
func TestFieldPolicyLookupByPath(t *testing.T) {
	cases := []struct {
		path     string
		wantOK   bool
		editable bool
		restart  bool
	}{
		{path: "server.max_job_timeout_sec", wantOK: true, editable: true},
		{path: "server.addr", wantOK: true, restart: true},
		{path: "server.token", wantOK: true, restart: true},
		{path: "server.token_env", wantOK: true, restart: true},
		{path: "agents.claude.command", wantOK: true, editable: true},
		{path: "agents.任意键.command", wantOK: true, editable: true},
		{path: "", wantOK: false},
		{path: "server.nope", wantOK: false},
		{path: "agents.k.nope", wantOK: false},
		{path: "projects.seed.host_path", wantOK: false},
	}
	for _, tc := range cases {
		fp, ok := FieldPolicyFor(tc.path)
		if ok != tc.wantOK {
			t.Errorf("FieldPolicyFor(%q) ok=%v, want %v", tc.path, ok, tc.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if fp.Editable != tc.editable {
			t.Errorf("FieldPolicyFor(%q).Editable=%v, want %v", tc.path, fp.Editable, tc.editable)
		}
		if fp.RestartRequired != tc.restart {
			t.Errorf("FieldPolicyFor(%q).RestartRequired=%v, want %v", tc.path, fp.RestartRequired, tc.restart)
		}
	}
}

// TestEditableAgentFieldsWhitelist pins the agent side of the table: the console
// builds its form from this list and the write endpoint refuses anything outside it,
// so a field that silently disappeared from the list would make an existing agent
// definition uneditable from the web — while `env` (which can hold plaintext
// secrets) must never be in it.
func TestEditableAgentFieldsWhitelist(t *testing.T) {
	fields := EditableAgentFields()
	if len(fields) == 0 {
		t.Fatal("EditableAgentFields() is empty")
	}
	has := func(name string) bool {
		for _, f := range fields {
			if f == name {
				return true
			}
		}
		return false
	}
	for _, want := range []string{"command", "args", "interactive_args", "session_capture", "ndjson_keep"} {
		if !has(want) {
			t.Errorf("EditableAgentFields() is missing %q", want)
		}
	}
	for _, never := range []string{"env", "detect", "mcp_server_name"} {
		if has(never) {
			t.Errorf("EditableAgentFields() must not carry %q", never)
		}
	}
	if len(SecretFields()) == 0 {
		t.Fatal("SecretFields() is empty — the write endpoint has no secret markers to refuse")
	}
}
