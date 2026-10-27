package commands

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/tracker"
)

func TestMemoryDoctorArchivePromoteCLI(t *testing.T) {
	t.Setenv("GOFER_JOB_ID", "job-42")
	t.Setenv("GOFER_SESSION_ID", "")
	root := t.TempDir()
	trackerRunOK(t, root, "repo", "init")
	long := strings.Repeat("长", tracker.MemorySummaryRequiredRunes+1)

	out := trackerRunOK(t, root, "memory", "set", "deploy-x", "see `docs/gone.md`", "--kind", "rule", "--json")
	var m tracker.Memory
	assert.Require(t, assert.NoErr(t, json.Unmarshal([]byte(out), &m)))
	assert.Eq(t, "job:job-42", m.Source) // auto-filled from the job environment
	trackerRunOK(t, root, "memory", "set", "handoff-1", long, "--kind", "handoff")
	out, code := trackerCLI(t, root, "memory", "set", "deploy-y", "x", "--doctor-ignore", "nope")
	assert.NotEq(t, 0, code)
	assert.Contains(t, out, "invalid --doctor-ignore")

	// doctor: advisory, exit 0, JSON for the steward.
	out = trackerRunOK(t, root, "memory", "doctor", "--json")
	var report tracker.DoctorReport
	assert.Require(t, assert.NoErr(t, json.Unmarshal([]byte(out), &report)))
	assert.Eq(t, 2, report.Checked)
	assert.Require(t, assert.Len(t, report.Memories, 1))
	assert.Eq(t, "deploy-x", report.Memories[0].Key)
	assert.Eq(t, tracker.DoctorPathMissing, report.Memories[0].Findings[0].Slug)
	out = trackerRunOK(t, root, "memory", "doctor")
	assert.Contains(t, out, "  - path-missing: docs/gone.md")
	assert.Contains(t, trackerRunOK(t, root, "repo", "prime"), "deploy-x（"+tracker.PrimeStaleMarkerIn(i18n.ZhCN)+"）")
	trackerRunOK(t, root, "memory", "set", "deploy-x", "see `docs/gone.md`", "--doctor-ignore", "path-missing")
	out = trackerRunOK(t, root, "memory", "doctor")
	assert.Contains(t, out, "checked 2, with findings 0, suppressed 1")
	assert.Contains(t, trackerRunOK(t, root, "memory", "show", "deploy-x"), "doctor_ignore: path-missing")
	assert.Contains(t, trackerRunOK(t, root, "memory", "ls"), "· 来源 job:job-42")

	// promote: handoff → note needs a summary for long content, clears expiry.
	out, code = trackerCLI(t, root, "memory", "promote", "handoff-1", "--kind", "note")
	assert.NotEq(t, 0, code)
	assert.Contains(t, out, "--summary")
	out = trackerRunOK(t, root, "memory", "promote", "handoff-1", "--kind", "note", "--summary", "沉淀的经验", "--json")
	assert.Require(t, assert.NoErr(t, json.Unmarshal([]byte(out), &m)))
	assert.Eq(t, tracker.MemoryKindNote, m.Kind)
	assert.Empty(t, m.ExpiresAt)

	// archive / ls --archived / restore.
	trackerRunOK(t, root, "memory", "archive", "deploy-x", "--reason", "obsolete")
	assert.NotContains(t, trackerRunOK(t, root, "memory", "ls"), "deploy-x")
	assert.NotContains(t, trackerRunOK(t, root, "repo", "prime"), "deploy-x")
	out = trackerRunOK(t, root, "memory", "ls", "--archived", "gone")
	assert.Contains(t, out, "deploy-x [rule]")
	assert.Contains(t, out, "（obsolete）")
	assert.FileExists(t, filepath.Join(root, ".gofer", "tracker", "memories-archive.jsonl"))
	out, code = trackerCLI(t, root, "memory", "archive", "deploy-x", "--project", "p")
	assert.NotEq(t, 0, code)
	assert.Contains(t, out, "not --project")
	trackerRunOK(t, root, "memory", "restore", "deploy-x")
	assert.Contains(t, trackerRunOK(t, root, "memory", "ls"), "deploy-x")
	assert.Eq(t, "", strings.TrimSpace(trackerRunOK(t, root, "memory", "ls", "--archived")))
}

func TestMemoryTitleCLI(t *testing.T) {
	t.Setenv("GOFER_JOB_ID", "")
	t.Setenv("GOFER_SESSION_ID", "")
	root := t.TempDir()
	trackerRunOK(t, root, "repo", "init")

	var m tracker.Memory
	out := trackerRunOK(t, root, "memory", "set", "deploy", "run make release", "--title", "  发版流程 ", "--summary", "发版前读", "--json")
	assert.Require(t, assert.NoErr(t, json.Unmarshal([]byte(out), &m)))
	assert.Eq(t, "发版流程", m.Title)
	assert.Contains(t, trackerRunOK(t, root, "memory", "show", "deploy"), "title: 发版流程")

	out = trackerRunOK(t, root, "memory", "ls", "--json")
	var list []tracker.Memory
	assert.Require(t, assert.NoErr(t, json.Unmarshal([]byte(out), &list)))
	assert.Require(t, assert.Len(t, list, 1))
	assert.Eq(t, "发版流程", list[0].Title)
	assert.Contains(t, trackerRunOK(t, root, "memory", "ls"), "· 发版流程 — 发版前读")
	assert.Contains(t, trackerRunOK(t, root, "memory", "ls", "发版流"), "deploy")
	assert.Contains(t, trackerRunOK(t, root, "repo", "prime"), "deploy · 发版流程 — 发版前读")

	// Updating without --title keeps it; "-" clears it.
	trackerRunOK(t, root, "memory", "set", "deploy", "run make release v2")
	assert.Contains(t, trackerRunOK(t, root, "memory", "show", "deploy"), "title: 发版流程")
	trackerRunOK(t, root, "memory", "set", "deploy", "run make release v2", "--title", "-")
	assert.NotContains(t, trackerRunOK(t, root, "memory", "show", "deploy"), "title:")

	// Length and line-break validation.
	out, code := trackerCLI(t, root, "memory", "set", "bad", "x", "--title", strings.Repeat("长", tracker.MemoryTitleMaxRunes+1))
	assert.NotEq(t, 0, code)
	assert.Contains(t, out, "title is 81 chars")
	out, code = trackerCLI(t, root, "memory", "set", "bad", "x", "--title", "a\nb")
	assert.NotEq(t, 0, code)
	assert.Contains(t, out, "single line")
}
