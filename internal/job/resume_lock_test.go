package job

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// TestResumeJobLockOverride: `job resume --lock` (gofer-jkd2) replaces the directory
// lock the continuation inherits from its source, and asks for the lock while doing so
// — a resident ACP session otherwise holds the whole project root, so every job of one
// sub-module queues behind it. Without the flag the source's declaration is inherited.
func TestResumeJobLockOverride(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newClaudeInjectService(t, root)
	if err := os.MkdirAll(filepath.Join(root, "sub", "mod"), 0o755); err != nil {
		t.Fatal(err)
	}

	src := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "claude", Runner: "local", Prompt: "source",
		Cwd: ".", TimeoutSec: 30,
	})
	assert.True(t, src.DirExclusive, "a writable agent job locks its directory")
	assert.Eq(t, 0, len(src.LockPaths))

	inherit, err := s.ResumeJobWith(src.ID, "continue", "", "caller", ResumeOptions{})
	assert.NoErr(t, err)
	assert.Eq(t, 0, len(inherit.LockPaths), "no --lock keeps the source's declaration (none)")

	narrowed, err := s.ResumeJobWith(src.ID, "continue", "", "caller", ResumeOptions{LockPaths: []string{"sub/mod"}})
	assert.NoErr(t, err)
	assert.Eq(t, "sub/mod", strings.Join(narrowed.LockPaths, ","))
	assert.True(t, narrowed.DirExclusive, "declaring a lock asks for the lock")
	assert.Eq(t, src.SessionID, narrowed.SessionID, "the continuation still continues the same session")
}

// TestResumeJobLockOverrideReEnablesExclusivity: a source that ran --shared-dir makes
// its continuations shared too (ExclusiveDir is stamped onto the request and inherited),
// so a resume that DECLARES a lock must re-enable the exclusive decision — otherwise
// the declared lock would be silently ignored.
func TestResumeJobLockOverrideReEnablesExclusivity(t *testing.T) {
	t.Parallel()
	s := newClaudeInjectService(t, t.TempDir())
	shared := false

	src := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "claude", Runner: "local", Prompt: "source",
		Cwd: ".", TimeoutSec: 30, ExclusiveDir: &shared,
	})
	assert.False(t, src.DirExclusive)

	next, err := s.ResumeJobWith(src.ID, "narrow", "", "caller", ResumeOptions{LockPaths: []string{"sub"}})
	assert.NoErr(t, err)
	assert.True(t, next.DirExclusive, "--lock on a shared source must take the lock")
	assert.Eq(t, "sub", strings.Join(next.LockPaths, ","))
}
