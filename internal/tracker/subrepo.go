package tracker

import (
	"strings"
)

// SubrepoScope returns the `when.paths` entry of a memory written from cwd: the path
// of the nested repository cwd lives in, relative to this tracker's repository root,
// as "<subrepo>/**". ok=false when cwd is not inside this repository, when it belongs
// to the repository at the tracker root itself (there is nothing to narrow), or when
// it is in no repository at all — a plain subdirectory of the workspace is not a
// subproject, and only a checkout of its own is.
//
// It is the write-time half of memory scoping (gofer-opv5): one workspace root holding
// several independent checkouts (each with its own `.git`) shares a single tracker, and
// without a scope the memories of one checkout are injected into the jobs of all the
// others.
func (s *Store) SubrepoScope(cwd string) (string, bool) {
	if strings.TrimSpace(cwd) == "" {
		return "", false
	}
	rel, ok := s.CwdRel(cwd)
	if !ok || rel == "" {
		return "", false
	}
	boundary, ok := RepoBoundary(cwd)
	if !ok {
		return "", false
	}
	// "" means the boundary IS the tracker's repository root: cwd is in the repository
	// the tracker belongs to, and its memories are not a subproject's.
	boundaryRel, ok := s.CwdRel(boundary)
	if !ok || boundaryRel == "" {
		return "", false
	}
	return boundaryRel + "/**", true
}
