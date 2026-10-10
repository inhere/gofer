package tracker

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// hermeticGit runs git without the developer's global / system config (merge
// drivers, autocrlf, signing) leaking into the test.
type hermeticGit struct {
	t   *testing.T
	env []string
}

func newHermeticGit(t *testing.T) hermeticGit {
	t.Helper()
	global := filepath.Join(t.TempDir(), "gitconfig")
	assert.Require(t, assert.NoErr(t, os.WriteFile(global, nil, 0o644)))
	return hermeticGit{t: t, env: append(os.Environ(), "GIT_CONFIG_GLOBAL="+global, "GIT_CONFIG_NOSYSTEM=1")}
}

func (g hermeticGit) cmd(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "core.autocrlf=false"}, args...)...)
	cmd.Env = g.env
	return cmd
}

// run runs git and fails the test on error; it returns stdout + stderr.
func (g hermeticGit) run(dir string, args ...string) string {
	g.t.Helper()
	out, err := g.cmd(dir, args...).CombinedOutput()
	assert.Require(g.t, assert.NoErr(g.t, err, "git %s: %s", strings.Join(args, " "), out))
	return string(out)
}

// runner is the GitRunner InstallMergeDriver takes.
func (g hermeticGit) runner() GitRunner {
	return func(_ context.Context, dir string, args ...string) (string, error) {
		out, err := g.cmd(dir, args...).Output()
		return string(out), err
	}
}

func writeDriverFiles(t *testing.T, base, ours, theirs []byte) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "base"), filepath.Join(dir, "ours"), filepath.Join(dir, "theirs")}
	for i, b := range [][]byte{base, ours, theirs} {
		assert.Require(t, assert.NoErr(t, os.WriteFile(paths[i], b, 0o644)))
	}
	return paths[0], paths[1], paths[2]
}

func TestRunMergeDriverTrackerFile(t *testing.T) {
	base := jsonl(t, mIssue("a"))
	ours := jsonl(t, mIssue("a", func(i *Issue) { i.Title = "o"; i.UpdatedAt = tOlder }))
	theirs := jsonl(t, mIssue("a", func(i *Issue) { i.Title = "t"; i.UpdatedAt = tNewer }), mIssue("b"))
	b, o, th := writeDriverFiles(t, base, ours, theirs)
	var stderr bytes.Buffer
	code := RunMergeDriver(b, o, th, ".gofer/tracker/issues.jsonl", &stderr)
	assert.Eq(t, 0, code)
	got, err := os.ReadFile(o)
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, string(jsonl(t, mIssue("a", func(i *Issue) { i.Title = "t"; i.UpdatedAt = tNewer }), mIssue("b"))), string(got))
	assert.StrContains(t, stderr.String(), "issue a: title changed on both sides, took theirs")

	// Without %P the kind is sniffed from the content.
	b, o, th = writeDriverFiles(t, base, ours, theirs)
	assert.Eq(t, 0, RunMergeDriver(b, o, th, "", &stderr))
	got, _ = os.ReadFile(o)
	assert.StrContains(t, string(got), `"id":"b"`)
}

func TestRunMergeDriverFallsBackToTextMerge(t *testing.T) {
	t.Run("unknown file, clean text merge", func(t *testing.T) {
		b, o, th := writeDriverFiles(t, []byte("1\n2\n3\n"), []byte("1o\n2\n3\n"), []byte("1\n2\n3t\n"))
		var stderr bytes.Buffer
		assert.Eq(t, 0, RunMergeDriver(b, o, th, ".gofer/tracker/other.jsonl", &stderr))
		got, _ := os.ReadFile(o)
		assert.Eq(t, "1o\n2\n3t\n", string(got))
		assert.StrContains(t, stderr.String(), "not a tracker file")
	})
	t.Run("unknown file, colliding text merge", func(t *testing.T) {
		b, o, th := writeDriverFiles(t, []byte("1\n"), []byte("o\n"), []byte("t\n"))
		var stderr bytes.Buffer
		assert.Eq(t, 1, RunMergeDriver(b, o, th, "other.jsonl", &stderr))
		got, _ := os.ReadFile(o)
		assert.StrContains(t, string(got), "<<<<<<< ours")
	})
	t.Run("tracker file with a field this version does not know", func(t *testing.T) {
		line := `{"id":"a","title":"t","type":"task","status":"open","priority":2,"created_at":"x","future":1}` + "\n"
		changed := strings.Replace(line, `"title":"t"`, `"title":"o"`, 1)
		b, o, th := writeDriverFiles(t, []byte(line), []byte(changed), jsonl(t, mIssue("a")))
		var stderr bytes.Buffer
		assert.Eq(t, 1, RunMergeDriver(b, o, th, "issues.jsonl", &stderr))
		got, _ := os.ReadFile(o)
		assert.StrContains(t, string(got), "<<<<<<< ours")
		assert.StrContains(t, stderr.String(), "unknown field")
	})
}

