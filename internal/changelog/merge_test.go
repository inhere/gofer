package changelog

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

// repoDoc builds a changelog: header + the given sections.
func repoDoc(sections ...string) []byte {
	var b strings.Builder
	b.WriteString("# 更新日志\n\n维护说明。\n\n")
	b.WriteString(strings.Join(sections, ""))
	return []byte(b.String())
}

func mergeOK(t *testing.T, base, ours, theirs []byte) Result {
	t.Helper()
	res, err := Merge(base, ours, theirs, Options{})
	assert.Require(t, assert.NoErr(t, err))
	assert.False(t, res.Conflict)
	return res
}

func assertDoc(t *testing.T, want string, got []byte) {
	t.Helper()
	assert.Eq(t, want, string(got))
}

const unreleased = "## [未发布]\n\n### 新功能\n\n- 已有功能\n\n### 改进\n\n- 已有改进\n\n"

const released = "## [0.1.0] - 2026-01-01\n\n### 新功能\n\n- 旧功能\n\n"

func TestMergeIdentity(t *testing.T) {
	doc := repoDoc(unreleased, released)
	res := mergeOK(t, doc, doc, doc)
	assertDoc(t, string(doc), res.Merged)
}

func TestMergeSameSubsectionAddsBothSides(t *testing.T) {
	base := repoDoc(unreleased, released)
	ours := repoDoc(
		"## [未发布]\n\n### 新功能\n\n- 已有功能\n- 我方条目\n\n### 改进\n\n- 已有改进\n\n", released)
	theirs := repoDoc(
		"## [未发布]\n\n### 新功能\n\n- 已有功能\n- 对方条目\n\n### 改进\n\n- 已有改进\n\n", released)
	res := mergeOK(t, base, ours, theirs)
	assertDoc(t, string(repoDoc(
		"## [未发布]\n\n### 新功能\n\n- 已有功能\n- 我方条目\n- 对方条目\n\n### 改进\n\n- 已有改进\n\n", released)), res.Merged)
}

func TestMergeDifferentSubsections(t *testing.T) {
	base := repoDoc(unreleased, released)
	ours := repoDoc(
		"## [未发布]\n\n### 新功能\n\n- 已有功能\n- 我方条目\n\n### 改进\n\n- 已有改进\n\n", released)
	theirs := repoDoc(
		"## [未发布]\n\n### 新功能\n\n- 已有功能\n\n### 改进\n\n- 已有改进\n- 对方改进\n\n", released)
	res := mergeOK(t, base, ours, theirs)
	assertDoc(t, string(repoDoc(
		"## [未发布]\n\n### 新功能\n\n- 已有功能\n- 我方条目\n\n### 改进\n\n- 已有改进\n- 对方改进\n\n", released)), res.Merged)
}

func TestMergeSubsectionAddedByOneSide(t *testing.T) {
	base := repoDoc(unreleased, released)
	ours := base
	theirs := repoDoc(
		"## [未发布]\n\n### 新功能\n\n- 已有功能\n\n### 改进\n\n- 已有改进\n\n### 修复\n\n- 修了个 bug\n\n", released)
	res := mergeOK(t, base, ours, theirs)
	assertDoc(t, string(repoDoc(
		"## [未发布]\n\n### 新功能\n\n- 已有功能\n\n### 改进\n\n- 已有改进\n\n### 修复\n\n- 修了个 bug\n\n", released)), res.Merged)
}

// TestMergeMultiLineEntries: an entry covers its continuation lines and indented
// sub-items, so appended entries stay whole.
func TestMergeMultiLineEntries(t *testing.T) {
	base := repoDoc(unreleased, released)
	oursEntry := "- **条目 A**：\n  - 子项 1\n  - 子项 2\n"
	theirsEntry := "- **条目 B（gofer-1234）**：\n  一段说明\n\n  - 子项 3\n"
	ours := repoDoc("## [未发布]\n\n### 新功能\n\n- 已有功能\n"+oursEntry+"\n### 改进\n\n- 已有改进\n\n", released)
	theirs := repoDoc("## [未发布]\n\n### 新功能\n\n- 已有功能\n"+theirsEntry+"\n### 改进\n\n- 已有改进\n\n", released)
	res := mergeOK(t, base, ours, theirs)
	assertDoc(t, string(repoDoc(
		"## [未发布]\n\n### 新功能\n\n- 已有功能\n"+oursEntry+theirsEntry+"\n### 改进\n\n- 已有改进\n\n", released)), res.Merged)
}

