package commands

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"
	"github.com/inhere/gofer/internal/tracker"
)

// isolateGitConfig keeps the developer's global / system git config out of git
// children of this test.
func isolateGitConfig(t *testing.T) {
	t.Helper()
	global := filepath.Join(t.TempDir(), "gitconfig")
	assert.Require(t, assert.NoErr(t, os.WriteFile(global, nil, 0o644)))
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	assert.Require(t, assert.NoErr(t, err, "git %s: %s", strings.Join(args, " "), out))
	return strings.TrimSpace(string(out))
}

func TestRepoMergeDriverCommand(t *testing.T) {
	isolateGitConfig(t)
	root := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(root, name)
		assert.Require(t, assert.NoErr(t, os.WriteFile(p, []byte(body), 0o644)))
		return p
	}
	issue := `{"id":"%s","title":"t","type":"task","status":"open","priority":2,"created_at":"2026-10-10T01:00:00Z"}` + "\n"
	line := func(id string) string { return strings.Replace(issue, "%s", id, 1) }
	base := write("base", line("a"))
	ours := write("ours", line("a")+line("b"))
	theirs := write("theirs", line("a")+line("c"))
	trackerRunOK(t, root, "repo", "merge-driver", base, ours, theirs, ".gofer/tracker/issues.jsonl")
	got, err := os.ReadFile(ours)
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, line("a")+line("b")+line("c"), string(got))

	// An unknown file collides in the text merge: non-zero exit, markers left.
	base, ours, theirs = write("base", "1\n"), write("ours", "o\n"), write("theirs", "t\n")
	_, code := trackerCLI(t, root, "repo", "merge-driver", base, ours, theirs, "x.jsonl")
	assert.Neq(t, 0, code)
	got, _ = os.ReadFile(ours)
	assert.StrContains(t, string(got), "<<<<<<< ours")

	_, code = trackerCLI(t, root, "repo", "merge-driver", base)
	assert.Neq(t, 0, code)
}

func TestRepoInitInstallsMergeDriver(t *testing.T) {
	isolateGitConfig(t)
	root := t.TempDir()
	gitOut(t, root, "init", "-q")
	out := trackerRunOK(t, root, "repo", "init", "--no-hooks")
	assert.StrContains(t, out, "merge driver: added `"+tracker.MergeAttributesLine+"`")
	attrs, err := os.ReadFile(filepath.Join(root, ".gitattributes"))
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, tracker.MergeAttributesLine+"\n", string(attrs))
	assert.Eq(t, tracker.MergeDriverCommand, gitOut(t, root, "config", "--local", "--get", "merge.gofer-tracker.driver"))
	assert.Eq(t, ".gofer/tracker/issues.jsonl: merge: gofer-tracker", gitOut(t, root, "check-attr", "merge", "--", ".gofer/tracker/issues.jsonl"))

	out = trackerRunOK(t, root, "repo", "merge-driver", "--install")
	assert.StrContains(t, out, "merge driver: already installed")

	// Outside git the tracker still initializes; the driver is skipped.
	plain := t.TempDir()
	out = trackerRunOK(t, plain, "repo", "init", "--no-hooks")
	assert.StrContains(t, out, "merge driver: skipped (not inside a git work tree)")
}
