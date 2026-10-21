package job

import (
	"regexp"
	"sort"
	"strings"
)

// Review hints (gofer-hjj1): the parts of a job's review material a supervisor
// otherwise digs out by hand — which files lost the most lines, which comments
// disappeared without reappearing, and which verification result lines the agent's
// report ends with. All three are best-effort hints over data the review screen
// already has (the captured changes.diff and the stdout tail); they never block
// accept/reject.
const (
	// reviewHotspotLimit caps deletion_hotspots: enough to start reviewing at the
	// top, few enough to stay on one screen.
	reviewHotspotLimit = 5
	// reviewHotspotMinDeleted is the deletion count below which a file is ordinary
	// churn rather than a hotspot ("only list files with 10+ deleted lines").
	reviewHotspotMinDeleted = 10
	// reviewRemovedCommentLimit caps removed_comments lines.
	reviewRemovedCommentLimit = 20
	// reviewVerifyLineLimit caps verify_lines lines.
	reviewVerifyLineLimit = 30
)

// ReviewHints is the review material computed from a job's diff and report
// (gofer-hjj1). Empty sections are omitted from JSON, and the review screen prints
// nothing for them.
type ReviewHints struct {
	DeletionHotspots []DeletionHotspot `json:"deletion_hotspots,omitempty"`
	RemovedComments  []RemovedComment  `json:"removed_comments,omitempty"`
	VerifyLines      []string          `json:"verify_lines,omitempty"`
}

// DeletionHotspot is one file's churn in the job's diff: Deleted lines is what the
// list is sorted by.
type DeletionHotspot struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
}

// RemovedComment is a comment line the diff deletes without re-adding the same text
// in the same file: Line is the old file's line number, Text the trimmed content.
type RemovedComment struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// BuildReviewHints derives the three sections from a captured changes.diff and the
// agent's final report (stdout tail). Both may be empty: the result then has no
// sections at all rather than an error — a job without a diff is not a failed review.
func BuildReviewHints(diff, report string) ReviewHints {
	files := scanDiff(diff)
	return ReviewHints{
		DeletionHotspots: deletionHotspots(files),
		RemovedComments:  removedComments(files),
		VerifyLines:      verifyLines(report),
	}
}

// diffFileScan is one file's slice of a unified diff: its churn counts, the added
// lines (to tell a moved comment from a deleted one) and the comment lines it drops.
type diffFileScan struct {
	path    string
	added   int
	deleted int
	// plusText holds the trimmed text of every added line, per file: a removed
	// comment whose text is still added somewhere in the same file only moved.
	plusText map[string]bool
	removed  []RemovedComment
}

// scanDiff walks a unified diff (a job's changes.diff, worktree sections included)
// and per-file counts additions/deletions and collects removed comment lines with
// their old-file line numbers. The trailing "=== changed files ===" list of a
// size-capped diff carries no churn and is ignored.
func scanDiff(diff string) []*diffFileScan {
	var out []*diffFileScan
	var cur *diffFileScan
	oldLine, newLine := 0, 0
	inHeader := false // just after "diff --git", before the first hunk
	for _, raw := range strings.Split(diff, "\n") {
		line := strings.TrimRight(raw, "\r")
		switch {
		case strings.HasPrefix(line, "=== "):
			cur = nil // section marker (committed/uncommitted/changed files): not a patch
		case strings.HasPrefix(line, "diff --git "):
			cur = &diffFileScan{path: diffHeaderPath(line), plusText: map[string]bool{}}
			out = append(out, cur)
			oldLine, newLine = 0, 0
			inHeader = true
		case cur == nil:
			continue
		case inHeader && strings.HasPrefix(line, "--- "):
			// The old-side file header, not a removed line.
			continue
		case inHeader && strings.HasPrefix(line, "+++ "):
			// The new-side file header; the hunk (or the next file) follows.
			inHeader = false
			continue
		case strings.HasPrefix(line, "@@"):
			if a, b, ok := hunkStart(line); ok {
				oldLine, newLine = a, b
			}
			inHeader = false
		case line == "":
			continue
		case line[0] == '+':
			cur.added++
			cur.plusText[strings.TrimSpace(line[1:])] = true
			newLine++
		case line[0] == '-':
			cur.deleted++
			if text, ok := commentText(strings.TrimSpace(line[1:])); ok {
				cur.removed = append(cur.removed, RemovedComment{Path: cur.path, Line: oldLine, Text: text})
			}
			oldLine++
		case line[0] == ' ':
			oldLine++
			newLine++
		}
	}
	return out
}