// TestMergeSameEntryChangedBothSides: one entry rewritten differently on each
// side is a real conflict.
func TestMergeSameEntryChangedBothSides(t *testing.T) {
	base := repoDoc(unreleased, released)
	ours := repoDoc("## [未发布]\n\n### 新功能\n\n- 已有功能，我方改写\n\n### 改进\n\n- 已有改进\n\n", released)
	theirs := repoDoc("## [未发布]\n\n### 新功能\n\n- 已有功能，对方改写\n\n### 改进\n\n- 已有改进\n\n", released)
	res, err := Merge(base, ours, theirs, Options{})
	assert.Require(t, assert.NoErr(t, err))
	assert.True(t, res.Conflict)
	got := string(res.Merged)
	assert.StrContains(t, got, "<<<<<<< ours\n- 已有功能，我方改写\n=======\n- 已有功能，对方改写\n>>>>>>> theirs")
	assert.StrContains(t, got, "### 改进\n\n- 已有改进\n")
	assert.Eq(t, 1, len(res.Notes))
}

// TestMergeDeletedEntry: an entry one side removed and the other left alone goes
// away; removed on one side and rewritten on the other conflicts.
func TestMergeDeletedEntry(t *testing.T) {
	withEntry := repoDoc("## [未发布]\n\n### 新功能\n\n- 已有功能\n- 保留\n\n### 改进\n\n- 已有改进\n\n", released)
	oursDel := repoDoc("## [未发布]\n\n### 新功能\n\n- 保留\n\n### 改进\n\n- 已有改进\n\n", released)
	res := mergeOK(t, withEntry, oursDel, withEntry)
	assertDoc(t, string(repoDoc(
		"## [未发布]\n\n### 新功能\n\n- 保留\n\n### 改进\n\n- 已有改进\n\n", released)), res.Merged)

	// removed on ours, rewritten on theirs
	theirsMod := repoDoc("## [未发布]\n\n### 新功能\n\n- 已有功能，对方改写\n- 保留\n\n### 改进\n\n- 已有改进\n\n", released)
	res, err := Merge(withEntry, oursDel, theirsMod, Options{})
	assert.Require(t, assert.NoErr(t, err))
	assert.True(t, res.Conflict)
	assert.StrContains(t, string(res.Merged), ">>>>>>> theirs")
}

// TestMergeEntryChangedOnOneSideOnly: an entry only theirs rewrote is theirs'
// text, not a conflict; ours did not touch it.
func TestMergeEntryChangedOnOneSideOnly(t *testing.T) {
	base := repoDoc(unreleased, released)
	theirs := repoDoc("## [未发布]\n\n### 新功能\n\n- 已有功能，对方改写\n\n### 改进\n\n- 已有改进\n\n", released)
	res := mergeOK(t, base, base, theirs)
	assertDoc(t, string(theirs), res.Merged)
	assert.Eq(t, 0, len(res.Notes))
}

// TestMergeEntryChangedByTheirsAfterOursAdded: ours appended an entry right after
// an entry theirs rewrote byte for byte differently. The neighbouring addition is
// no change of the old entry on ours: theirs' text wins and nothing conflicts.
func TestMergeEntryChangedByTheirsAfterOursAdded(t *testing.T) {
	base := repoDoc(unreleased, released)
	ours := repoDoc("## [未发布]\n\n### 新功能\n\n- 已有功能\n- 我方新增\n\n### 改进\n\n- 已有改进\n\n", released)
	theirs := repoDoc("## [未发布]\n\n### 新功能\n\n- 已有功能，对方改写\n\n### 改进\n\n- 已有改进\n\n", released)
	res := mergeOK(t, base, ours, theirs)
	assertDoc(t, string(repoDoc(
		"## [未发布]\n\n### 新功能\n\n- 已有功能，对方改写\n- 我方新增\n\n### 改进\n\n- 已有改进\n\n", released)), res.Merged)
}

// TestMergeEntryChangedByTheirsBeforeOursAdded: same with ours' entry inserted in
// front of the entry theirs rewrote.
func TestMergeEntryChangedByTheirsBeforeOursAdded(t *testing.T) {
	base := repoDoc(unreleased, released)
	ours := repoDoc("## [未发布]\n\n### 新功能\n\n- 我方新增\n- 已有功能\n\n### 改进\n\n- 已有改进\n\n", released)
	theirs := repoDoc("## [未发布]\n\n### 新功能\n\n- 已有功能，对方改写\n\n### 改进\n\n- 已有改进\n\n", released)
	res := mergeOK(t, base, ours, theirs)
	assertDoc(t, string(repoDoc(
		"## [未发布]\n\n### 新功能\n\n- 我方新增\n- 已有功能，对方改写\n\n### 改进\n\n- 已有改进\n\n", released)), res.Merged)
}

