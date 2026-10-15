package tracker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	assert.NoErr(t, os.MkdirAll(dir, 0o755))
}

// TestSubrepoScope: a memory written from a nested checkout names that checkout as its
// scope; the tracker's own repository, a plain subdirectory (no `.git` of its own) and a
// cwd outside the repository are all workspace-wide (gofer-opv5).
func TestSubrepoScope(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, ".git"))
	mustMkdir(t, filepath.Join(root, ".gofer", "tracker"))
	mustMkdir(t, filepath.Join(root, "deploy-hub", ".git"))
	mustMkdir(t, filepath.Join(root, "deploy-hub", "src"))
	mustMkdir(t, filepath.Join(root, "plain", "sub"))
	s := NewStore(filepath.Join(root, ".gofer", "tracker"))

	scope, ok := s.SubrepoScope(filepath.Join(root, "deploy-hub", "src"))
	assert.True(t, ok)
	assert.Eq(t, "deploy-hub/**", scope)

	for name, cwd := range map[string]string{
		"tracker's own repository": root,
		"plain subdirectory":       filepath.Join(root, "plain", "sub"),
		"empty cwd":                "",
		"outside the repository":   filepath.Dir(root),
	} {
		_, ok := s.SubrepoScope(cwd)
		assert.False(t, ok, name)
	}

	// A tracker directory that is not a <root>/.gofer/tracker layout has no repository
	// root to relate a cwd to.
	elsewhere := NewStore(t.TempDir())
	_, ok = elsewhere.SubrepoScope(filepath.Join(root, "deploy-hub", "src"))
	assert.False(t, ok)
}
