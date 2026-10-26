package tracker

import (
	"os"
	"path/filepath"
	"testing"
)

// TestZhManagedBlockIsUnchanged is the pre-migration characterization of the
// instruction block `gofer repo init` writes into AGENTS.md / CLAUDE.md: these
// expected bytes were captured from the renderer that hard-coded its text, not
// from the catalog, so a migration that changes one character in Chinese fails.
func TestZhManagedBlockIsUnchanged(t *testing.T) {
	for _, policy := range []string{"local-commit", "ask", "none"} {
		got, err := ManagedBlockFor(policy)
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile(filepath.Join("testdata", "managed-block-zh-"+policy+".txt"))
		if err != nil {
			t.Fatal(err)
		}
		if got != string(want) {
			t.Errorf("zh-CN managed block (%s) changed\n--- got ---\n%s\n--- want ---\n%s", policy, got, want)
		}
	}
}