// TestMergeEntryChangedAndAddedByTheirs: theirs rewrote an entry and added one
// after it while ours left the subsection alone: both of theirs' changes land.
func TestMergeEntryChangedAndAddedByTheirs(t *testing.T) {
	base := repoDoc(unreleased, released)
	theirs := repoDoc("## [未发布]\n\n### 新功能\n\n- 已有功能，对方改写\n- 对方新增\n\n### 改进\n\n- 已有改进\n\n", released)
	res := mergeOK(t, base, base, theirs)
	assertDoc(t, string(theirs), res.Merged)
}

// TestMergeEntriesAddedAtSamePosition: both sides add a different entry in the
// same place; ours' order is kept, theirs' additions follow.
func TestMergeEntriesAddedAtSamePosition(t *testing.T) {
	base := repoDoc(unreleased, released)
	ours := repoDoc("## [未发布]\n\n### 新功能\n\n- 我方新增\n- 已有功能\n\n### 改进\n\n- 已有改进\n\n", released)
	theirs := repoDoc("## [未发布]\n\n### 新功能\n\n- 对方新增\n- 已有功能\n\n### 改进\n\n- 已有改进\n\n", released)
	res := mergeOK(t, base, ours, theirs)
	assertDoc(t, string(repoDoc(
		"## [未发布]\n\n### 新功能\n\n- 我方新增\n- 已有功能\n- 对方新增\n\n### 改进\n\n- 已有改进\n\n", released)), res.Merged)
}

// TestMergeRealChangelogOneSideEntryChange: the merge that used to report
// "changed on both sides" for an entry ours never touched. Ours only appended a
// new entry behind it, theirs rewrote one of its sub-lines.
func TestMergeRealChangelogOneSideEntryChange(t *testing.T) {
	base := readTestdata(t, "real-base.md")
	ours := readTestdata(t, "real-ours.md")
	theirs := readTestdata(t, "real-theirs.md")
	res := mergeOK(t, base, ours, theirs)
	assert.Eq(t, 0, len(res.Notes))
	got := string(res.Merged)
	assert.False(t, strings.Contains(got, "<<<<<<<"))
	// theirs' rewrite of the sub-line replaces ours' (unchanged) copy
	assert.StrContains(t, got, "- 步骤与 fan 新增 `env`、`ref`（执行侧见下一条）")
	assert.False(t, strings.Contains(got, "下发 worker 待后续版本，提交时明确报「暂不支持」"))
	// ours' and theirs' new entries are both there
	assert.StrContains(t, got, "- **workflow 模板可以查看源文件、保存与删除**")
	assert.StrContains(t, got, "- **workflow 的 `ref:` 临时检出与 `env` 下发 worker**")
}

// TestMergeReleasedSectionConflict: outside the unreleased section the driver
// leaves git's own markers and a non-zero exit.
func TestMergeReleasedSectionConflict(t *testing.T) {
	base := repoDoc(unreleased, released)
	ours := repoDoc(unreleased, "## [0.1.0] - 2026-01-01\n\n### 新功能\n\n- 旧功能，我方改\n\n")
	theirs := repoDoc(unreleased, "## [0.1.0] - 2026-01-01\n\n### 新功能\n\n- 旧功能，对方改\n\n")
	res, err := Merge(base, ours, theirs, Options{})
	assert.Require(t, assert.NoErr(t, err))
	assert.True(t, res.Conflict)
	got := string(res.Merged)
	assert.StrContains(t, got, "<<<<<<< ours\n- 旧功能，我方改\n=======\n- 旧功能，对方改\n>>>>>>> theirs")
	// the unreleased section still merged cleanly above the conflict
	assert.StrContains(t, got, "## [未发布]\n\n### 新功能\n\n- 已有功能\n\n### 改进\n\n- 已有改进\n\n")
}

