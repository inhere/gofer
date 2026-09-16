package config

import (
	"bytes"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

// interactiveProjectYAML is a pre-AGT-02 project: the interactive switch does not
// exist yet and interactive_allowed_agents is the only interactive field. switchLine
// is spliced in verbatim (already indented) so both the "unset" and the "explicit
// false" reading of the same yaml can be loaded.
func loadInteractiveProject(t *testing.T, switchLine string) ProjectConfig {
	t.Helper()
	p := writeInteractiveProject(t, switchLine)
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg.Projects["demo"]
}

func writeInteractiveProject(t *testing.T, switchLine string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	write(t, p, `
projects:
  demo:
    host_path: /tmp/demo
    allowed_agents: [term]
    interactive_allowed_agents: [term]
`+switchLine+`
agents:
  term:
    type: cli
    command: bash
    interactive: true
    no_raw_cmd: true
`)
	return p
}

// TestProjectInteractiveLegacyListImpliesAllow pins the AGT-02 compatibility rule:
// an existing yaml that only lists interactive_allowed_agents (non-empty) with no
// allow_interactive keeps working — the list is read as "interactive jobs allowed".
func TestProjectInteractiveLegacyListImpliesAllow(t *testing.T) {
	proj := loadInteractiveProject(t, "")

	if !proj.IsInteractiveAllowed() {
		t.Fatalf("IsInteractiveAllowed() = false, want true for a legacy non-empty interactive_allowed_agents (%+v)", proj)
	}
	if len(proj.InteractiveAllowedAgents) != 1 || proj.InteractiveAllowedAgents[0] != "term" {
		t.Fatalf("interactive_allowed_agents = %v, want [term]", proj.InteractiveAllowedAgents)
	}
	if proj.AllowInteractive != nil {
		t.Fatalf("AllowInteractive = %v, want nil (unset) for a legacy yaml", *proj.AllowInteractive)
	}
}

// TestProjectInteractiveExplicitFalseWinsOverList is the other half of the
// compatibility rule: a bool could not express "unset", so the switch is a *bool. An
// explicit allow_interactive:false must deny interactive jobs even with a leftover
// narrowing list in the yaml — flipping the switch off must not leave the project
// half-open.
func TestProjectInteractiveExplicitFalseWinsOverList(t *testing.T) {
	proj := loadInteractiveProject(t, "    allow_interactive: false\n")

	if proj.IsInteractiveAllowed() {
		t.Fatalf("IsInteractiveAllowed() = true, want false: an explicit allow_interactive:false beats the list (%+v)", proj)
	}
	if len(proj.InteractiveAllowedAgents) != 1 || proj.InteractiveAllowedAgents[0] != "term" {
		t.Fatalf("interactive_allowed_agents = %v, want the list preserved as-is", proj.InteractiveAllowedAgents)
	}
}

// TestProjectInteractiveSwitchAloneAllows loads the switch WITHOUT any narrowing list
// — the post-AGT-02 shape: allow_interactive:true on its own means "every
// interactive-capable agent, subject to allowed_agents".
func TestProjectInteractiveSwitchAloneAllows(t *testing.T) {
	proj := loadInteractiveProject(t, "    allow_interactive: true\n")

	if !proj.IsInteractiveAllowed() {
		t.Fatalf("IsInteractiveAllowed() = false, want true for an explicit allow_interactive:true (%+v)", proj)
	}
}

// TestLoadWarnsOnLegacyInteractiveList: the legacy combination still works, but the
// operator should be told to write the switch explicitly (once per project, at load).
// An explicit switch — either value — must not warn.
func TestLoadWarnsOnLegacyInteractiveList(t *testing.T) {
	tests := []struct {
		name       string
		switchLine string
		wantWarn   bool
	}{
		{name: "legacy list without switch", switchLine: "", wantWarn: true},
		{name: "explicit true", switchLine: "    allow_interactive: true\n"},
		{name: "explicit false", switchLine: "    allow_interactive: false\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := writeInteractiveProject(t, tt.switchLine)

			var buf bytes.Buffer
			prev := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
			t.Cleanup(func() { slog.SetDefault(prev) })

			if _, _, err := Load(p); err != nil {
				t.Fatalf("Load: %v", err)
			}
			got := buf.String()
			if warned := strings.Contains(got, "legacy interactive_allowed_agents"); warned != tt.wantWarn {
				t.Fatalf("warn = %v, want %v; log: %s", warned, tt.wantWarn, got)
			}
			if tt.wantWarn && !strings.Contains(got, `project=demo`) {
				t.Fatalf("warning must name the project: %s", got)
			}
		})
	}
}
