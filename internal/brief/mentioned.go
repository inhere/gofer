package brief

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/inhere/gofer/internal/tracker"
)

// mentionedFilesMax bounds the files lifted from the issue's own text.
const mentionedFilesMax = 8

// mentionRe matches path-like tokens in issue text: a source file name with an
// optional directory part (internal/job/gitdiff.go, gitdiff.go, web/src/utils/scope.ts).
var mentionRe = regexp.MustCompile(`[A-Za-z0-9_./-]*[A-Za-z0-9_-]+\.(?:go|ts|vue)\b`)

// mentionedFiles resolves the source files the issue text names (description, design,
// acceptance, comments — a takeover plan usually names the files to change) to repo
// paths. A token with a directory part must exist as given; a bare file name is looked
// up with `git ls-files` and kept only when it is unambiguous (≤ 2 matches), so a
// common name like main.go does not flood the list. Test files are skipped, as in
// codeEntries.
func mentionedFiles(root string, it tracker.Issue) []string {
	if root == "" {
		return nil
	}
	texts := []string{it.Description, it.Design, it.AcceptanceCriteria}
	for _, c := range it.Comments {
		texts = append(texts, c.Text)
	}
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if !seen[p] && codeEntryFile(p) && len(out) < mentionedFilesMax {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, text := range texts {
		for _, tok := range mentionRe.FindAllString(text, -1) {
			tok = strings.TrimLeft(tok, "./")
			if tok == "" {
				continue
			}
			if strings.Contains(tok, "/") {
				if st, err := os.Stat(filepath.Join(root, filepath.FromSlash(tok))); err == nil && !st.IsDir() {
					add(tok)
				}
				continue
			}
			raw, err := git(root, "ls-files", "--", "*/"+tok, tok)
			if err != nil {
				continue
			}
			var hits []string
			for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
				if line = strings.TrimSpace(line); line != "" {
					hits = append(hits, line)
				}
			}
			if len(hits) > 0 && len(hits) <= 2 {
				for _, h := range hits {
					add(h)
				}
			}
		}
	}
	return out
}

// mergeEntries puts the issue's own mentions first, then the commit-derived entries,
// without duplicates, capped at codeEntriesMax.
func mergeEntries(mentioned, fromCommits []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, codeEntriesMax)
	for _, group := range [][]string{mentioned, fromCommits} {
		for _, p := range group {
			if !seen[p] && len(out) < codeEntriesMax {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// hasOwnCommit reports whether any related commit names the issue itself (rather than
// reaching it through the parent or a sibling's close reason).
func hasOwnCommit(id string, commits []commit) bool {
	for _, c := range commits {
		if c.Via == id {
			return true
		}
	}
	return false
}
