package tracker

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

func transferFixture(t *testing.T) (string, string) {
	t.Helper()
	sourceRoot := filepath.Join(t.TempDir(), "source repo")
	assert.Require(t, assert.NoErr(t, os.MkdirAll(sourceRoot, 0o755)))
	source, _, err := Init(sourceRoot, "old", true)
	assert.Require(t, assert.NoErr(t, err))
	issues := []Issue{
		{ID: "old-a", Title: "root", Type: "feature", Status: "open", Priority: 1, Description: "description", Design: "design", AcceptanceCriteria: "done", Notes: []NoteEntry{{At: "2026-01-02T03:04:05Z", By: "author", Text: "note"}}, Assignee: "worker", Owner: "owner", Tags: []string{"move"}, Comments: []Comment{{At: "2026-01-02T03:04:06Z", By: "reviewer", Text: "comment"}}, CreatedAt: "2026-01-01T00:00:00Z", CreatedBy: "creator", UpdatedAt: "2026-01-02T00:00:00Z", StartedAt: "2026-01-02T01:00:00Z", ExternalRef: "EXT-1", SpecID: "spec-1"},
		{ID: "old-b", Title: "child", Type: "task", Status: "in_progress", Priority: 2, Parent: "old-a", CreatedAt: "2026-01-01T00:00:00Z", Tags: []string{"move"}},
		{ID: "old-c", Title: "dependency", Type: "bug", Status: "blocked", Priority: 3, Deps: []Dep{{ID: "old-a", Type: "blocks"}}, CreatedAt: "2026-01-01T00:00:00Z", Tags: []string{"move"}},
		{ID: "old-d", Title: "closed", Type: "task", Status: "closed", Priority: 4, ClosedAt: "2026-02-01T00:00:00Z", CloseReason: "done", CreatedAt: "2026-01-01T00:00:00Z", Tags: []string{"move"}},
		{ID: "old-stay", Title: "unselected", Type: "task", Status: "open", CreatedAt: "2026-01-01T00:00:00Z", Tags: []string{"other"}},
	}
	assert.Require(t, assert.NoErr(t, source.WriteIssues(issues)))
	assert.Require(t, assert.NoErr(t, source.UpdateMemories(func([]Memory) ([]Memory, error) {
		return []Memory{{Key: "move-key", Content: "preserve content", Tags: []string{"move"}, UpdatedAt: "2026-02-01T00:00:00Z", By: "author"}, {Key: "stay-key", Content: "other", Tags: []string{"other"}, UpdatedAt: "2026-02-02T00:00:00Z", By: "author"}}, nil
	})))
	// A newer source may contain fields unknown to this binary. Keep the whole
	// object rather than re-marshalling the current Issue type and losing them.
	issuePath := filepath.Join(source.Dir, "issues.jsonl")
	raw, err := os.ReadFile(issuePath)
	assert.Require(t, assert.NoErr(t, err))
	raw = bytes.Replace(raw, []byte(`"id":"old-a"`), []byte(`"future_field":{"value":7},"id":"old-a"`), 1)
	assert.Require(t, assert.NoErr(t, os.WriteFile(issuePath, raw, 0o644)))
	return source.Dir, sourceRoot
}

func completeTransferSelection() TransferSelection {
	return TransferSelection{IssueIDs: []string{"old-a", "old-b", "old-c", "old-d"}, MemoryKeys: []string{"move-key"}}
}

func transferTargetRepo(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "target repo")
	cmd := exec.Command("git", "init", root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init fixture: %v: %s", err, out)
	}
	return root
}

