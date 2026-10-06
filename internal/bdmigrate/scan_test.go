package bdmigrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanManualListsLeftoverBdMentionsWithoutEditing(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "CLAUDE.md"), "@workspace.md\nplain line\nUse `bd ready` here\n")
	writeFile(t, filepath.Join(root, "workspace.md"), "# ws\n## Beads notes\nnothing about the board\n")
	writeFile(t, filepath.Join(root, ".claude", "settings.local.json"), "{\n  \"permissions\": {\"allow\": [\n    \"Bash(bd list *)\",\n    \"Bash(ls)\"\n  ]}\n}\n")
	if err := os.MkdirAll(filepath.Join(root, ".agents", "skills", "beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".agents", "skills", "other"), 0o755); err != nil {
		t.Fatal(err)
	}
	before := readFileUnix(t, filepath.Join(root, "CLAUDE.md"))
	got := strings.Join(scanManual(root, nil), "\n")
	for _, want := range []string{"CLAUDE.md:3:", "workspace.md:2: ## Beads notes", ".claude/settings.local.json: 1 line(s) mention bd", ".agents/skills/beads"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "plain line") || strings.Contains(got, "skills/other") || strings.Contains(got, "nothing about the board") {
		t.Fatalf("false positives:\n%s", got)
	}
	// Lines inside a planned-away block are not reported: the scan sees the edited text.
	after := map[string]string{"CLAUDE.md": "@workspace.md\nplain line\n"}
	if got := strings.Join(scanManual(root, after), "\n"); strings.Contains(got, "CLAUDE.md:3") {
		t.Fatalf("scan must use the planned content:\n%s", got)
	}
	if readFileUnix(t, filepath.Join(root, "CLAUDE.md")) != before {
		t.Fatal("scan must never edit files")
	}
}

func readFileUnix(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
