package tracker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// TestDiscoverWithinRepoStopsAtRepoBoundary (gofer-krzy): the search for a tracker
// must not leave the git repository the start directory belongs to. A nested,
// independent checkout (its own `.git`, no tracker of its own) used to be fed the
// OUTER workspace's tracker — rules and memories about a different subsystem.
func TestDiscoverWithinRepoStopsAtRepoBoundary(t *testing.T) {
	outer := t.TempDir()
	outerTracker := filepath.Join(outer, ".gofer", "tracker")
	_, _, err := Init(outer, "outer", true)
	assert.Require(t, assert.NoErr(t, err))
	// The workspace root is a repository, and the nested service is another one.
	assert.Require(t, assert.NoErr(t, os.MkdirAll(filepath.Join(outer, ".git"), 0o755)))
	nested := filepath.Join(outer, "svc", "app")
	assert.Require(t, assert.NoErr(t, os.MkdirAll(filepath.Join(nested, ".git"), 0o755)))

	// Nested repo without a tracker of its own: nothing from the outer workspace.
	_, err = DiscoverWithinRepo(nested, "")
	assert.ErrMsgContains(t, err, "no tracker found")
	// tracker_prime: nearest keeps the historical walk across the boundary.
	got, err := Discover(nested, "")
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, outerTracker, got.Dir)

	// A plain subdirectory of the outer repository is the ordinary case: unchanged.
	got, err = DiscoverWithinRepo(filepath.Join(outer, "svc"), "")
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, outerTracker, got.Dir)

	// The boundary itself is included in the search, and a tracker of the nested
	// repository wins over the outer one.
	inner, _, err := Init(nested, "inner", true)
	assert.Require(t, assert.NoErr(t, err))
	got, err = DiscoverWithinRepo(nested, "")
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, inner.Dir, got.Dir)

	// An explicit tracker directory is taken as given, boundary or not.
	got, err = DiscoverWithinRepo(nested, outerTracker)
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, outerTracker, got.Dir)

	// RepoBoundary reports the top of the repository a path lives in.
	top, ok := RepoBoundary(nested)
	assert.True(t, ok)
	assert.Eq(t, nested, top)
	top, ok = RepoBoundary(filepath.Join(outer, "svc"))
	assert.True(t, ok)
	assert.Eq(t, outer, top)
}
