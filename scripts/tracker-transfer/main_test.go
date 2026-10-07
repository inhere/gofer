package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/gookit/goutil/x/assert"
	"github.com/inhere/gofer/internal/tracker"
)

func TestOfflineTransferCLIOnFixture(t *testing.T) {
	sourceRoot := filepath.Join(t.TempDir(), "source repo")
	assert.Require(t, assert.NoErr(t, os.MkdirAll(sourceRoot, 0o755)))
	source, _, err := tracker.Init(sourceRoot, "old", true)
	assert.Require(t, assert.NoErr(t, err))
	assert.Require(t, assert.NoErr(t, source.WriteIssues([]tracker.Issue{{ID: "old-1", Title: "fixture", Status: "closed", CreatedAt: "2026-01-01T00:00:00Z"}})))
	bundlePath := filepath.Join(t.TempDir(), "bundle.json")
	assert.Require(t, assert.NoErr(t, run([]string{"inspect", "--source-tracker", source.Dir, "--query", "fixture"})))
	issuePath := filepath.Join(source.Dir, "issues.jsonl")
	before, err := os.ReadFile(issuePath)
	assert.Require(t, assert.NoErr(t, err))
	assert.Require(t, assert.Err(t, run([]string{"export", "--source-tracker", source.Dir, "--bundle", issuePath, "--prefix", "gofer", "--project-key", "gofer", "--issue-id", "old-1"})))
	after, err := os.ReadFile(issuePath)
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, string(before), string(after))
	assert.Require(t, assert.NoErr(t, run([]string{"export", "--source-tracker", source.Dir, "--bundle", bundlePath, "--prefix", "gofer", "--project-key", "gofer", "--issue-id", "old-1"})))
	targetRoot := filepath.Join(t.TempDir(), "target repo")
	cmd := exec.Command("git", "init", targetRoot)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git init fixture: %v: %s", err, output)
	}
	assert.Require(t, assert.NoErr(t, run([]string{"import", "--source-tracker", source.Dir, "--bundle", bundlePath, "--target-root", targetRoot})))
	target, err := tracker.Discover(targetRoot, "")
	assert.Require(t, assert.NoErr(t, err))
	issues, err := target.ReadIssues()
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, "old-1", issues[0].ID)
}
