// Filesystem path helpers.
//
// gofer regularly compares a path it was HANDED against a path a tool REPORTED:
// a caller's cwd against `git rev-parse --show-toplevel`, a project host path
// against a worker root. Those two names can differ without being different
// directories, because macOS puts /tmp and /var behind /private symlinks (and
// /tmp itself is a symlink on many Linux boxes). Comparing the raw strings then
// rejects a perfectly valid path as "outside the checkout". RealPath resolves
// the symlinks before the comparison.

package util

import (
	"path/filepath"
)

// RealPath resolves symlinks on the existing prefix of p and re-appends the
// non-existent tail. When nothing resolves it falls back to the lexical clean, so
// pure-logical (non-existent) paths map to themselves.
func RealPath(p string) string {
	p = filepath.Clean(p)
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	parent := filepath.Dir(p)
	if parent == p { // reached root; nothing left to resolve.
		return p
	}
	return filepath.Join(RealPath(parent), filepath.Base(p))
}