// diffHeaderPath returns the new path of a "diff --git a/<old> b/<new>" line, using
// the same split DiffFiles uses so a path with spaces survives.
func diffHeaderPath(line string) string {
	rest := strings.TrimPrefix(line, "diff --git ")
	if i := strings.LastIndex(rest, " b/"); i >= 0 {
		return rest[i+3:]
	}
	return strings.TrimPrefix(rest, "a/")
}

// hunkStart reads the old/new start lines of a unified-diff hunk header
// ("@@ -12,3 +14,5 @@ context").
func hunkStart(line string) (oldStart, newStart int, ok bool) {
	fields := strings.Fields(line)
	for i, f := range fields {
		if !strings.HasPrefix(f, "-") || i+1 >= len(fields) || !strings.HasPrefix(fields[i+1], "+") {
			continue
		}
		return parseHunkStart(f[1:]), parseHunkStart(strings.TrimPrefix(fields[i+1], "+")), true
	}
	return 0, 0, false
}

// parseHunkStart parses "12" or "12,3" (the count is not needed).
func parseHunkStart(s string) int {
	if i := strings.IndexByte(s, ','); i >= 0 {
		s = s[:i]
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return n
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// commentText reports whether a diff body line (sans +/- sigil) is a comment line
// and returns its trimmed text. The recognised forms are the ones the review
// question is about — line comments (`//`, Python/shell/YAML `#`), block comments
// (`/* … */`, `<!-- … -->`, their `*` continuation) and therefore Go doc comments.
func commentText(s string) (string, bool) {
	switch {
	case s == "":
		return "", false
	case strings.HasPrefix(s, "//"), strings.HasPrefix(s, "#"),
		strings.HasPrefix(s, "/*"), strings.HasPrefix(s, "*/"),
		strings.HasPrefix(s, "<!--"):
		return s, true
	case s == "*", strings.HasPrefix(s, "* "):
		return s, true
	}
	return "", false
}

// deletionHotspots ranks files by deleted lines (ties: more added lines first, then
// path) and keeps the top reviewHotspotLimit with at least reviewHotspotMinDeleted
// deletions.
func deletionHotspots(files []*diffFileScan) []DeletionHotspot {
	var out []DeletionHotspot
	for _, f := range files {
		if f.deleted < reviewHotspotMinDeleted {
			continue
		}
		out = append(out, DeletionHotspot{Path: f.path, Added: f.added, Deleted: f.deleted})
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Deleted != b.Deleted {
			return a.Deleted > b.Deleted
		}
		if a.Added != b.Added {
			return a.Added > b.Added
		}
		return a.Path < b.Path
	})
	if len(out) > reviewHotspotLimit {
		out = out[:reviewHotspotLimit]
	}
	return out
}

// removedComments keeps the comment lines that no added line of the same file
// reintroduces (a moved comment is not a deleted one), in diff order, capped at
// reviewRemovedCommentLimit.
func removedComments(files []*diffFileScan) []RemovedComment {
	var out []RemovedComment
	for _, f := range files {
		for _, rc := range f.removed {
			if f.plusText[rc.Text] {
				continue
			}
			out = append(out, rc)
			if len(out) >= reviewRemovedCommentLimit {
				return out
			}
		}
	}
	return out
}

// verifyLinePatterns are the verification result shapes a report carries: `go test`
// (ok / FAIL / --- FAIL: / PASS), captured exit codes (`*_EXIT=n`, `exit=n`,
// `tsc=n`) and the vitest summary lines (Test Files / Tests … passed).
var verifyLinePatterns = []*regexp.Regexp{
	regexp.MustCompile(`^ok\s+\S`),
	regexp.MustCompile(`^(FAIL|PASS)\b`),
	regexp.MustCompile(`^--- FAIL:`),
	regexp.MustCompile(`\b[A-Z][A-Z0-9_]*_EXIT=\d+`),
	regexp.MustCompile(`\bexit=\d+`),
	regexp.MustCompile(`\bTest Files\b.*\bpassed\b`),
	regexp.MustCompile(`\bTests\b.*\bpassed\b`),
	regexp.MustCompile(`\btsc=\d+`),
}

// verifyLines extracts the verification result lines from a report, in the order the
// report states them, without repeated identical lines, capped at
// reviewVerifyLineLimit.
func verifyLines(report string) []string {
	var out []string
	seen := map[string]bool{}
	for _, raw := range strings.Split(report, "\n") {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		if !isVerifyLine(trimmed) {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
		if len(out) >= reviewVerifyLineLimit {
			break
		}
	}
	return out
}

// isVerifyLine reports whether a trimmed report line is a verification result.
func isVerifyLine(s string) bool {
	for _, re := range verifyLinePatterns {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}