// TestMergeReleaseRename: one side released (renamed the unreleased heading to a
// version, opened a new empty section); the other side's new entries land in the
// new unreleased section.
func TestMergeReleaseRename(t *testing.T) {
	base := repoDoc(unreleased, released)
	ours := repoDoc(
		"## [未发布]\n",
		"## [0.2.0] - 2026-02-02\n\n### 新功能\n\n- 已有功能\n\n### 改进\n\n- 已有改进\n\n",
		released)
	theirs := repoDoc(
		"## [未发布]\n\n### 新功能\n\n- 已有功能\n- 对方条目\n\n### 改进\n\n- 已有改进\n\n", released)
	res := mergeOK(t, base, ours, theirs)
	assertDoc(t, string(repoDoc(
		"## [未发布]\n\n### 新功能\n\n- 对方条目\n\n",
		"## [0.2.0] - 2026-02-02\n\n### 新功能\n\n- 已有功能\n\n### 改进\n\n- 已有改进\n\n",
		released)), res.Merged)
}

// TestMergeUnreleasedHeadingTitles: both spellings of the unreleased heading are
// recognized, an extra title can be configured.
func TestMergeUnreleasedHeadingTitles(t *testing.T) {
	base := repoDoc("## [Unreleased]\n\n### Added\n\n- a\n\n", released)
	ours := repoDoc("## [Unreleased]\n\n### Added\n\n- a\n- ours\n\n", released)
	theirs := repoDoc("## [Unreleased]\n\n### Added\n\n- a\n- theirs\n\n", released)
	res, err := Merge(base, ours, theirs, Options{})
	assert.Require(t, assert.NoErr(t, err))
	assert.StrContains(t, string(res.Merged), "- a\n- ours\n- theirs\n")

	base = repoDoc("## [待发布]\n\n### Added\n\n- a\n\n", released)
	ours = repoDoc("## [待发布]\n\n### Added\n\n- a\n- ours\n\n", released)
	theirs = repoDoc("## [待发布]\n\n### Added\n\n- a\n- theirs\n\n", released)
	res, err = Merge(base, ours, theirs, Options{UnreleasedTitles: []string{"待发布"}})
	assert.Require(t, assert.NoErr(t, err))
	assert.False(t, res.Conflict)
	assert.StrContains(t, string(res.Merged), "## [待发布]\n\n### Added\n\n- a\n- ours\n- theirs\n")
}

// TestRunDriverFiles: the CLI facing entry writes the result into ours and
// reports the conflict through its exit code.
func TestRunDriverFiles(t *testing.T) {
	base := repoDoc(unreleased, released)
	ours := repoDoc("## [未发布]\n\n### 新功能\n\n- 已有功能\n- 我方条目\n\n### 改进\n\n- 已有改进\n\n", released)
	theirs := repoDoc("## [未发布]\n\n### 新功能\n\n- 已有功能\n- 对方条目\n\n### 改进\n\n- 已有改进\n\n", released)
	b, o, th := driverFiles(t, base, ours, theirs)
	var stderr bytes.Buffer
	assert.Eq(t, 0, RunDriver(b, o, th, "CHANGELOG.md", Options{}, &stderr), stderr.String())
	got, err := os.ReadFile(o)
	assert.Require(t, assert.NoErr(t, err))
	assert.StrContains(t, string(got), "- 我方条目\n- 对方条目\n")

	conflictOurs := repoDoc("## [未发布]\n\n### 新功能\n\n- 已有功能，我方改写\n\n### 改进\n\n- 已有改进\n\n", released)
	conflictTheirs := repoDoc("## [未发布]\n\n### 新功能\n\n- 已有功能，对方改写\n\n### 改进\n\n- 已有改进\n\n", released)
	b, o, th = driverFiles(t, base, conflictOurs, conflictTheirs)
	stderr.Reset()
	assert.Eq(t, 1, RunDriver(b, o, th, "CHANGELOG.md", Options{}, &stderr))
	assert.StrContains(t, stderr.String(), "changed on both sides")
	got, _ = os.ReadFile(o)
	assert.StrContains(t, string(got), "<<<<<<< ours")
}

