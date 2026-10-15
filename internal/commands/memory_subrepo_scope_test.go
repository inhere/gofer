package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/tracker"
)

// TestMemorySetAutoScopeNestedRepo: `memory set` in a workspace root that holds several
// checkouts narrows a NEW memory to the checkout its cwd is in, unless an explicit
// --when-paths says otherwise, and never touches the `when` of an update (gofer-opv5).
func TestMemorySetAutoScopeNestedRepo(t *testing.T) {
	root := t.TempDir()
	trackerRunOK(t, root, "repo", "init")
	nested := filepath.Join(root, "deploy-hub")
	assert.NoErr(t, os.MkdirAll(filepath.Join(nested, ".git"), 0o755))
	assert.NoErr(t, os.MkdirAll(filepath.Join(nested, "src"), 0o755))

	pathsOf := func(cwd, key string) []string {
		t.Helper()
		out := trackerRunOK(t, cwd, "memory", "show", key, "--json")
		var item tracker.Memory
		assert.Require(t, assert.NoErr(t, json.Unmarshal([]byte(out), &item)))
		if item.When == nil {
			return nil
		}
		return item.When.Paths
	}

	// A new memory written inside the nested checkout is scoped to it, and says so (the
	// note also points at the two ways to stay repo- or workspace-wide).
	out := trackerRunOK(t, nested, "memory", "set", "hub-1", "deploy-hub 的发布步骤")
	assert.Contains(t, out, "适用路径已设为 deploy-hub/**")
	assert.Contains(t, out, "整仓通用用 --when-paths '**'")
	assert.Contains(t, out, "工作区通用请写成项目记忆 --project")
	assert.Eq(t, []string{"deploy-hub/**"}, pathsOf(nested, "hub-1"))

	// A subdirectory of the checkout scopes to the checkout, not to the subdirectory.
	trackerRunOK(t, filepath.Join(nested, "src"), "memory", "set", "hub-2", "x")
	assert.Eq(t, []string{"deploy-hub/**"}, pathsOf(nested, "hub-2"))

	// The workspace root itself: workspace-wide.
	trackerRunOK(t, root, "memory", "set", "all-1", "工作区通用约定")
	assert.Empty(t, pathsOf(root, "all-1"))

	// Explicit --when-paths wins over the auto scope; '**' is how a memory says "the whole
	// repository".
	trackerRunOK(t, nested, "memory", "set", "hub-3", "x", "--when-paths", "web/**")
	assert.Eq(t, []string{"web/**"}, pathsOf(nested, "hub-3"))
	trackerRunOK(t, nested, "memory", "set", "all-2", "x", "--when-paths", "**")
	assert.Eq(t, []string{"**"}, pathsOf(root, "all-2"))

	// An update keeps the stored when (scoped stays scoped, unscoped stays unscoped).
	trackerRunOK(t, nested, "memory", "set", "all-1", "工作区通用约定（更新）")
	assert.Empty(t, pathsOf(root, "all-1"))
	trackerRunOK(t, root, "memory", "set", "hub-1", "deploy-hub 的发布步骤（更新）")
	assert.Eq(t, []string{"deploy-hub/**"}, pathsOf(root, "hub-1"))
	// An explicitly empty --when-paths clears the scope (repo-wide again); `-` too.
	trackerRunOK(t, nested, "memory", "set", "hub-1", "deploy-hub 的发布步骤（整仓通用）", "--when-paths", "")
	assert.Empty(t, pathsOf(root, "hub-1"))
	trackerRunOK(t, nested, "memory", "set", "hub-3", "x", "--when-paths", "-")
	assert.Empty(t, pathsOf(root, "hub-3"))

	// --json stays machine-readable: the note is not appended to it.
	out = trackerRunOK(t, nested, "memory", "set", "hub-4", "x", "--json")
	var item tracker.Memory
	assert.Require(t, assert.NoErr(t, json.Unmarshal([]byte(out), &item)))
	assert.Eq(t, []string{"deploy-hub/**"}, item.When.Paths)
}
