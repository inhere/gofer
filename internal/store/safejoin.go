package store

import (
	"errors"
	"path"
	"path/filepath"
	"strings"
)

// SafeJoinUnder joins a request-supplied relative name under base and guarantees
// the result stays inside base (design §9 — the artifact download path check):
//   - empty / absolute names are rejected outright;
//   - the name is anchored to root and Cleaned ("/"+name) so any "../" is folded
//     away before joining (a "../etc" can never climb above base);
//   - a Rel check rejects any path that still escapes (defence in depth);
//   - EvalSymlinks resolves the real target and re-checks containment, so a
//     symlink inside artifacts/ pointing outside is rejected (symlink escape).
//
// It mirrors the口径 of project.SafeJoin (internal/project/path.go). Moved here
// from httpapi (was safeJoinUnder) so the path-safety primitive lives with the
// store layer and is unit-tested independently of the HTTP handler.
func SafeJoinUnder(base, name string) (string, error) {
	if name == "" || filepath.IsAbs(name) {
		return "", errors.New("artifact name must be a non-empty relative path")
	}
	// Normalize backslashes so a Windows-style "sub\\b" is treated as a path
	// separator on Linux too.
	normalized := strings.ReplaceAll(name, "\\", "/")
	if normalized == "" || strings.HasPrefix(normalized, "/") {
		return "", errors.New("artifact name must be a non-empty relative path")
	}

	// Reject traversal that escapes the dir explicitly (clearer than silently
	// folding "../foo" into "foo"): a name whose Cleaned form is ".." or begins
	// with "../" climbs above base. Internal "x/../y" Cleans to "y" → allowed.
	cleaned := path.Clean(normalized)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", errors.New("artifact name escapes artifacts dir")
	}

	// Anchor to root + Clean to strip any residual "..", then Rel-check that the
	// join stays inside base (defence in depth on top of the segment check).
	full := filepath.Join(base, filepath.FromSlash(cleaned))
	rel, err := filepath.Rel(base, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("artifact name escapes artifacts dir")
	}

	// Symlink escape: if the (existing) target resolves outside base, reject.
	// A non-existent path (EvalSymlinks errors) is handled by the caller's Stat
	// (404), so a missing file is not a 400 here.
	if real, err := filepath.EvalSymlinks(full); err == nil {
		realBase, errB := filepath.EvalSymlinks(base)
		if errB != nil {
			realBase = base // base should exist; fall back to the lexical base.
		}
		r2, err2 := filepath.Rel(realBase, real)
		if err2 != nil || r2 == ".." || strings.HasPrefix(r2, ".."+string(filepath.Separator)) {
			return "", errors.New("artifact name escapes artifacts dir (symlink)")
		}
	}
	return full, nil
}