// TestInstallChangelogDriver: the .gitattributes line and the git config entries
// are written once, a second run changes nothing.
func TestInstallChangelogDriver(t *testing.T) {
	ctx := context.Background()
	git := newHermeticGit(t)

	plain := t.TempDir()
	res, err := Install(ctx, git.runner(), plain, "CHANGELOG.md")
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, "not inside a git work tree", res.Skipped)
	_, statErr := os.Stat(filepath.Join(plain, ".gitattributes"))
	assert.True(t, os.IsNotExist(statErr))

	root := t.TempDir()
	git.run(root, "init", "-q")
	attrs := filepath.Join(root, ".gitattributes")
	assert.Require(t, assert.NoErr(t, os.WriteFile(attrs, []byte("* text=auto eol=lf"), 0o644)))
	res, err = Install(ctx, git.runner(), root, "CHANGELOG.md")
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, Setup{Attributes: attrs, AttributesAdded: true, ConfigChanged: true, File: "CHANGELOG.md"}, res)
	b, _ := os.ReadFile(attrs)
	assert.Eq(t, "* text=auto eol=lf\nCHANGELOG.md merge=gofer-changelog\n", string(b))
	assert.Eq(t, DriverCommand+"\n", git.run(root, "config", "--get", "merge.gofer-changelog.driver"))
	assert.Eq(t, DriverDesc+"\n", git.run(root, "config", "--get", "merge.gofer-changelog.name"))

	res, err = Install(ctx, git.runner(), root, "CHANGELOG.md")
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, Setup{Attributes: attrs, File: "CHANGELOG.md"}, res)
	b2, _ := os.ReadFile(attrs)
	assert.Eq(t, string(b), string(b2))
}

// TestChangelogDriverGitEndToEnd: `git merge` of two branches that both added
// entries to the unreleased section succeeds through the installed driver.
func TestChangelogDriverGitEndToEnd(t *testing.T) {
	git := newHermeticGit(t)
	root := t.TempDir()
	git.run(root, "init", "-q", "-b", "main")
	git.run(root, "commit", "-q", "--allow-empty", "-m", "root")
	_, err := Install(context.Background(), git.runner(), root, "CHANGELOG.md")
	assert.Require(t, assert.NoErr(t, err))
	// The real driver is `gofer …` from PATH; the test runs the same entry point
	// through the testcmd helper instead (G053).
	driver := filepath.ToSlash(testcmd.Path(t))
	git.run(root, "config", "merge.gofer-changelog.driver", `"`+driver+`" changelog-merge %O %A %B %P`+DriverFallback)

	path := filepath.Join(root, "CHANGELOG.md")
	assert.Require(t, assert.NoErr(t, os.WriteFile(path, repoDoc(unreleased, released), 0o644)))
	git.run(root, "add", "-A")
	git.run(root, "commit", "-q", "-m", "base")

	git.run(root, "checkout", "-q", "-b", "b")
	assert.Require(t, assert.NoErr(t, os.WriteFile(path, repoDoc(
		"## [未发布]\n\n### 新功能\n\n- 已有功能\n- 对方条目\n\n### 改进\n\n- 已有改进\n\n", released), 0o644)))
	git.run(root, "commit", "-q", "-am", "theirs")

	git.run(root, "checkout", "-q", "main")
	assert.Require(t, assert.NoErr(t, os.WriteFile(path, repoDoc(
		"## [未发布]\n\n### 新功能\n\n- 已有功能\n- 我方条目\n\n### 改进\n\n- 已有改进\n\n", released), 0o644)))
	git.run(root, "commit", "-q", "-am", "ours")

	git.run(root, "merge", "--no-edit", "b")
	merged, err := os.ReadFile(path)
	assert.Require(t, assert.NoErr(t, err))
	assert.StrContains(t, string(merged), "- 已有功能\n- 我方条目\n- 对方条目\n")
	assert.Eq(t, "", strings.TrimSpace(git.run(root, "status", "--porcelain")))
}

// --- test helpers -------------------------------------------------------

// readTestdata reads a file of the testdata directory.
func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	assert.Require(t, assert.NoErr(t, err))
	return b
}

func driverFiles(t *testing.T, base, ours, theirs []byte) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "base"), filepath.Join(dir, "ours"), filepath.Join(dir, "theirs")}
	for i, b := range [][]byte{base, ours, theirs} {
		assert.Require(t, assert.NoErr(t, os.WriteFile(paths[i], b, 0o644)))
	}
	return paths[0], paths[1], paths[2]
}

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

func (g hermeticGit) run(dir string, args ...string) string {
	g.t.Helper()
	out, err := g.cmd(dir, args...).CombinedOutput()
	assert.Require(g.t, assert.NoErr(g.t, err, "git %s: %s", strings.Join(args, " "), out))
	return string(out)
}

func (g hermeticGit) runner() GitRunner {
	return func(_ context.Context, dir string, args ...string) (string, error) {
		out, err := g.cmd(dir, args...).Output()
		return string(out), err
	}
}
