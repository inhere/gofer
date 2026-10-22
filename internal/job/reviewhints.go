package job

import (
	"fmt"
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
	// reviewReportVerifyLimit caps report_verify blocks: a report that pastes ten
	// command outputs needs its best one shown, not all ten.
	reviewReportVerifyLimit = 3
	// reviewReportVerifyLineLimit caps one report_verify block. The tail is kept:
	// a verification output ends with its verdict, its command at the top.
	reviewReportVerifyLineLimit = 60
	// reviewHotspotHeavyRatio marks a file whose deletions are at least this many
	// times its additions: a rewrite that mostly removed code, the shape a reviewer
	// wants to read first (a compressed/reordered file also looks like this).
	reviewHotspotHeavyRatio = 2
)

// VerifySourceAgent marks a report_verify block as the agent's own account of its
// verification: shown for reference, never as proof that a verify step passed.
const VerifySourceAgent = "agent"

// ReviewHints is the review material computed from a job's diff and report
// (gofer-hjj1). Empty sections are omitted from JSON, and the review screen prints
// nothing for them.
type ReviewHints struct {
	DeletionHotspots []DeletionHotspot `json:"deletion_hotspots,omitempty"`
	RemovedComments  []RemovedComment  `json:"removed_comments,omitempty"`
	VerifyLines      []string          `json:"verify_lines,omitempty"`
	ReportVerify     []ReportVerify    `json:"report_verify,omitempty"`
}

// DeletionHotspot is one file's churn in the job's diff: Deleted lines is what the
// list is sorted by, DeletionHeavy marks the files whose deletions dwarf their
// additions.
type DeletionHotspot struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
	// Net is added-deleted: negative means the file shrank.
	Net int `json:"net"`
	// DeletionHeavy is true when the file lost at least twice the lines it gained
	// (and lost at least reviewHotspotMinDeleted of them).
	DeletionHeavy bool `json:"deletion_heavy"`
}

// ReportVerify is a verification output block the agent's own report presents: the
// fenced code block under a 「验证」/Verification heading. It is the agent's account
// of what it ran (Source is always VerifySourceAgent), shown as a reference when the
// job itself ran no verify step.
type ReportVerify struct {
	// Heading is the report heading the block sits under, without the leading '#'.
	Heading string `json:"heading"`
	// Text is the block body, its tail kept when it is very long.
	Text string `json:"text"`
	// Source names where the block comes from: "agent" (its own report).
	Source string `json:"source"`
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
		ReportVerify:     reportVerify(report),
	}
}

// diffFileScan is one file's slice of a unified diff: its churn counts, the added
// lines (to tell a moved comment from a deleted one) and the comment lines it drops.
type diffFileScan struct {
	path    string
	added   int
	deleted int
	// binary is set on a "Binary files … differ" / "GIT binary patch" section: its
	// lines are not patch body, so neither churn nor comments are counted.
	binary bool
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
		case strings.HasPrefix(line, "Binary files "):
			// Binary content has no lines to count (and no comments to lose).
			cur.binary = true
		case strings.HasPrefix(line, "GIT binary patch"):
			cur.binary = true
		case cur.binary:
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
		out = append(out, DeletionHotspot{
			Path: f.path, Added: f.added, Deleted: f.deleted,
			Net: f.added - f.deleted, DeletionHeavy: deletionHeavy(f.added, f.deleted),
		})
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

// deletionHeavy reports whether a file's churn is mostly deletion: it lost at least
// reviewHotspotHeavyRatio times what it gained. Below the hotspot threshold the
// ratio alone would flag ordinary churn, and a deletion of every line of a small
// file is not a hotspot either.
func deletionHeavy(added, deleted int) bool {
	return deleted >= reviewHotspotMinDeleted && deleted >= reviewHotspotHeavyRatio*added
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

// reportVerify collects the verification output blocks the agent's report presents:
// every fenced code block under a 「验证」/Verification heading, in report order. The
// blocks are the agent's own account (Source "agent") — a reference for a job whose
// own verify step was not configured, not a substitute for it.
func reportVerify(report string) []ReportVerify {
	var out []ReportVerify
	heading, level := "", 0 // the verify section currently open
	var body []string
	inFence := false

	flush := func() {
		defer func() { body = nil }()
		if heading == "" || len(out) >= reviewReportVerifyLimit {
			return
		}
		text := strings.TrimRight(strings.Join(body, "\n"), "\n")
		if strings.TrimSpace(text) == "" {
			return
		}
		out = append(out, ReportVerify{
			Heading: heading, Text: markedTailLines(text, reviewReportVerifyLineLimit), Source: VerifySourceAgent,
		})
	}

	for _, raw := range strings.Split(report, "\n") {
		line := strings.TrimRight(raw, "\r")
		switch {
		case inFence:
			if isFence(line) {
				inFence = false
				flush()
				continue
			}
			body = append(body, line)
		case isFence(line):
			// A fence outside a verify section is the agent's own code sample.
			if heading != "" {
				inFence = true
				body = nil
			}
		default:
			text, lv, ok := atxHeading(line)
			if !ok {
				continue
			}
			switch {
			case isVerifyHeading(text):
				heading, level = text, lv
			case heading != "" && lv <= level:
				// The next heading of the same or a higher level closes the section.
				heading, level = "", 0
			}
		}
	}
	if inFence {
		flush() // a block that runs to the end of the report
	}
	return out
}

// isFence reports whether a line opens or closes a markdown code fence.
func isFence(line string) bool {
	t := strings.TrimLeft(line, " \t")
	return strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~")
}

// atxHeading parses a markdown ATX heading ("## Verification"): its text without the
// leading '#'s and its level. A '#' run not followed by a space is not a heading.
func atxHeading(line string) (text string, level int, ok bool) {
	t := strings.TrimLeft(line, " \t")
	n := 0
	for n < len(t) && t[n] == '#' {
		n++
	}
	if n == 0 || n > 6 || (n < len(t) && t[n] != ' ' && t[n] != '\t') {
		return "", 0, false
	}
	text = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(t[n:]), "#"))
	if text == "" {
		return "", 0, false
	}
	return text, n, true
}

// isVerifyHeading reports whether a heading announces verification output:
// 「验证」/「验证结果」 or Verification/verify/tests in English.
func isVerifyHeading(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(text, "验证") || strings.Contains(lower, "verif") ||
		strings.Contains(lower, "verify")
}

// markedTailLines keeps the last n lines of s, prefixed with a marker line naming how
// many were dropped (a verification block ends with its verdict).
func markedTailLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return fmt.Sprintf("…（前面 %d 行已省略）\n%s", len(lines)-n, strings.Join(lines[len(lines)-n:], "\n"))
}
