package brief

import (
	"os/exec"
	"regexp"
	"sort"
	"strings"

	"github.com/inhere/gofer/internal/procattr"
)

const (
	// commitsMax caps the related commits listed.
	commitsMax = 20
	// codeEntriesMax caps the 「代码入口」 files.
	codeEntriesMax = 10
	// siblingSHAsMax caps the commit hashes taken from sibling close reasons.
	siblingSHAsMax = 10
	// siblingLookupsMax caps the git lookups of hash-like tokens in sibling close
	// reasons (dates and numbers are candidates too and fail the lookup).
	siblingLookupsMax = 3 * siblingSHAsMax
)

// commit is one related commit.
type commit struct {
	Hash    string `json:"hash"`
	Short   string `json:"short"`
	Date    string `json:"date"`
	Subject string `json:"subject"`
	// Via says why it is listed: the issue id, the parent id or a sibling's close reason.
	Via string `json:"via"`
}

// shaRef finds commit-hash-like tokens (7–40 hex chars).
var shaRef = regexp.MustCompile(`\b[0-9a-f]{7,40}\b`)

// shaCandidates lists the distinct hash-like tokens of text. All-digit tokens stay in:
// about 1 in 40 eight-char short hashes has no letter (e.g. 28513304), and dropping
// them silently lost that sibling's commit. Every candidate is checked with git
// before it is listed, so a date or a number only costs one failed lookup
// (capped by siblingLookupsMax).
func shaCandidates(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range shaRef.FindAllString(text, -1) {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

func git(root string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	procattr.Background(cmd)
	out, err := cmd.Output()
	return string(out), err
}

const (
	gitFieldSep  = "\x1f"
	gitRecordSep = "\x1e"
	gitFormat    = "--format=%H%x1f%h%x1f%ad%x1f%s%x1f%B%x1e"
)

// logRow is one parsed `git log` record with its full message body.
type logRow struct {
	c    commit
	body string
}

func parseGitLog(out string) []logRow {
	var rows []logRow
	for _, rec := range strings.Split(out, gitRecordSep) {
		rec = strings.TrimLeft(rec, "\n")
		parts := strings.SplitN(rec, gitFieldSep, 5)
		if len(parts) < 5 {
			continue
		}
		rows = append(rows, logRow{c: commit{Hash: parts[0], Short: parts[1], Date: parts[2], Subject: parts[3]}, body: parts[4]})
	}
	return rows
}

// relatedCommits lists the commits whose message names id (or parent), then the
// commits named in the sibling close reasons. ok=false: root is not a git checkout.
func relatedCommits(root, id, parent string, siblingReasons map[string]string) ([]commit, bool) {
	if root == "" {
		return nil, false
	}
	if _, err := git(root, "rev-parse", "--git-dir"); err != nil {
		return nil, false
	}
	args := []string{"log", "--fixed-strings", "--date=short", gitFormat, "-n", "200", "--grep=" + id}
	if parent != "" {
		args = append(args, "--grep="+parent)
	}
	var out []commit
	seen := map[string]bool{}
	if raw, err := git(root, args...); err == nil {
		for _, row := range parseGitLog(raw) {
			msg := row.c.Subject + "\n" + row.body
			switch {
			case mentions(msg, id):
				row.c.Via = id
			case parent != "" && mentions(msg, parent):
				row.c.Via = parent
			default:
				continue
			}
			seen[row.c.Hash] = true
			out = append(out, row.c)
		}
	}
	siblings := make([]string, 0, len(siblingReasons))
	for sib := range siblingReasons {
		siblings = append(siblings, sib)
	}
	sort.Strings(siblings)
	taken, lookups := 0, 0
	for _, sib := range siblings {
		for _, sha := range shaCandidates(siblingReasons[sib]) {
			if taken >= siblingSHAsMax || lookups >= siblingLookupsMax {
				break
			}
			lookups++
			raw, err := git(root, "log", "--no-walk", "--date=short", gitFormat, sha+"^{commit}", "--")
			if err != nil {
				continue
			}
			for _, row := range parseGitLog(raw) {
				if seen[row.c.Hash] {
					continue
				}
				seen[row.c.Hash] = true
				row.c.Via = sib + " 关闭说明"
				out = append(out, row.c)
				taken++
			}
		}
	}
	return out, true
}

// codeEntries counts the files the commits touch (a merge against its first parent)
// and returns the most touched ones. Docs, tests and the tracker files are left out:
// docs have their own section and tests follow their code.
func codeEntries(root string, commits []commit) []string {
	if len(commits) == 0 {
		return nil
	}
	args := []string{"show", "-m", "--first-parent", "--name-only", "--format=%x1e"}
	for _, c := range commits {
		args = append(args, c.Hash)
	}
	raw, err := git(root, append(args, "--")...)
	if err != nil {
		return nil
	}
	count := map[string]int{}
	for _, rec := range strings.Split(raw, gitRecordSep) {
		files := map[string]bool{}
		for _, line := range strings.Split(rec, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || files[line] || !codeEntryFile(line) {
				continue
			}
			files[line] = true
			count[line]++
		}
	}
	paths := make([]string, 0, len(count))
	for p := range count {
		paths = append(paths, p)
	}
	sort.Slice(paths, func(i, j int) bool {
		if count[paths[i]] != count[paths[j]] {
			return count[paths[i]] > count[paths[j]]
		}
		return paths[i] < paths[j]
	})
	if len(paths) > codeEntriesMax {
		paths = paths[:codeEntriesMax]
	}
	return paths
}

func codeEntryFile(path string) bool {
	switch {
	case strings.HasPrefix(path, ".gofer/"), strings.HasPrefix(path, "docs/"):
		return false
	case strings.HasSuffix(path, "_test.go"), strings.Contains(path, ".test."), strings.Contains(path, ".spec."):
		return false
	}
	// Test files of other languages: foo_test.py / foo_spec.rb / test_foo.py.
	base := path[strings.LastIndex(path, "/")+1:]
	if strings.Contains(base, "_test.") || strings.Contains(base, "_spec.") || strings.HasPrefix(base, "test_") {
		return false
	}
	return true
}
