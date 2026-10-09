package commands

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"
	"github.com/inhere/gofer/internal/tracker"
)

func TestIssueStaleAndFromCLI(t *testing.T) {
	root := t.TempDir()
	trackerRunOK(t, root, "repo", "init")
	s := tracker.NewStore(filepath.Join(root, ".gofer", "tracker"))
	assert.Require(t, assert.NoErr(t, s.WriteIssues([]tracker.Issue{
		{ID: "x-old", Title: "Old work", Status: "open", Priority: 2, CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-02T00:00:00Z"},
		{ID: "x-new", Title: "New work", Status: "open", Priority: 2, CreatedAt: tracker.Now()},
	})))
	out := trackerRunOK(t, root, "issue", "ls", "--stale")
	assert.Contains(t, out, "x-old [open] P2 Old work · 更新于 ")
	assert.NotContains(t, out, "x-new")
	out = trackerRunOK(t, root, "issue", "ls", "--stale", "--days", "100000")
	assert.Eq(t, "", strings.TrimSpace(out))

	out = trackerRunOK(t, root, "issue", "create", "Found while working", "--from", "x-old", "--json")
	var created tracker.Issue
	assert.Require(t, assert.NoErr(t, json.Unmarshal([]byte(out), &created)))
	assert.Eq(t, []tracker.Dep{{ID: "x-old", Type: tracker.DepDiscoveredFrom}}, created.Deps)
	assert.Contains(t, trackerRunOK(t, root, "issue", "show", created.ID), "discovered-from: x-old\n")
	assert.Contains(t, trackerRunOK(t, root, "issue", "show", "x-old"), "linked-from: "+created.ID+" (discovered-from)")
	// discovered-from does not gate ready.
	assert.Contains(t, trackerRunOK(t, root, "issue", "ready"), created.ID)
	_, code := trackerCLI(t, root, "issue", "create", "bad", "--from", "x-missing")
	assert.NotEq(t, 0, code)
}
