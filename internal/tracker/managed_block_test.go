package tracker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestManagedBlockSkipsClaudeImportingAgents: a CLAUDE.md whose content is
// `@AGENTS.md` already loads AGENTS.md, so the gofer block goes into AGENTS.md
// only (Claude would otherwise read it twice). Migration still strips a bd
// block from such a CLAUDE.md, without putting a gofer block in its place.
func TestManagedBlockSkipsClaudeImportingAgents(t *testing.T) {
	write := func(root, name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	read := func(root, name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	root := t.TempDir()
	write(root, "AGENTS.md", "# agents\n")
	write(root, "CLAUDE.md", "@AGENTS.md\n\nlocal notes\n")
	if _, _, err := Init(root, "demo", false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(root, "AGENTS.md"), beginBlock) {
		t.Fatal("init: AGENTS.md should carry the gofer block")
	}
	if strings.Contains(read(root, "CLAUDE.md"), beginBlock) {
		t.Fatal("init: CLAUDE.md imports AGENTS.md and must not repeat the block")
	}

}
