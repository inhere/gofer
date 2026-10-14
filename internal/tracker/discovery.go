package tracker

import (
	"fmt"
	"os"
	"path/filepath"
)

func Discover(start, explicit string) (*Store, error) {
	return discoverUpTo(start, explicit, "")
}

// DiscoverWithinRepo is Discover restricted to the git repository start belongs to:
// the upward search stops at (and includes) that repository's top directory, so a
// nested checkout — its own `.git`, no `.gofer/tracker` of its own — finds nothing
// instead of the OUTER workspace's tracker, whose rules and memories describe another
// subsystem entirely (gofer-krzy). A start that no ancestor of which is a repository
// has no boundary to stop at and keeps Discover's behaviour, and an explicit tracker
// directory is always used as given.
func DiscoverWithinRepo(start, explicit string) (*Store, error) {
	boundary, ok := RepoBoundary(start)
	if !ok {
		return discoverUpTo(start, explicit, "")
	}
	return discoverUpTo(start, explicit, boundary)
}

// RepoBoundary returns the top directory of the git repository start lives in: the
// nearest ancestor (start included) holding a `.git` entry — a directory in a normal
// checkout, a file in a linked worktree or a submodule. ok=false when no ancestor is
// a repository.
func RepoBoundary(start string) (string, bool) {
	cur, err := filepath.Abs(start)
	if err != nil {
		return "", false
	}
	for {
		if _, err := os.Stat(filepath.Join(cur, ".git")); err == nil {
			return cur, true
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", false
		}
		cur = parent
	}
}

// discoverUpTo walks start and its ancestors looking for `<dir>/.gofer/tracker`.
// boundary, when non-empty, is the LAST ancestor it may look at (the top of the git
// repository start belongs to).
func discoverUpTo(start, explicit, boundary string) (*Store, error) {
	if explicit != "" {
		return existingStore(explicit)
	}
	cur, err := filepath.Abs(start)
	if err != nil {
		return nil, err
	}
	for {
		path := filepath.Join(cur, ".gofer", "tracker")
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			return NewStore(path), nil
		}
		parent := filepath.Dir(cur)
		if parent == cur || (boundary != "" && cur == boundary) {
			return nil, fmt.Errorf("no tracker found; run gofer repo init")
		}
		cur = parent
	}
}

func existingStore(dir string) (*Store, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("tracker %s not found; run gofer repo init", abs)
	}
	return NewStore(abs), nil
}
