package brief

import (
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/tracker"
)

// verifyPkgsMax caps the directories a verification command names.
const verifyPkgsMax = 8

// verifySection lists the verification commands for the issue's code-entry files.
// With `brief.verify` rules configured, every rule whose paths match an entry file
// (or that has no paths) contributes its command. Without rules there is only a
// generic hint: when the repository has a go.mod, `go test` on the Go packages the
// entries sit in. Either way the generic rule memories below stay authoritative.
func verifySection(lang i18n.Lang, root string, rules []tracker.BriefVerifyRule, files []string) Section {
	sec := Section{Title: briefText(lang, "brief.verify.title"), More: "gofer memory ls verify"}
	hint := briefText(lang, "brief.verify.config_hint")
	if len(rules) > 0 {
		for _, r := range rules {
			if line, ok := verifyRuleLine(lang, root, r, files); ok {
				sec.Lines = append(sec.Lines, line)
			}
		}
		if len(sec.Lines) == 0 {
			sec.Note = briefText(lang, "brief.verify.no_match", "hint", hint)
			return sec
		}
		sec.Lines = append(sec.Lines, briefText(lang, "brief.verify.configured_note", "hint", hint))
		return sec
	}
	var pkgs []string
	if root != "" && fileExists(filepath.Join(root, "go.mod")) {
		var goFiles []string
		for _, f := range files {
			if strings.HasSuffix(f, ".go") {
				goFiles = append(goFiles, f)
			}
		}
		pkgs = existingDirs(root, goFiles)
	}
	if len(pkgs) == 0 {
		sec.Note = briefText(lang, "brief.verify.derive_note", "hint", hint)
		return sec
	}
	list, extra := capList(lang, pkgs, briefText(lang, "brief.verify.unit_package"))
	sec.Lines = append(sec.Lines,
		"- `go build ./... && go vet ./...`",
		"- `go test -count=1 "+strings.Join(list, " ")+"`"+extra,
		briefText(lang, "brief.verify.generic_note", "hint", hint))
	return sec
}

// verifyRuleLine renders one configured rule for the entry files; ok=false when the
// rule does not apply (no path matches, or a placeholder has nothing to expand to).
func verifyRuleLine(lang i18n.Lang, root string, r tracker.BriefVerifyRule, files []string) (string, bool) {
	cmd := strings.TrimSpace(r.Cmd)
	if cmd == "" {
		return "", false
	}
	matched := files
	if len(r.Paths) > 0 {
		matched = nil
		for _, f := range files {
			for _, p := range r.Paths {
				if tracker.MatchMemoryPath(p, f) {
					matched = append(matched, f)
					break
				}
			}
		}
		if len(matched) == 0 {
			return "", false
		}
	}
	extra := ""
	if strings.Contains(cmd, "{dirs}") {
		dirs := existingDirs(root, matched)
		if len(dirs) == 0 {
			return "", false
		}
		var list []string
		list, extra = capList(lang, dirs, briefText(lang, "brief.verify.unit_dir"))
		cmd = strings.ReplaceAll(cmd, "{dirs}", strings.Join(list, " "))
	}
	if strings.Contains(cmd, "{files}") {
		var present []string
		for _, f := range matched {
			if root == "" || fileExists(filepath.Join(root, filepath.FromSlash(f))) {
				present = append(present, f)
			}
		}
		if len(present) == 0 {
			return "", false
		}
		list, more := capList(lang, present, briefText(lang, "brief.verify.unit_file"))
		if extra == "" {
			extra = more
		}
		cmd = strings.ReplaceAll(cmd, "{files}", strings.Join(list, " "))
	}
	return "- `" + cmd + "`" + extra, true
}

// existingDirs lists the distinct directories of files as ./dir (sorted), leaving
// out the ones gone at the current checkout.
func existingDirs(root string, files []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range files {
		dir := path.Dir(f)
		if seen[dir] {
			continue
		}
		seen[dir] = true
		if root != "" {
			if st, err := os.Stat(filepath.Join(root, filepath.FromSlash(dir))); err != nil || !st.IsDir() {
				continue
			}
		}
		if dir == "." {
			out = append(out, ".")
		} else {
			out = append(out, "./"+dir)
		}
	}
	sort.Strings(out)
	return out
}

// capList keeps the first verifyPkgsMax items and says how many it left out.
func capList(lang i18n.Lang, items []string, unit string) ([]string, string) {
	if len(items) <= verifyPkgsMax {
		return items, ""
	}
	n := strconv.Itoa(len(items) - verifyPkgsMax)
	return items[:verifyPkgsMax], "  # " + briefText(lang, "brief.verify.cap", "n", n, "unit", unit)
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
