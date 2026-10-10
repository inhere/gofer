package job

import (
	"regexp"
	"strings"
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
	glob = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(glob), "./"), "/")
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
