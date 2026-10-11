package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func readText(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const splitMain = `# main file header
include:
  - config.d/*.yaml

server:
  token: tok
runners:
  local:
    type: local
`

const splitAgents = `# agent definitions
agents:
  myagent:
    type: cli-agent
    command: myagent   # inline comment
    args: [run, "{{prompt}}"]
`

const splitProjects = `projects:
  p:
    host_path: /x
    allowed_runners: [local]
    allowed_agents: [myagent]
`

func TestIncludeMergesFragmentsLikeOneFile(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"config.yaml":            splitMain,
		"config.d/agents.yaml":   splitAgents,
		"config.d/projects.yaml": splitProjects,
		"single/config.yaml":     strings.Replace(splitMain, "include:\n  - config.d/*.yaml\n", "", 1) + splitAgents + splitProjects,
	})
	split, path, err := Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	one, _, err := Load(filepath.Join(dir, "single/config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(split.Agents, one.Agents) || !reflect.DeepEqual(split.Projects, one.Projects) || split.Server.Token != one.Server.Token {
		t.Fatalf("split config differs from the one-file equivalent:\n%+v\n%+v", split, one)
	}
	files := split.SourceFiles()
	want := []string{path, filepath.Join(dir, "config.d/agents.yaml"), filepath.Join(dir, "config.d/projects.yaml")}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("SourceFiles = %v, want %v (main first, globs in name order)", files, want)
	}
	srcs := split.SectionSources()
	if srcs["agents"] != "config.d/agents.yaml" || srcs["projects"] != "config.d/projects.yaml" || srcs["server"] != "config.yaml" {
		t.Fatalf("SectionSources = %v", srcs)
	}
	if one.SectionSources() != nil {
		t.Fatalf("a one-file config reports no section sources, got %v", one.SectionSources())
	}
}

func TestIncludeErrors(t *testing.T) {
	cases := map[string]struct {
		files map[string]string
		want  string
	}{
		"section in two files": {map[string]string{
			"config.yaml": "include: [a.yaml]\nagents:\n  x: {type: exec}\n",
			"a.yaml":      "agents:\n  y: {type: exec}\n",
		}, `section "agents" is in both`},
		"include in a fragment": {map[string]string{
			"config.yaml": "include: [a.yaml]\n",
			"a.yaml":      "include: [b.yaml]\n",
		}, "only allowed in the main config file"},
		"named file missing": {map[string]string{
			"config.yaml": "include: [missing.yaml]\n",
		}, "missing.yaml"},
		"removed key in a fragment": {map[string]string{
			"config.yaml": "include: [a.yaml]\n",
			"a.yaml":      "projects:\n  p:\n    host_path: /x\n    interactive_allowed_agents: [claude]\n",
		}, "a.yaml"},
	}
	for name, c := range cases {
		dir := t.TempDir()
		writeFiles(t, dir, c.files)
		_, _, err := Load(filepath.Join(dir, "config.yaml"))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err=%v, want it to mention %q", name, err, c.want)
		}
	}
	// A glob that matches nothing is fine (a conventional config.d/*.yaml).
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"config.yaml": "include: [config.d/*.yaml]\nserver:\n  token: t\n"})
	if _, _, err := Load(filepath.Join(dir, "config.yaml")); err != nil {
		t.Fatalf("empty glob: %v", err)
	}
}

// An edit to a section rewrites only the file holding it; the other files keep their
// bytes (comments included), and a brand-new section lands in the main file.
func TestIncludeSaveWritesBackToOwningFile(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"config.yaml":            splitMain,
		"config.d/agents.yaml":   splitAgents,
		"config.d/projects.yaml": splitProjects,
	})
	main := filepath.Join(dir, "config.yaml")
	agentsFile := filepath.Join(dir, "config.d/agents.yaml")
	projectsFile := filepath.Join(dir, "config.d/projects.yaml")
	cfg, _, err := Load(main)
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Projects["p"]
	p.DefaultAgent = "myagent"
	cfg.Projects["p"] = p
	if err := Save(main, cfg); err != nil {
		t.Fatal(err)
	}
	if got := readText(t, main); got != splitMain {
		t.Fatalf("main file changed:\n%s", got)
	}
	if got := readText(t, agentsFile); got != splitAgents {
		t.Fatalf("agents file changed:\n%s", got)
	}
	if got := readText(t, projectsFile); !strings.Contains(got, "default_agent: myagent") || strings.Contains(got, "agents:\n  myagent") {
		t.Fatalf("projects file:\n%s", got)
	}

	// A new section (roles) goes to the main file; fragments stay untouched.
	cfg, _, err = Load(main)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Roles = map[string]RoleConfig{"reviewer": {Agent: "myagent"}}
	if err := Save(main, cfg); err != nil {
		t.Fatal(err)
	}
	if got := readText(t, main); !strings.HasPrefix(got, "# main file header\ninclude:") || !strings.Contains(got, "roles:") {
		t.Fatalf("main file:\n%s", got)
	}
	if got := readText(t, agentsFile); got != splitAgents {
		t.Fatalf("agents file changed on a roles edit:\n%s", got)
	}
	again, _, err := Load(main)
	if err != nil {
		t.Fatalf("reload after save: %v", err)
	}
	if again.Roles["reviewer"].Agent != "myagent" || again.Projects["p"].DefaultAgent != "myagent" {
		t.Fatalf("round trip lost an edit: %+v", again)
	}
}

func TestSectionFileOf(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"config.yaml":          splitMain,
		"config.d/agents.yaml": splitAgents,
	})
	main := filepath.Join(dir, "config.yaml")
	if got, err := SectionFileOf(main, "agents"); err != nil || got != filepath.Join(dir, "config.d/agents.yaml") {
		t.Fatalf("agents -> %q, %v", got, err)
	}
	// The main file holds its own sections and any section not written anywhere yet.
	for _, key := range []string{"server", "roles"} {
		if got, err := SectionFileOf(main, key); err != nil || got != main {
			t.Fatalf("%s -> %q, %v", key, got, err)
		}
	}
}