func TestInstallMergeDriver(t *testing.T) {
	ctx := context.Background()
	git := newHermeticGit(t)
	run := git.runner()

	plain := t.TempDir()
	res, err := InstallMergeDriver(ctx, run, plain)
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, "not inside a git work tree", res.Skipped)
	_, statErr := os.Stat(filepath.Join(plain, ".gitattributes"))
	assert.True(t, os.IsNotExist(statErr))

	root := t.TempDir()
	git.run(root, "init", "-q")
	attrs := filepath.Join(root, ".gitattributes")
	assert.Require(t, assert.NoErr(t, os.WriteFile(attrs, []byte("* text=auto eol=lf"), 0o644)))
	res, err = InstallMergeDriver(ctx, run, root)
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, MergeDriverSetup{Attributes: attrs, AttributesAdded: true, ConfigChanged: true}, res)
	b, _ := os.ReadFile(attrs)
	assert.Eq(t, "* text=auto eol=lf\n"+MergeAttributesLine+"\n", string(b))
	assert.Eq(t, MergeDriverCommand+"\n", git.run(root, "config", "--get", "merge.gofer-tracker.driver"))
	assert.Eq(t, MergeDriverDesc+"\n", git.run(root, "config", "--get", "merge.gofer-tracker.name"))

	res, err = InstallMergeDriver(ctx, run, root)
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, MergeDriverSetup{Attributes: attrs}, res)
	b2, _ := os.ReadFile(attrs)
	assert.Eq(t, string(b), string(b2))
}

// TestMergeDriverGitEndToEnd: two branches each edit tracker records — different
// issues, the same issue with different comments and a conflicting title, new
// memories — and `git merge` succeeds through the installed driver.
func TestMergeDriverGitEndToEnd(t *testing.T) {
	driver := filepath.ToSlash(testcmd.Path(t))
	git := newHermeticGit(t)
	root := t.TempDir()
	git.run(root, "init", "-q", "-b", "main")
	git.run(root, "commit", "-q", "--allow-empty", "-m", "root")
	_, err := InstallMergeDriver(context.Background(), git.runner(), root)
	assert.Require(t, assert.NoErr(t, err))
	// The real driver is `gofer …` from PATH; the test runs the same function
	// through the testcmd helper instead (G053).
	git.run(root, "config", "merge.gofer-tracker.driver", `"`+driver+`" merge-driver %O %A %B %P`)

	s := NewStore(filepath.Join(root, ".gofer", "tracker"))
	shared := mIssue("p-2", func(i *Issue) { i.Comments = []Comment{comment(tBase, "base")} })
	assert.Require(t, assert.NoErr(t, s.WriteIssues([]Issue{mIssue("p-1"), shared, mIssue("p-3")})))
	assert.Require(t, assert.NoErr(t, writeLines(filepath.Join(s.Dir, "memories.jsonl"), []Memory{mMemory("m", "c", tBase)})))
	git.run(root, "add", "-A")
	git.run(root, "commit", "-q", "-m", "base")

	// branch b: edits p-3, comments on p-2 and renames it later (newer), adds memory b.
	git.run(root, "checkout", "-q", "-b", "b")
	assert.Require(t, assert.NoErr(t, s.WriteIssues([]Issue{
		mIssue("p-1"),
		mIssue("p-2", func(i *Issue) {
			i.Title = "theirs title"
			i.Comments = []Comment{comment(tBase, "base"), comment(tNewer, "from b")}
			i.UpdatedAt = tNewer
		}),
		mIssue("p-3", func(i *Issue) { i.Status = "closed"; i.ClosedAt = tOlder; i.UpdatedAt = tOlder }),
	})))
	assert.Require(t, assert.NoErr(t, writeLines(filepath.Join(s.Dir, "memories.jsonl"), []Memory{mMemory("b", "b", tOlder), mMemory("m", "c", tBase)})))
	git.run(root, "commit", "-q", "-am", "b edits")

	// main: edits p-1, comments on p-2 and renames it earlier, adds memory z.
	git.run(root, "checkout", "-q", "main")
	assert.Require(t, assert.NoErr(t, s.WriteIssues([]Issue{
		mIssue("p-1", func(i *Issue) { i.Priority = 0; i.UpdatedAt = tOlder }),
		mIssue("p-2", func(i *Issue) {
			i.Title = "ours title"
			i.Priority = 1
			i.Comments = []Comment{comment(tBase, "base"), comment(tOlder, "from main")}
			i.UpdatedAt = tOlder
		}),
		mIssue("p-3"),
	})))
	assert.Require(t, assert.NoErr(t, writeLines(filepath.Join(s.Dir, "memories.jsonl"), []Memory{mMemory("m", "c", tBase), mMemory("z", "z", tOlder)})))
	git.run(root, "commit", "-q", "-am", "main edits")

	out := git.run(root, "merge", "--no-edit", "b")
	assert.StrContains(t, out, "issue p-2: title changed on both sides, took theirs")

	issues, err := s.ReadIssues()
	assert.Require(t, assert.NoErr(t, err))
	want := []Issue{
		mIssue("p-1", func(i *Issue) { i.Priority = 0; i.UpdatedAt = tOlder }),
		mIssue("p-2", func(i *Issue) {
			i.Title = "theirs title"
			i.Priority = 1
			i.Comments = []Comment{comment(tBase, "base"), comment(tOlder, "from main"), comment(tNewer, "from b")}
			i.UpdatedAt = tNewer
		}),
		mIssue("p-3", func(i *Issue) { i.Status = "closed"; i.ClosedAt = tOlder; i.UpdatedAt = tOlder }),
	}
	assert.Eq(t, want, issues)
	mems, err := s.ReadMemories()
	assert.Require(t, assert.NoErr(t, err))
	var keys []string
	for _, m := range mems {
		keys = append(keys, m.Key)
	}
	assert.Eq(t, []string{"b", "m", "z"}, keys)
	assert.Eq(t, "", strings.TrimSpace(git.run(root, "status", "--porcelain")))
}
