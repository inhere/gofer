package config

import (
	"path"
	"path/filepath"
	"strings"
)

// MapRoot resolves a server-side logical path to this worker's host path using
// the longest boundary-aligned roots prefix (design §10-Q1, P3 T2-B). It returns
// (host, true) only when some root's From is a boundary-aligned prefix of logical
// (longest wins) AND the mapping does not escape that root on either side.
//
// Normalization: backslashes -> '/', trailing slashes trimmed. Windows-style
// (drive-letter) From prefixes match case-insensitively (D:/work == d:/work);
// POSIX From prefixes match case-sensitively.
//
// Boundary alignment: /a/b must NOT match /a/bc — only an exact match or a '/'
// at logical[len(from)] counts.
//
// Containment (B6) is enforced HERE, not delegated to project.SafeJoin (which is
// purely lexical Abs/Clean/Rel and does NOT resolve symlinks):
//   - roots carrying a ".." segment are misconfigured -> reject;
//   - logical side: after "..", the logical path must still fall under From;
//   - host side: the joined host must resolve under To both lexically AND through
//     symlinks (EvalSymlinks on the existing prefix, non-existent tail re-appended).
//
// On any miss or containment violation it returns ("", false); it NEVER returns
// an empty host as "success".
func (wc *WorkerConfig) MapRoot(logical string) (host string, ok bool) {
	norm := normalizeLogicalPath(logical)

	// 1. longest boundary-aligned From prefix wins.
	bestIdx, bestLen := -1, -1
	var bestCI bool
	for i := range wc.Roots {
		from := normalizeLogicalPath(wc.Roots[i].From)
		if from == "" {
			continue
		}
		ci := isWindowsLogicalPath(from)
		if logicalHasPrefix(norm, from, ci) && len(from) > bestLen {
			bestIdx, bestLen, bestCI = i, len(from), ci
		}
	}
	if bestIdx < 0 {
		return "", false
	}

	from := normalizeLogicalPath(wc.Roots[bestIdx].From)
	to := normalizeLogicalPath(wc.Roots[bestIdx].To)

	// 2. containment (B6). Deleting this block regresses to the unsafe
	//    "join-then-Clean" behaviour that acceptance 23 falsifies.
	// 2a. a root with a ".." segment is a misconfiguration -> reject.
	if hasDotDotSegment(from) || hasDotDotSegment(to) {
		return "", false
	}
	// 2b. logical side: cleaned logical must still fall under From once ".."
	//     resolves (e.g. /root/../outside escapes /root).
	cleanLog := path.Clean(norm)
	cleanFrom := path.Clean(from)
	if !logicalHasPrefix(cleanLog, cleanFrom, bestCI) {
		return "", false
	}

	// 3. host = To + <suffix after From>, then Clean. byte length of cleanFrom is
	//    case-independent, so the suffix is correct even under CI matching.
	rem := cleanLog[len(cleanFrom):] // "" or "/..."
	cleanTo := filepath.Clean(to)
	cleanHost := filepath.Clean(cleanTo + rem)

	// 3a. host side lexical containment.
	if escapesDir(cleanTo, cleanHost) {
		return "", false
	}
	// 3b. host side symlink containment: real host must sit under real To.
	if escapesDir(realPathBestEffort(cleanTo), realPathBestEffort(cleanHost)) {
		return "", false
	}

	return cleanHost, true
}

// escapesDir reports whether child is NOT contained within parent, using host
// filepath semantics (separators / case). A ".."-leading or absolute relative
// path, or a Rel error, means the child escaped.
func escapesDir(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return true
	}
	return rel == ".." ||
		strings.HasPrefix(rel, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(rel)
}

// normalizeLogicalPath turns backslashes into '/' and trims trailing slashes
// (keeping a lone "/" root). It does NOT resolve "." / ".." — that is the
// containment check's job.
func normalizeLogicalPath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	for len(p) > 1 && strings.HasSuffix(p, "/") {
		p = p[:len(p)-1]
	}
	return p
}

// isWindowsLogicalPath reports whether p starts with a drive-letter prefix (X:).
// Such prefixes are matched case-insensitively.
func isWindowsLogicalPath(p string) bool {
	return len(p) >= 2 && p[1] == ':' &&
		((p[0] >= 'a' && p[0] <= 'z') || (p[0] >= 'A' && p[0] <= 'Z'))
}

// logicalHasPrefix reports whether normalized p has normalized prefix with a
// segment boundary (exact match or a '/' right after prefix). ci folds case for
// Windows-style prefixes.
func logicalHasPrefix(p, prefix string, ci bool) bool {
	if prefix == "/" { // filesystem-root prefix: any absolute path is under it.
		return strings.HasPrefix(p, "/")
	}
	if len(p) < len(prefix) {
		return false
	}
	head := p[:len(prefix)]
	if ci {
		if !strings.EqualFold(head, prefix) {
			return false
		}
	} else if head != prefix {
		return false
	}
	return len(p) == len(prefix) || p[len(prefix)] == '/'
}

// hasDotDotSegment reports whether the normalized path contains a ".." segment.
func hasDotDotSegment(p string) bool {
	for _, seg := range strings.Split(normalizeLogicalPath(p), "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

// realPathBestEffort resolves symlinks on the existing prefix of p and
// re-appends the non-existent tail. When nothing resolves it falls back to the
// lexical clean, so pure-logical (non-existent) paths map to themselves.
func realPathBestEffort(p string) string {
	p = filepath.Clean(p)
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	parent := filepath.Dir(p)
	if parent == p { // reached root; nothing left to resolve.
		return p
	}
	return filepath.Join(realPathBestEffort(parent), filepath.Base(p))
}
