package commands

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/tracker"
)

// `gofer repo sync` archives the settled memories before it pushes; `memory doctor` lists
// them and `memory restore` brings one back; a rule is never touched.
func TestRepoSyncAutoArchivesSettledMemories(t *testing.T) {
	t.Setenv("GOFER_SERVER_ADDR", "")
	t.Setenv("GOFER_SERVER_TOKEN", "")
	t.Setenv("GOFER_JOB_ID", "")
	t.Setenv("GOFER_SESSION_ID", "")
	root := t.TempDir()
	trackerRunOK(t, root, "repo", "init", "--no-hooks")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	}))
	defer server.Close()
	cfg := fmt.Sprintf("server:\n  addr: %q\n  allow_empty_token: true\n", server.URL)
	assert.Require(t, assert.NoErr(t, os.WriteFile(filepath.Join(root, "test-config.yaml"), []byte(cfg), 0o600)))

	s, err := tracker.Discover(root, "")
	assert.Require(t, assert.NoErr(t, err))
	old := time.Now().Add(-60 * 24 * time.Hour).UTC().Format(time.RFC3339)
	assert.Require(t, assert.NoErr(t, s.UpdateMemories(func([]tracker.Memory) ([]tracker.Memory, error) {
		return []tracker.Memory{
			{Key: "h-old", Content: "state", UpdatedAt: old, MemoryMeta: tracker.MemoryMeta{Kind: tracker.MemoryKindHandoff}},
			{Key: "n-gone", Content: "see `docs/gone.md`", UpdatedAt: old},
			{Key: "r-gone", Content: "see `docs/gone.md`", UpdatedAt: old, MemoryMeta: tracker.MemoryMeta{Kind: tracker.MemoryKindRule}},
		}, nil
	})))

	out, _ := trackerCLI(t, root, "repo", "sync")
	assert.Contains(t, out, "auto-archived 2 memories")
	live := trackerRunOK(t, root, "memory", "ls")
	assert.Contains(t, live, "r-gone")
	assert.NotContains(t, live, "h-old")
	assert.NotContains(t, live, "n-gone")

	out = trackerRunOK(t, root, "memory", "doctor")
	assert.Contains(t, out, "auto-archived")
	assert.Contains(t, out, "h-old [handoff]")
	assert.Contains(t, out, "n-gone [note]")
	var report tracker.DoctorReport
	assert.Require(t, assert.NoErr(t, json.Unmarshal([]byte(trackerRunOK(t, root, "memory", "doctor", "--json")), &report)))
	assert.Len(t, report.AutoArchived, 2)

	// A second sync has nothing left to archive; restore brings a memory back.
	out, _ = trackerCLI(t, root, "repo", "sync")
	assert.NotContains(t, out, "auto-archived")
	trackerRunOK(t, root, "memory", "restore", "n-gone")
	assert.Contains(t, trackerRunOK(t, root, "memory", "ls"), "n-gone")

	// Switched off in the repository config: nothing is archived.
	assert.Require(t, assert.NoErr(t, s.UpdateMemories(func(items []tracker.Memory) ([]tracker.Memory, error) {
		for i := range items {
			if items[i].Key == "n-gone" {
				items[i].UpdatedAt = old
			}
		}
		return items, nil
	})))
	off := false
	assert.Require(t, assert.NoErr(t, s.UpdateConfig(func(c *tracker.Config) { c.Prime.Doctor = &tracker.PrimeDoctorConfig{AutoArchive: &off} })))
	out, _ = trackerCLI(t, root, "repo", "sync")
	assert.NotContains(t, out, "auto-archived")
	assert.Contains(t, trackerRunOK(t, root, "memory", "ls"), "n-gone")
}
