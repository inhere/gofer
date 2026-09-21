package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// TestRegistrySaveDoesNotTouchAgents is the end-to-end half of h-aii-kd57: the
// `project add` / web "project settings save" path goes through
// project.Registry.save → config.Save, and that write must leave the operator's
// agents block alone — comments, `interactive_args: []` and all — because nothing
// about the agents changed.
func TestRegistrySaveDoesNotTouchAgents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	orig := `agents:
  # the acp agent: interactive with a bare launch
  omp:
    type: acp-agent
    command: omp
    args: [acp]
    interactive_args: []

projects:
  a:
    host_path: /x
`
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, _, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	reg := NewRegistry(cfg, path)
	if err := reg.Add("b", config.ProjectConfig{HostPath: "/y"}, false); err != nil {
		t.Fatalf("Add: %v", err)
	}

	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if g, w := topBlock(t, string(out), "agents"), topBlock(t, orig, "agents"); g != w {
		t.Fatalf("agents block rewritten by a project write:\n--- got ---\n%s\n--- want ---\n%s", g, w)
	}
	if !strings.Contains(string(out), "interactive_args: []") {
		t.Fatalf("interactive_args: [] lost:\n%s", out)
	}

	reloaded, _, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := reloaded.Agents["omp"].InteractiveArgs; got == nil || len(got) != 0 {
		t.Fatalf("interactive_args = %#v after the write, want a non-nil empty list", got)
	}
	if _, ok := reloaded.Projects["b"]; !ok {
		t.Fatalf("added project not persisted:\n%s", out)
	}
}

// topBlock extracts one top-level block's raw text (its `key:` line plus the
// following indented or blank lines).
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
