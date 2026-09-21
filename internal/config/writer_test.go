package config

import (
	"path/filepath"
	"strings"
	"testing"

	yaml "github.com/goccy/go-yaml"
)

// acpAgentYAML is the agent block every test below edits: an acp-agent whose
// interactive_args is the EMPTY list — AGT-02's "interactive with a bare launch",
// a value a length-based omitempty silently drops.
const acpAgentYAML = `agents:
  # the acp agent
  omp:
    type: acp-agent
    command: omp
    args: [acp]      # batch argv
    interactive_args: []

projects:
  a:
    host_path: /x
`

// TestSaveKeepsEmptyInteractiveArgs is the h-aii-kd57 regression: `interactive_args: []`
// means "interactive mode, no extra argv" (AGT-02), so it must survive BOTH save paths —
// the surgical one (the agents block is untouched and keeps its original text) and the
// rewriting one (the block IS re-rendered because the agent itself changed), which is
// what the ArgList type exists for.
func TestSaveKeepsEmptyInteractiveArgs(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	write(t, p, acpAgentYAML)

	cfg, _, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Agents["omp"].InteractiveArgs; got == nil || len(got) != 0 {
		t.Fatalf("Load: interactive_args = %#v, want a non-nil empty list", got)
	}

	// (1) An unrelated change: the agents block is not rewritten at all.
	cfg.Projects["b"] = ProjectConfig{HostPath: "/y"}
	if err := Save(p, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out := read(t, p)
	if !strings.Contains(out, "interactive_args: []") {
		t.Fatalf("interactive_args: [] lost after a projects-only save:\n%s", out)
	}
	reloaded, _, err := Load(p)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := reloaded.Agents["omp"].InteractiveArgs; got == nil || len(got) != 0 {
		t.Fatalf("after save: interactive_args = %#v, want a non-nil empty list (the agent is no longer interactive)", got)
	}

	// (2) A real change to the agent: the block IS re-rendered, and the empty list
	// must still round-trip through the typed struct.
	reloaded.Agents["other"] = AgentConfig{Type: "cli-agent", Command: "other", Args: []string{"{{prompt}}"}}
	if err := Save(p, reloaded); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out = read(t, p)
	if !strings.Contains(out, "interactive_args: []") {
		t.Fatalf("interactive_args: [] lost after an agents-block rewrite:\n%s", out)
	}
	again, _, err := Load(p)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := again.Agents["omp"].InteractiveArgs; got == nil || len(got) != 0 {
		t.Fatalf("after an agents rewrite: interactive_args = %#v, want a non-nil empty list", got)
	}
}

// TestSaveOnlyRewritesChangedTopLevelKeys pins the surgical write (h-aii-kd57 修法①):
// a save replaces only the managed top-level keys whose canonical render changed. An
// untouched block keeps the operator's own text — comments, key order and all — so a
// web project edit can no longer rewrite (and comment-strip) a hand-annotated agents
// block.
func TestSaveOnlyRewritesChangedTopLevelKeys(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	write(t, p, "# hand-written header\n"+acpAgentYAML)

	cfg, _, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.Projects["b"] = ProjectConfig{HostPath: "/y"}
	if err := Save(p, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	afterProjects := read(t, p)

	if got, want := topBlock(t, afterProjects, "agents"), topBlock(t, acpAgentYAML, "agents"); got != want {
		t.Fatalf("agents block changed although only projects was edited:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	if !strings.Contains(afterProjects, "# hand-written header") {
		t.Fatalf("non-managed text lost:\n%s", afterProjects)
	}
	if got, want := topBlock(t, afterProjects, "projects"), topBlock(t, acpAgentYAML, "projects"); got == want {
		t.Fatalf("projects block unchanged although a project was added:\n%s", got)
	}
	reloaded, _, err := Load(p)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, ok := reloaded.Projects["b"]; !ok {
		t.Fatalf("added project b not persisted:\n%s", afterProjects)
	}

	// Now the mirrored case: editing the agents block must leave projects alone.
	reloaded.Agents["other"] = AgentConfig{Type: "cli-agent", Command: "other", Args: []string{"{{prompt}}"}}
	if err := Save(p, reloaded); err != nil {
		t.Fatalf("Save: %v", err)
	}
	afterAgents := read(t, p)
	if got, want := topBlock(t, afterAgents, "projects"), topBlock(t, afterProjects, "projects"); got != want {
		t.Fatalf("projects block changed although only agents was edited:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	if got, want := topBlock(t, afterAgents, "agents"), topBlock(t, acpAgentYAML, "agents"); got == want {
		t.Fatalf("agents block unchanged although the agent was edited:\n%s", got)
	}
	if !strings.Contains(afterAgents, "interactive_args: []") {
		t.Fatalf("the rewritten agents block dropped the empty interactive_args:\n%s", afterAgents)
	}
}

// TestSaveNewTopLevelKeyAppended covers the other half of the merge: a managed key the
// original file never had is appended (and the file stays loadable).
func TestSaveNewTopLevelKeyAppended(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	write(t, p, acpAgentYAML+"custom_top: 123\n")

	cfg, _, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.Roles["reviewer"] = RoleConfig{Agent: "omp", SystemPrompt: "review"}
	if err := Save(p, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out := read(t, p)
	if !strings.Contains(out, "roles:") {
		t.Fatalf("new managed key roles not written:\n%s", out)
	}
	if !strings.Contains(out, "custom_top: 123") {
		t.Fatalf("unknown top key lost:\n%s", out)
	}
	reloaded, _, err := Load(p)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Roles["reviewer"].SystemPrompt != "review" {
		t.Fatalf("role not persisted:\n%s", out)
	}
}

// TestInteractiveArgsEmptyRoundTrip is the unit-level statement of the type's job: the
// YAML pair must distinguish unset (nil → key omitted) from set-but-empty (`[]` → the
// key written back as `[]`).
func TestInteractiveArgsEmptyRoundTrip(t *testing.T) {
	out, err := yaml.Marshal(AgentConfig{Type: "acp-agent", Command: "omp", InteractiveArgs: ArgList{}})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(out), "interactive_args: []") {
		t.Fatalf("Marshal(empty) = %q, want an interactive_args: [] line", out)
	}

	var unset AgentConfig
	unsetOut, err := yaml.Marshal(unset)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(unsetOut), "interactive_args") {
		t.Fatalf("Marshal(unset) = %q, want no interactive_args key", unsetOut)
	}

	var back AgentConfig
	if err := yaml.Unmarshal([]byte("interactive_args: []\n"), &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if back.InteractiveArgs == nil || len(back.InteractiveArgs) != 0 {
		t.Fatalf("Unmarshal(`[]`) = %#v, want a non-nil empty list", back.InteractiveArgs)
	}
}

// topBlock extracts one top-level block's raw text: its `key:` line plus every
// following indented or blank line, up to the next top-level key.
func topBlock(t *testing.T, text, key string) string {
	t.Helper()
	lines := strings.Split(text, "\n")
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(l, key+":") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("no top-level %q block in:\n%s", key, text)
	}
	end := start + 1
	for end < len(lines) {
		l := lines[end]
		if strings.TrimSpace(l) != "" && !strings.HasPrefix(l, " ") && !strings.HasPrefix(l, "\t") {
			break
		}
		end++
	}
	for end > start+1 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	return strings.Join(lines[start:end], "\n")
}

// TestSaveHealsDuplicateTopLevelBlocks: the pre-F1 writer could leave a config with
// the same managed key twice (the user saw duplicated `log:` / `session:` blocks after
// a web save, after which every gofer command failed to load the file). Loading the
// damaged text is impossible, but a save from an in-memory config must not carry the
// duplicate forward: the first block wins, the copy is dropped, and the result loads.
func TestSaveHealsDuplicateTopLevelBlocks(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	damaged := acpAgentYAML + `
log: # file logging
  dir: logs

session:
  auto_relay_idle_sec: 300

log:
  dir: logs
session: # duplicated by an old save
  auto_relay_idle_sec: 300
`
	write(t, p, damaged)
	cfg, _, err := Load(acpFixturePath(t, dir))
	if err != nil {
		t.Fatalf("Load fixture: %v", err)
	}
	cfg.Log.Dir = "logs"
	idle := 300
	cfg.Session.AutoRelayIdleSec = &idle
	if err := Save(p, cfg); err != nil {
		t.Fatalf("Save over damaged file: %v", err)
	}
	out := read(t, p)
	for _, key := range []string{"log:", "session:"} {
		if n := strings.Count(out, "\n"+key); n != 1 {
			t.Fatalf("top-level %q appears %d times after save, want 1:\n%s", key, n, out)
		}
	}
	if _, _, err := Load(p); err != nil {
		t.Fatalf("healed file must load: %v\n%s", err, out)
	}
}

// acpFixturePath writes the clean fixture next to the damaged file so the in-memory
// config comes from a loadable document (the damaged one cannot be loaded).
func acpFixturePath(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "clean.yaml")
	write(t, p, acpAgentYAML)
	return p
}
