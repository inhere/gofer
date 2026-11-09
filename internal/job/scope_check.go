package job

import (
	"context"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/inhere/gofer/internal/util"
)

// Declared-scope checks for the review (gofer-3nxa.3). The web console carries the same
// rules in web/src/utils/scope.ts — keep the two in step (their tests share cases).

// DiffFiles lists the files a unified diff (a job's changes.diff, worktree sections
// included) touches, in first-seen order without duplicates. A rename contributes its
// new path; a deleted file its old one. A diff truncated at the size cap ends with a
// "=== changed files ===" name list (one path per line), which is read too.
func DiffFiles(diff string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	inList := false // inside the trailing "=== changed files ===" list (truncated diffs)
	for _, line := range strings.Split(diff, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "=== ") {
			inList = line == changedFilesHeader
			continue
		}
		if inList {
			add(line)
			continue
		}
		if !strings.HasPrefix(line, "diff --git ") {
			continue
		}
		rest := strings.TrimPrefix(line, "diff --git ")
		// "a/<old> b/<new>": split on the LAST " b/" so a path with spaces survives.
		if i := strings.LastIndex(rest, " b/"); i >= 0 {
			add(rest[i+3:])
			continue
		}
		add(strings.TrimPrefix(rest, "a/"))
	}
	return out
}

// OutOfScope returns the files matching none of the scope globs (nil when scope is
// empty: no declaration, nothing is out of scope). See ScopeMatch for the glob rules.
func OutOfScope(files, scope []string) []string {
	globs := cleanScope(scope)
	if len(globs) == 0 {
		return nil
	}
	var out []string
	for _, f := range files {
		in := false
		for _, g := range globs {
			if ScopeMatch(g, f) {
				in = true
				break
			}
		}
		if !in {
			out = append(out, f)
		}
	}
	return out
}

// ScopeMatch reports whether file (a repo-root-relative slash path) is covered by glob:
// `**` spans any number of path segments, `*` and `?` stay within one segment, and a
// glob without wildcards also covers everything under it as a directory (`internal/job`
// = `internal/job/**`). A leading `./` and a trailing `/` are ignored.
func ScopeMatch(glob, file string) bool {
	glob = normScopeGlob(glob)
	file = strings.TrimPrefix(file, "./")
	if glob == "" {
		return false
	}
	if !strings.ContainsAny(glob, "*?") {
		return file == glob || strings.HasPrefix(file, glob+"/")
	}
	re, err := regexp.Compile(globRegexp(glob))
	return err == nil && re.MatchString(file)
}

// normScopeGlob is the glob normalisation shared by ScopeMatch and ScopeLockPaths:
// surrounding whitespace, a leading `./` and a trailing `/` are dropped.
func normScopeGlob(glob string) string {
	return strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(glob), "./"), "/")
}

// ScopeLockPaths derives the directory-lock paths a declared scope needs (gofer-341t):
// per glob, the repo-relative slash path before its first wildcard segment (a glob
// without wildcards locks the path itself, file or directory, no stat). Pure string work.
// prefixes are de-duplicated, entries contained in another entry dropped (no common
// ancestor is taken) and sorted. A non-empty reason means "do not derive": invalid_glob
// (absolute or `..` path) or empty_prefix (some glob has no fixed prefix, e.g. `**/*.go`,
// so only the whole repo covers it). Empty scope yields (nil, "").
func ScopeLockPaths(scope []string) (prefixes []string, reason string) {
	var all []string
	for _, g := range cleanScope(scope) {
		g = normScopeGlob(g)
		if g == "" {
			continue
		}
		if strings.HasPrefix(g, "/") || strings.Contains(g, "\\") || (len(g) > 1 && g[1] == ':') {
			return nil, "invalid_glob"
		}
		var fixed []string
		for _, seg := range strings.Split(g, "/") {
			if seg == ".." {
				return nil, "invalid_glob"
			}
			if strings.ContainsAny(seg, "*?") {
				break // the first wildcard segment and everything after it is not fixed
			}
			if seg != "" && seg != "." {
				fixed = append(fixed, seg)
			}
		}
		if len(fixed) == 0 {
			return nil, "empty_prefix"
		}
		all = append(all, strings.Join(fixed, "/"))
	}
	sort.Strings(all)
	for _, p := range all {
		covered := false
		for _, kept := range prefixes {
			if dirContains(kept, p) { // duplicate, or inside a shorter kept prefix
				covered = true
				break
			}
		}
		if !covered {
			prefixes = append(prefixes, p)
		}
	}
	return prefixes, ""
}

// globRegexp translates a scope glob into an anchored regular expression.
func globRegexp(glob string) string {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		switch {
		case c == '*' && i+1 < len(glob) && glob[i+1] == '*':
			i++
			if i+1 < len(glob) && glob[i+1] == '/' {
				i++
				b.WriteString("(?:.*/)?") // `**/` = zero or more whole segments
			} else {
				b.WriteString(".*")
			}
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return b.String()
}

// scopeLockFallback is why a --scope did not narrow the directory lock (R4): the lock
// stays on the working directory and the job records job.lock_scope_fallback.
type scopeLockFallback struct {
	Reason string
	Scope  []string
}

// scopeLockPathsForSubmit turns a declared scope into project-root-relative lock paths:
// ScopeLockPaths gives repo-relative prefixes (scope globs are repo-root relative, like
// the review's scope check), which are re-based from the repo top onto the project root.
// A non-empty reason means "do not derive": invalid_glob / empty_prefix from the pure
// derivation, not_git when workDir is no git checkout, outside_project when a prefix
// lies outside the project root (a repo bigger than the project).
func scopeLockPathsForSubmit(ctx context.Context, projectRoot, workDir string, scope []string) ([]string, string) {
	prefixes, reason := ScopeLockPaths(scope)
	if reason != "" || len(prefixes) == 0 {
		return nil, reason
	}
	top, err := gitTopLevel(ctx, workDir)
	if err != nil {
		return nil, "not_git"
	}
	top, root := util.RealPath(top), util.RealPath(projectRoot)
	out := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		rel, relErr := filepath.Rel(root, filepath.Join(top, filepath.FromSlash(p)))
		if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, "outside_project"
		}
		out = append(out, filepath.ToSlash(rel))
	}
	return out, ""
}