func TestTransferCandidatesAndBoundaries(t *testing.T) {
	source, _ := transferFixture(t)
	candidates, err := FindTransferCandidates(source, "move", "")
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, []string{"old-a", "old-b", "old-c", "old-d"}, candidates.IssueIDs)
	assert.Eq(t, []string{"move-key"}, candidates.MemoryKeys)
	bundle, err := PrepareTransfer(source, TransferSelection{IssueIDs: []string{"old-a"}}, "gofer", "gofer")
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, 2, len(bundle.Boundaries)) // child and reverse dependency
	target := transferTargetRepo(t)
	_, err = ImportTransfer(source, target, bundle)
	assert.Require(t, assert.Err(t, err))
	assert.Eq(t, true, strings.Contains(err.Error(), "unresolved issue references"))
	_, statErr := os.Stat(filepath.Join(target, ".gofer", "tracker"))
	assert.Eq(t, true, os.IsNotExist(statErr))

	// A reference to an absent source issue is also a visible hard boundary.
	store := NewStore(source)
	assert.Require(t, assert.NoErr(t, store.UpdateIssues(func(items []Issue) ([]Issue, error) {
		for i := range items {
			if items[i].ID == "old-c" {
				items[i].Deps = append(items[i].Deps, Dep{ID: "missing", Type: "blocks"})
			}
		}
		return items, nil
	})))
	bundle, err = PrepareTransfer(source, completeTransferSelection(), "gofer", "gofer")
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, 1, len(bundle.Boundaries))
	assert.Eq(t, "referenced issue missing from source", bundle.Boundaries[0].Reason)
}

func TestTransferPublishPreservesObjectsAndIsIdempotent(t *testing.T) {
	source, sourceRoot := transferFixture(t)
	before, err := sourceFileHashes(source)
	assert.Require(t, assert.NoErr(t, err))
	bundle, err := PrepareTransfer(source, completeTransferSelection(), "gofer", "gofer")
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, 0, len(bundle.Boundaries))
	bundlePath := filepath.Join(t.TempDir(), "bundle.json")
	assert.Require(t, assert.NoErr(t, WriteTransferBundle(source, bundlePath, bundle)))
	bundle, err = ReadTransferBundle(bundlePath)
	assert.Require(t, assert.NoErr(t, err))
	targetRoot := transferTargetRepo(t)
	assert.Require(t, assert.NoErr(t, os.MkdirAll(filepath.Join(targetRoot, "nested"), 0o755)))
	published, err := ImportTransfer(source, targetRoot, bundle)
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, true, published)
	target, err := Discover(filepath.Join(targetRoot, "nested"), "")
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, filepath.Join(targetRoot, ".gofer", "tracker"), target.Dir)
	prime, err := target.Prime()
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, true, strings.Contains(prime, "old-a"))
	status, err := target.Status()
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, "gofer", status.ProjectKey)
	assert.Eq(t, 1, status.Memories)
	cfg, err := target.ReadConfig()
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, "gofer", cfg.Prefix)
	assert.Eq(t, "gofer", cfg.ProjectKey)
	assert.Eq(t, bundle.TargetTrackerID, cfg.TrackerID)
	assert.Eq(t, false, cfg.AutoSync)
	assert.Eq(t, false, cfg.TrackerID == bundle.SourceTrackerID)
	issues, err := target.ReadIssues()
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, 4, len(issues))
	assert.Eq(t, "old-a", issues[0].ID)
	assert.Eq(t, "description", issues[0].Description)
	assert.Eq(t, "note", issues[0].Notes[0].Text)
	assert.Eq(t, "comment", issues[0].Comments[0].Text)
	assert.Eq(t, "EXT-1", issues[0].ExternalRef)
	assert.Eq(t, "closed", issues[3].Status)
	memories, err := target.ReadMemories()
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, 1, len(memories))
	assert.Eq(t, "move-key", memories[0].Key)
	raw, err := os.ReadFile(filepath.Join(target.Dir, "issues.jsonl"))
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, true, bytes.Contains(raw, []byte(`"future_field":{"value":7}`)))
	_, statErr := os.Stat(filepath.Join(target.Dir, ".local"))
	assert.Eq(t, true, os.IsNotExist(statErr))
	after, err := sourceFileHashes(source)
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, before, after)
	assert.Eq(t, false, sourceRoot == targetRoot)
	more, err := ImportTransfer(source, targetRoot, bundle)
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, false, more)
	different, err := PrepareTransfer(source, completeTransferSelection(), "gofer", "gofer")
	assert.Require(t, assert.NoErr(t, err))
	_, err = ImportTransfer(source, targetRoot, different)
	assert.Require(t, assert.Err(t, err))
	assert.Eq(t, true, strings.Contains(err.Error(), "conflicts with transfer bundle"))
	assert.Require(t, assert.NoErr(t, os.WriteFile(filepath.Join(target.Dir, "issues.jsonl"), []byte("{}\n"), 0o644)))
	_, err = ImportTransfer(source, targetRoot, bundle)
	assert.Require(t, assert.Err(t, err))
	assert.Eq(t, true, strings.Contains(err.Error(), "changed after transfer"))
}

