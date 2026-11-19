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

// The removed supervisor / presence sections fail the load with a message naming the
// replacement (supervisor.leader moves to the top-level leader: key).
func TestRetiredSectionsFailLoad(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	write(t, p, retiredSectionsYAML)
	_, _, err := Load(p)
	if err == nil {
		t.Fatal("Load must reject the removed supervisor / presence sections")
	}
	for _, want := range []string{"`supervisor:` section has been removed", "top-level `leader:`", "`presence:` section has been removed"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error lacks %q: %v", want, err)
		}
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

// A supervisor section without a leader sub-key gets the plain message.
func TestRetiredSupervisorWithoutLeaderFailsPlainly(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	write(t, p, "projects:\n  demo:\n    host_path: /tmp/demo\nsupervisor:\n  enabled: true\n")
	_, _, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "`supervisor:` section has been removed") || strings.Contains(err.Error(), "leader") {
		t.Fatalf("err = %v", err)
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

// The rejection also covers retired sections that live in an include fragment, and
// names that file.
func TestRetiredSectionsFailInIncludeFragment(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"config.yaml":       "include:\n  - config.d/*.yaml\nprojects:\n  demo:\n    host_path: /tmp/demo\n",
		"config.d/sup.yaml": "supervisor:\n  leader:\n    enabled: true\n    agent: x\n",
	})
	_, _, err := Load(filepath.Join(dir, "config.yaml"))
	if err == nil || !strings.Contains(err.Error(), "sup.yaml") || !strings.Contains(err.Error(), "top-level `leader:`") {
		t.Fatalf("err = %v", err)
	}
}

// A webhook / desktop / hook events list that still names the retired session.waiting is read
// as session.awaiting_reply (deduplicated) and the load says to rename it.
func TestRetiredSessionWaitingEventIsRenamed(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	write(t, p, `projects:
  demo:
    host_path: /tmp/demo
server:
  notification:
    desktop:
      events: [session.waiting, job.terminal]
    webhooks:
      - url: https://example.com/hook
        events: [session.waiting, session.awaiting_reply]
`)
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	n := cfg.Server.Notification
	if got := n.Webhooks[0].Events; len(got) != 1 || got[0] != "session.awaiting_reply" {
		t.Fatalf("webhook events = %q", got)
	}
	if got := n.Desktop.Events; len(got) != 2 || got[0] != "session.awaiting_reply" || got[1] != "job.terminal" {
		t.Fatalf("desktop events = %q", got)
	}
	var n2 int
	for _, w := range cfg.LoadWarnings() {
		if strings.Contains(w, "session.waiting") && strings.Contains(w, "session.awaiting_reply") {
			n2++
		}
	}
	if n2 != 2 {
		t.Fatalf("retired event warnings = %d, want 2: %q", n2, cfg.LoadWarnings())
	}
}
