package config

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeProjectWithInteractiveKey writes a project block that still carries the
// REMOVED AGT-02 key; listLine is spliced in verbatim (already indented) or left
// empty, so both the "key present" and "key absent" shapes come from the same file
// an operator has on disk.
func writeProjectWithInteractiveKey(t *testing.T, listLine string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	write(t, p, `
projects:
  demo:
    host_path: /tmp/demo
    allowed_agents: [term]
`+listLine+`
agents:
  term:
    type: cli
    command: bash
    interactive: true
    no_raw_cmd: true
`)
	return p
}

// TestLoadRejectsInteractiveAllowedAgents pins the v0.48 end of the AGT-02 0.3
// decision: `interactive_allowed_agents` used to be read once at load (a non-empty
// list was carried over as allow_interactive). That one-shot read is gone, and the
// key is now a LOAD ERROR naming its replacement — the typed decode ignores unknown
// keys, so without this check an operator's stale narrowing list would be dropped in
// silence. Any presence counts, including an empty list.
func TestLoadRejectsInteractiveAllowedAgents(t *testing.T) {
	for _, tt := range []struct {
		name     string
		listLine string
	}{
		{name: "non-empty list", listLine: "    interactive_allowed_agents: [term]\n"},
		{name: "empty list", listLine: "    interactive_allowed_agents: []\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := Load(writeProjectWithInteractiveKey(t, tt.listLine))
			if err == nil {
				t.Fatal("Load accepted the removed interactive_allowed_agents key")
			}
			if !strings.Contains(err.Error(), "unknown project field interactive_allowed_agents; use allow_interactive") {
				t.Fatalf("error = %v, want it to name the removed key and its replacement", err)
			}
			if !strings.Contains(err.Error(), "demo") {
				t.Fatalf("error must name the offending project; got %v", err)
			}
		})
	}
}

// TestLoadSwitchAloneWithoutLegacyKey loads the post-AGT-02 shape: allow_interactive
// on its own, no removed key anywhere in the document, so the load succeeds.
func TestLoadSwitchAloneWithoutLegacyKey(t *testing.T) {
	p := writeProjectWithInteractiveKey(t, "    allow_interactive: true\n")
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Projects["demo"].IsInteractiveAllowed() {
		t.Fatalf("demo = %+v, want an explicit allow_interactive:true", cfg.Projects["demo"])
	}
}

// TestProjectConfigHasNoInteractiveAllowedAgents is the anti-relapse guard (design 0.3):
// the narrowing list must not come back to the config type or to its yaml surface. A
// re-added field would silently start decoding operator yaml again and re-open a second
// interactive gate next to allow_interactive.
func TestProjectConfigHasNoInteractiveAllowedAgents(t *testing.T) {
	typ := reflect.TypeOf(ProjectConfig{})
	for i := range typ.NumField() {
		f := typ.Field(i)
		if f.Name == "InteractiveAllowedAgents" {
			t.Fatalf("ProjectConfig.%s is back; AGT-02 0.3 removed it in favour of allow_interactive", f.Name)
		}
		if strings.Contains(f.Tag.Get("yaml"), "interactive_allowed_agents") {
			t.Fatalf("ProjectConfig.%s still decodes the removed interactive_allowed_agents key (tag %q)", f.Name, f.Tag)
		}
	}
}
