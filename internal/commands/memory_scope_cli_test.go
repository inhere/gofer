package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/tracker"
)

// TestMemoryScopeCLI: `memory scope` suggests the subproject of an unscoped memory (and
// only writes it with --apply, high confidence by default), and never touches a memory
// that already has a when.paths (gofer-opv5).
func TestMemoryScopeCLI(t *testing.T) {
	root := t.TempDir()
	trackerRunOK(t, root, "repo", "init")
	for _, dir := range []string{"codesys-cli", "hmi-cli", "deploy-hub"} {
		assert.NoErr(t, os.MkdirAll(filepath.Join(root, dir, ".git"), 0o755))
	}
	pathsOf := func(key string) []string {
		t.Helper()
		out := trackerRunOK(t, root, "memory", "show", key, "--json")
		var item tracker.Memory
		assert.Require(t, assert.NoErr(t, json.Unmarshal([]byte(out), &item)))
		if item.When == nil {
			return nil
		}
		return item.When.Paths
	}
	trackerRunOK(t, root, "memory", "set", "codesys-cli-login", "登录流程固定")
	trackerRunOK(t, root, "memory", "set", "hmicli-1", "生成脚本要跑两遍")
	trackerRunOK(t, root, "memory", "set", "misc-1", "编译 hmi-cli 时先跑生成脚本")
	trackerRunOK(t, root, "memory", "set", "general-1", "提交前跑测试")
	trackerRunOK(t, root, "memory", "set", "multi-1", "codesys-cli 与 deploy-hub 共用同一套脚本")
	trackerRunOK(t, root, "memory", "set", "scoped-1", "x", "--when-paths", "web/**")

	// --suggest (the default): a table, nothing written.
	out := trackerRunOK(t, root, "memory", "scope")
	for _, want := range []string{"codesys-cli-login", "codesys-cli/**", "[高]", "hmicli-1", "hmi-cli/**", "misc-1", "[中]", "gofer memory scope --apply"} {
		assert.Contains(t, out, want)
	}
	assert.NotContains(t, out, "general-1")
	assert.NotContains(t, out, "multi-1")
	assert.NotContains(t, out, "scoped-1")
	assert.Len(t, pathsOf("codesys-cli-login"), 0)

	var report memoryScopeOutput
	assert.Require(t, assert.NoErr(t, json.Unmarshal([]byte(trackerRunOK(t, root, "memory", "scope", "--json")), &report)))
	assert.Len(t, report.Suggestions, 3)
	assert.Eq(t, 1, report.AlreadyScoped)
	assert.Eq(t, 2, report.Unresolved)

	// --apply writes the high-confidence ones only, and says what it skipped.
	out = trackerRunOK(t, root, "memory", "scope", "--apply")
	assert.Contains(t, out, "set codesys-cli-login -> codesys-cli/**")
	assert.Contains(t, out, "skipped 1 weaker suggestions: misc-1")
	assert.Eq(t, []string{"codesys-cli/**"}, pathsOf("codesys-cli-login"))
	assert.Eq(t, []string{"hmi-cli/**"}, pathsOf("hmicli-1"))
	assert.Len(t, pathsOf("misc-1"), 0)

	// --min-confidence medium lifts the threshold, and --only narrows the selection.
	trackerRunOK(t, root, "memory", "scope", "--apply", "--min-confidence", "medium")
	assert.Eq(t, []string{"hmi-cli/**"}, pathsOf("misc-1"))
	assert.Eq(t, []string{"web/**"}, pathsOf("scoped-1")) // an existing scope is never rewritten
	out = trackerRunOK(t, root, "memory", "scope", "--apply", "--only", "general-1,multi-1")
	assert.Contains(t, out, "applied 0 memories")
	assert.Contains(t, out, "no suggestion for: general-1, multi-1")
	assert.Len(t, pathsOf("general-1"), 0)

	// Bad input and the wrong store are refused.
	refused := []struct {
		args []string
		want string
	}{
		{[]string{"memory", "scope", "--suggest", "--apply"}, "mutually exclusive"},
		{[]string{"memory", "scope", "--min-confidence", "low"}, "high|medium"},
		{[]string{"memory", "scope", "--only", ","}, "at least one memory key"},
		{[]string{"memory", "scope", "--global"}, "--global"},
		{[]string{"memory", "scope", "--project", "self"}, "--project"},
	}
	for _, tc := range refused {
		out, code := trackerCLI(t, root, tc.args...)
		assert.NotEq(t, 0, code)
		assert.Contains(t, out, tc.want)
	}
}

// TestMemoryScopeCLIWithoutSubrepos: a tracker whose root has no nested checkout has
// nothing to suggest and stays quiet about it.
func TestMemoryScopeCLIWithoutSubrepos(t *testing.T) {
	root := t.TempDir()
	trackerRunOK(t, root, "repo", "init")
	trackerRunOK(t, root, "memory", "set", "k1", "普通记忆")
	out := trackerRunOK(t, root, "memory", "scope")
	assert.Contains(t, out, "0 suggestions: 0 memories already scoped, 1 with no subproject evidence")
	assert.True(t, strings.Contains(trackerRunOK(t, root, "memory", "scope", "--json"), `"unresolved":1`))
}
