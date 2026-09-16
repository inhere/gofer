package config

import (
	"bytes"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// legacyInteractiveProjectYAML is a pre-AGT-02 project block: it still carries the
// REMOVED interactive_allowed_agents key. listLine and switchLine are spliced in
// verbatim (already indented) or left empty, so every combination of "old key present
// / absent" × "switch written / unwritten" loads from the same shape an operator's file
// has on disk.
func writeLegacyInteractiveProject(t *testing.T, listLine, switchLine string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	write(t, p, `
projects:
  demo:
    host_path: /tmp/demo
    allowed_agents: [term]
`+listLine+switchLine+`
agents:
  term:
    type: cli
    command: bash
    interactive: true
    no_raw_cmd: true
`)
	return p
}

// loadCapturingLog loads the yaml with the default slog redirected into a buffer,
// returning the project and the captured log text (the compat read warns once per
// project at load).
func loadCapturingLog(t *testing.T, path string) (ProjectConfig, string) {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg.Projects["demo"], buf.String()
}

// TestLoadCompatLegacyInteractiveListImpliesAllow pins the one-shot compat read of the
// removed key: an existing yaml with a non-empty interactive_allowed_agents and no
// allow_interactive keeps working — the list is carried over as an explicit true, so
// nothing downstream (admission, policy push, worker) ever has to know the old rule —
// and the operator is told to rewrite it.
func TestLoadCompatLegacyInteractiveListImpliesAllow(t *testing.T) {
	proj, log := loadCapturingLog(t, writeLegacyInteractiveProject(t, "    interactive_allowed_agents: [term]\n", ""))

	if !proj.IsInteractiveAllowed() {
		t.Fatalf("IsInteractiveAllowed() = false, want true for a legacy non-empty interactive_allowed_agents (%+v)", proj)
	}
	if proj.AllowInteractive == nil || !*proj.AllowInteractive {
		t.Fatalf("AllowInteractive = %v, want an explicit true (the compat read must store the switch, not re-derive it)", proj.AllowInteractive)
	}
	if !strings.Contains(log, "interactive_allowed_agents has been removed; treating it as allow_interactive: true") {
		t.Fatalf("missing rewrite warning; log: %s", log)
	}
	if !strings.Contains(log, "project=demo") {
		t.Fatalf("warning must name the project; log: %s", log)
	}
}

// TestLoadCompatLegacyListIgnoredWhenSwitchWritten: once allow_interactive is written —
// either value — the removed key is inert and the written value wins, so a switch
// deliberately flipped off is not resurrected by a leftover list. The load still warns,
// because the dead key should be deleted.
func TestLoadCompatLegacyListIgnoredWhenSwitchWritten(t *testing.T) {
	tests := []struct {
		name       string
		switchLine string
		want       bool
	}{
		{name: "explicit true", switchLine: "    allow_interactive: true\n", want: true},
		{name: "explicit false", switchLine: "    allow_interactive: false\n", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proj, log := loadCapturingLog(t, writeLegacyInteractiveProject(t, "    interactive_allowed_agents: [term]\n", tt.switchLine))

			if proj.AllowInteractive == nil || *proj.AllowInteractive != tt.want {
				t.Fatalf("AllowInteractive = %v, want an explicit %v (the written switch wins)", proj.AllowInteractive, tt.want)
			}
			if proj.IsInteractiveAllowed() != tt.want {
				t.Fatalf("IsInteractiveAllowed() = %v, want %v", proj.IsInteractiveAllowed(), tt.want)
			}
			if !strings.Contains(log, "interactive_allowed_agents has been removed and is ignored") {
				t.Fatalf("missing ignore warning; log: %s", log)
			}
			if strings.Contains(log, "treating it as allow_interactive: true") {
				t.Fatalf("a written switch must not be overridden by the legacy list; log: %s", log)
			}
		})
	}
}

// TestLoadCompatEmptyLegacyListStaysClosed: only a NON-EMPTY legacy list implies the
// switch. An empty list never meant "interactive allowed" (it meant the opposite), so
// it stays closed and there is nothing to warn about.
func TestLoadCompatEmptyLegacyListStaysClosed(t *testing.T) {
	proj, log := loadCapturingLog(t, writeLegacyInteractiveProject(t, "    interactive_allowed_agents: []\n", ""))

	if proj.IsInteractiveAllowed() {
		t.Fatalf("IsInteractiveAllowed() = true, want false for an empty legacy list (%+v)", proj)
	}
	if proj.AllowInteractive != nil {
		t.Fatalf("AllowInteractive = %v, want nil (nothing to carry over)", *proj.AllowInteractive)
	}
	if strings.Contains(log, "interactive_allowed_agents") {
		t.Fatalf("an empty legacy list must not warn; log: %s", log)
	}
}

// TestProjectInteractiveSwitchAloneAllows loads the post-AGT-02 shape: allow_interactive
// on its own, no legacy key anywhere in the document (hence no warning).
func TestProjectInteractiveSwitchAloneAllows(t *testing.T) {
	proj, log := loadCapturingLog(t, writeLegacyInteractiveProject(t, "", "    allow_interactive: true\n"))

	if !proj.IsInteractiveAllowed() {
		t.Fatalf("IsInteractiveAllowed() = false, want true for an explicit allow_interactive:true (%+v)", proj)
	}
	if strings.Contains(log, "interactive_allowed_agents") {
		t.Fatalf("no legacy key in the document → no compat warning; log: %s", log)
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
