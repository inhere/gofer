package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const splitSource = `# gofer server config
# (header comment)

server:
  token: tok

# agents defined on this host
agents:
  myagent:
    type: cli-agent
    command: myagent   # keep me
    args: [run, "{{prompt}}"]

projects:
  p:
    host_path: /x
    allowed_runners: [local]
    allowed_agents: [myagent]
runners:
  local:
    type: local
`

func TestSplitConfigMovesSectionsVerbatim(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "config.yaml")
	writeFiles(t, dir, map[string]string{"config.yaml": splitSource})
	res, err := SplitConfig(main, nil, "", false)
	if err != nil {
		t.Fatal(err)
	}
	agents := readText(t, filepath.Join(dir, "config.d/agents.yaml"))
	if !strings.HasPrefix(agents, "# agents defined on this host\nagents:\n") || !strings.Contains(agents, "command: myagent   # keep me") {
		t.Fatalf("agents fragment:\n%s", agents)
	}
	projects := readText(t, filepath.Join(dir, "config.d/projects.yaml"))
	if !strings.HasPrefix(projects, "projects:\n  p:\n") {
		t.Fatalf("projects fragment:\n%s", projects)
	}
	mainText := readText(t, main)
	if !strings.HasPrefix(mainText, "# gofer server config\n# (header comment)\n\ninclude:\n  - config.d/*.yaml\n\nserver:") {
		t.Fatalf("main file:\n%s", mainText)
	}
	if strings.Contains(mainText, "agents:") || strings.Contains(mainText, "projects:") || !strings.Contains(mainText, "runners:\n  local:") {
		t.Fatalf("main file still holds moved sections or lost runners:\n%s", mainText)
	}
	if res.Backup == "" || readText(t, res.Backup) != splitSource {
		t.Fatalf("backup %q missing or not the original", res.Backup)
	}
	cfg, _, err := Load(main)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agents["myagent"].Command != "myagent" || cfg.Projects["p"].HostPath != "/x" {
		t.Fatalf("split config lost content: %+v", cfg)
	}
}

func TestSplitConfigDryRunAndGuards(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "config.yaml")
	writeFiles(t, dir, map[string]string{"config.yaml": splitSource})
	res, err := SplitConfig(main, []string{"agents"}, "parts", true)
	if err != nil {
		t.Fatal(err)
	}
	if readText(t, main) != splitSource {
		t.Fatal("dry run wrote the main file")
	}
	if _, err := os.Stat(filepath.Join(dir, "parts")); !os.IsNotExist(err) {
		t.Fatal("dry run created the fragment dir")
	}
	if !strings.Contains(res.NewMain, "  - parts/*.yaml") || res.Files["agents"] != filepath.Join(dir, "parts/agents.yaml") {
		t.Fatalf("dry-run plan: %+v", res)
	}

	// An existing target is never overwritten.
	writeFiles(t, dir, map[string]string{"parts/agents.yaml": "agents: {}\n"})
	if _, err := SplitConfig(main, []string{"agents"}, "parts", false); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err=%v, want already exists", err)
	}
	if readText(t, main) != splitSource {
		t.Fatal("refused split touched the main file")
	}
	// A section already split out (or absent) is skipped; nothing left -> error.
	if _, err := SplitConfig(main, []string{"roles"}, "other", false); err == nil || !strings.Contains(err.Error(), "nothing to split") {
		t.Fatalf("err=%v, want nothing to split", err)
	}
}

// A second split appends to an existing include list instead of adding another one.
func TestSplitConfigExtendsExistingInclude(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "config.yaml")
	writeFiles(t, dir, map[string]string{"config.yaml": splitSource})
	if _, err := SplitConfig(main, []string{"agents"}, "config.d", false); err != nil {
		t.Fatal(err)
	}
	if _, err := SplitConfig(main, []string{"projects", "agents"}, "more", false); err != nil {
		t.Fatal(err)
	}
	mainText := readText(t, main)
	if strings.Count(mainText, "include:") != 1 || !strings.Contains(mainText, "  - config.d/*.yaml\n  - more/*.yaml\n") {
		t.Fatalf("main file:\n%s", mainText)
	}
	if _, err := os.Stat(filepath.Join(dir, "more/projects.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "more/agents.yaml")); !os.IsNotExist(err) {
		t.Fatal("agents was already split out and must be skipped")
	}
}