func TestTransferSourceChangeAndBundleTamperBlockImport(t *testing.T) {
	source, _ := transferFixture(t)
	bundle, err := PrepareTransfer(source, completeTransferSelection(), "gofer", "gofer")
	assert.Require(t, assert.NoErr(t, err))
	target := transferTargetRepo(t)
	// Simulate a writer landing a new issue after export. No target tracker may
	// appear from a stale package, even though selected objects are unchanged.
	store := NewStore(source)
	assert.Require(t, assert.NoErr(t, store.UpdateIssues(func(items []Issue) ([]Issue, error) {
		return append(items, Issue{ID: "old-new", Title: "concurrent", Status: "open", CreatedAt: "2026-03-01T00:00:00Z"}), nil
	})))
	_, err = ImportTransfer(source, target, bundle)
	assert.Require(t, assert.Err(t, err))
	assert.Eq(t, true, strings.Contains(err.Error(), "changed since export"))
	_, statErr := os.Stat(filepath.Join(target, ".gofer", "tracker"))
	assert.Eq(t, true, os.IsNotExist(statErr))

	bundle.Issues[0] = json.RawMessage(`{"id":"forged"}`)
	_, err = ImportTransfer(source, target, bundle)
	assert.Require(t, assert.Err(t, err))
	assert.Eq(t, true, strings.Contains(err.Error(), "digest mismatch"))
}

func TestTransferBundleOutputNeverOverwritesSource(t *testing.T) {
	source, _ := transferFixture(t)
	bundle, err := PrepareTransfer(source, completeTransferSelection(), "gofer", "gofer")
	assert.Require(t, assert.NoErr(t, err))
	before, err := sourceFileHashes(source)
	assert.Require(t, assert.NoErr(t, err))
	for _, name := range []string{"config.yaml", "issues.jsonl", "memories.jsonl", "bundle.json"} {
		err := WriteTransferBundle(source, filepath.Join(source, name), bundle)
		assert.Require(t, assert.Err(t, err), name)
	}
	alias := filepath.Join(t.TempDir(), "source-alias")
	if err := os.Symlink(source, alias); err == nil {
		err = WriteTransferBundle(source, filepath.Join(alias, "bundle.json"), bundle)
		assert.Require(t, assert.Err(t, err))
	} else {
		t.Logf("directory symlink unavailable: %v", err)
	}
	after, err := sourceFileHashes(source)
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, before, after)
}

func TestTransferDanglingTargetLinkIsConflict(t *testing.T) {
	source, _ := transferFixture(t)
	bundle, err := PrepareTransfer(source, completeTransferSelection(), "gofer", "gofer")
	assert.Require(t, assert.NoErr(t, err))
	targetRoot := transferTargetRepo(t)
	parent := filepath.Join(targetRoot, ".gofer")
	assert.Require(t, assert.NoErr(t, os.MkdirAll(parent, 0o755)))
	link := filepath.Join(parent, "tracker")
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), link); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}
	_, err = ImportTransfer(source, targetRoot, bundle)
	assert.Require(t, assert.Err(t, err))
	assert.Eq(t, true, strings.Contains(err.Error(), "already exists"))
	info, err := os.Lstat(link)
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, true, info.Mode()&os.ModeSymlink != 0)
}
