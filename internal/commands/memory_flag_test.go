package commands

import (
	"encoding/json"
	"testing"

	"github.com/gookit/goutil/x/assert"
	"github.com/inhere/gofer/internal/tracker"
)

func TestMemoryFlagUnflagCLI(t *testing.T) {
	t.Setenv("GOFER_JOB_ID", "job-7")
	t.Setenv("GOFER_SESSION_ID", "")
	root := t.TempDir()
	trackerRunOK(t, root, "repo", "init")
	trackerRunOK(t, root, "memory", "set", "verify", "run make test", "--kind", "rule")

	out, code := trackerCLI(t, root, "memory", "flag", "verify")
	assert.NotEq(t, 0, code)
	assert.Contains(t, out, "--reason is required")

	out = trackerRunOK(t, root, "memory", "flag", "verify", "--reason", "target renamed to check", "--json")
	var m tracker.Memory
	assert.Require(t, assert.NoErr(t, json.Unmarshal([]byte(out), &m)))
	assert.Require(t, assert.Len(t, m.Flags, 1))
	assert.Eq(t, "job-7", m.Flags[0].Job)

	assert.Contains(t, trackerRunOK(t, root, "memory", "show", "verify"), "target renamed to check")
	assert.Contains(t, trackerRunOK(t, root, "repo", "prime"), "⚠ 待复核（target renamed to check） verify: run make test")
	assert.Contains(t, trackerRunOK(t, root, "memory", "doctor"), "  - flagged: 1 次 · 最近：target renamed to check · job:job-7")

	out = trackerRunOK(t, root, "memory", "unflag", "verify", "--json")
	m = tracker.Memory{}
	assert.Require(t, assert.NoErr(t, json.Unmarshal([]byte(out), &m)))
	assert.Empty(t, m.Flags)
	assert.NotContains(t, trackerRunOK(t, root, "repo", "prime"), "待复核")
}
