package config

import (
	"path/filepath"
	"strings"
	"testing"
)

const retiredSectionsYAML = `projects:
  demo:
    host_path: /tmp/demo
agents:
  lead:
    type: cli-agent
    command: lead
    args: ["{{prompt}}"]
supervisor:
  enabled: true
  desired_supervisors: 3
  leader:
    enabled: true
    agent: lead
presence:
  ttl_sec: 30
`

// The retired supervisor / presence sections load without error and yield warnings;
// supervisor.leader is no longer read, so the leader stays off.
func TestRetiredSectionsWarnAndLeaderMoves(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	write(t, p, retiredSectionsYAML)
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LeaderEnabled() || cfg.LeaderConfig() != nil {
		t.Fatalf("supervisor.leader must not take effect: %+v", cfg.LeaderConfig())
	}
	ws := cfg.LoadWarnings()
	if len(ws) != 2 {
		t.Fatalf("warnings = %q, want one for supervisor and one for presence", ws)
	}
	joined := strings.Join(ws, "\n")
	if !strings.Contains(joined, "supervisor") || !strings.Contains(joined, "top-level `leader:`") || !strings.Contains(joined, "presence") {
		t.Fatalf("warning text: %s", joined)
	}
	if got := cfg.Clone().LoadWarnings(); len(got) != 2 {
		t.Fatalf("Clone dropped the warnings: %q", got)
	}
}

func TestNoRetiredSectionsNoWarnings(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	write(t, p, "projects:\n  demo:\n    host_path: /tmp/demo\n")
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if ws := cfg.LoadWarnings(); len(ws) != 0 {
		t.Fatalf("unexpected warnings: %q", ws)
	}
}

// A supervisor section without a leader sub-key gets the plain warning.
func TestRetiredSupervisorWithoutLeaderWarning(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	write(t, p, "projects:\n  demo:\n    host_path: /tmp/demo\nsupervisor:\n  enabled: true\n")
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	ws := cfg.LoadWarnings()
	if len(ws) != 1 || strings.Contains(ws[0], "leader") {
		t.Fatalf("warnings = %q", ws)
	}
}

// A top-level leader block takes effect and goes through the validation, with
// messages naming leader.* rather than supervisor.leader.*.
func TestTopLevelLeaderTakesEffectAndIsValidated(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg.yaml")
	write(t, p, `projects:
  demo:
    host_path: /tmp/demo
agents:
  lead:
    type: cli-agent
    command: lead
    args: ["{{prompt}}"]
leader:
  enabled: true
  agent: lead
  wake_delay_sec: 5
`)
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.LeaderEnabled() || cfg.LeaderConfig().Agent != "lead" || cfg.LeaderConfig().WakeDelaySec != 5 {
		t.Fatalf("leader not applied: %+v", cfg.LeaderConfig())
	}
	if len(cfg.LoadWarnings()) != 0 {
		t.Fatalf("unexpected warnings: %q", cfg.LoadWarnings())
	}

	for name, tc := range map[string]struct{ body, want string }{
		"negative delay":  {"leader:\n  wake_delay_sec: -1\n", "leader.wake_delay_sec"},
		"bad scope":       {"leader:\n  scopes: [job]\n", "leader.scopes"},
		"no agent":        {"leader:\n  enabled: true\n", "leader.enabled requires leader.agent"},
		"unknown agent":   {"leader:\n  enabled: true\n  agent: nope\n", "leader.agent \"nope\""},
		"negative rounds": {"leader:\n  max_rounds_per_scope: -2\n", "leader.max_rounds_per_scope"},
	} {
		bad := filepath.Join(t.TempDir(), "bad.yaml")
		write(t, bad, "projects:\n  demo:\n    host_path: /tmp/demo\n"+tc.body)
		_, _, err := Load(bad)
		if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "supervisor.leader") {
			t.Fatalf("%s: err = %v, want contains %q", name, err, tc.want)
		}
	}
}

// The warning also fires for retired sections that live in an include fragment, and
// names that file.
func TestRetiredSectionsWarnInIncludeFragment(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"config.yaml":       "include:\n  - config.d/*.yaml\nprojects:\n  demo:\n    host_path: /tmp/demo\n",
		"config.d/sup.yaml": "supervisor:\n  leader:\n    enabled: true\n    agent: x\n",
		"config.d/pre.yaml": "presence:\n  ttl_sec: 9\n",
	})
	cfg, _, err := Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LeaderEnabled() {
		t.Fatal("fragment supervisor.leader must not take effect")
	}
	joined := strings.Join(cfg.LoadWarnings(), "\n")
	if !strings.Contains(joined, "sup.yaml") || !strings.Contains(joined, "top-level `leader:`") || !strings.Contains(joined, "pre.yaml") {
		t.Fatalf("warnings = %s", joined)
	}
}

// Saving the config (as the web does) must not delete the retired sections, and must
// not serialize the load warnings.
func TestSaveKeepsRetiredSections(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	write(t, p, retiredSectionsYAML)
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Projects["b"] = ProjectConfig{HostPath: "/y"}
	if err := Save(p, cfg); err != nil {
		t.Fatal(err)
	}
	got := read(t, p)
	for _, want := range []string{"supervisor:\n  enabled: true\n  desired_supervisors: 3\n  leader:", "presence:\n  ttl_sec: 30"} {
		if !strings.Contains(got, want) {
			t.Fatalf("saved file lost %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "arning") {
		t.Fatalf("load warnings leaked into the file:\n%s", got)
	}
	if _, _, err := Load(p); err != nil {
		t.Fatalf("reload: %v", err)
	}
}
