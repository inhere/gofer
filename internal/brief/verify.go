package brief

import (
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/inhere/gofer/internal/tracker"
)

// verifyPkgsMax caps the directories a verification command names.
const verifyPkgsMax = 8

const verifyConfigHint = "`.gofer/tracker/config.yaml` 的 `brief.verify`"

// verifySection lists the verification commands for the issue's code-entry files.
// With `brief.verify` rules configured, every rule whose paths match an entry file
// (or that has no paths) contributes its command. Without rules there is only a
// generic hint: when the repository has a go.mod, `go test` on the Go packages the
// entries sit in. Either way the generic rule memories below stay authoritative.
func verifySection(root string, rules []tracker.BriefVerifyRule, files []string) Section {
	sec := Section{Title: "本 issue 的验证命令", More: "gofer memory ls verify"}
	if len(rules) > 0 {
		for _, r := range rules {
			if line, ok := verifyRuleLine(root, r, files); ok {
				sec.Lines = append(sec.Lines, line)
			}
		}
		if len(sec.Lines) == 0 {
			sec.Note = verifyConfigHint + " 没有匹配本 issue 代码入口的命令，验证命令见下方 rule 记忆"
			return sec
		}
		sec.Lines = append(sec.Lines, "- （按 "+verifyConfigHint+" 匹配代码入口；全量与其他约束以下方 rule 记忆为准）")
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
		sec.Note = "无法从代码入口推导验证命令；可在 " + verifyConfigHint + " 按路径配置本仓库的命令，或见下方 rule 记忆"
		return sec
	}
	list, extra := capList(pkgs, "个包")
	sec.Lines = append(sec.Lines,
		"- `go build ./... && go vet ./...`",
		"- `go test -count=1 "+strings.Join(list, " ")+"`"+extra,
		"- （由代码入口推导的通用提示；本仓库的验证命令可在 "+verifyConfigHint+" 配置，全量与平台相关的约束以下方 rule 记忆为准）")
	return sec
}

// verifyRuleLine renders one configured rule for the entry files; ok=false when the
// rule does not apply (no path matches, or a placeholder has nothing to expand to).
func verifyRuleLine(root string, r tracker.BriefVerifyRule, files []string) (string, bool) {
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
		list, extra = capList(dirs, "个目录")
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
		list, more := capList(present, "个文件")
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
func capList(items []string, unit string) ([]string, string) {
	if len(items) <= verifyPkgsMax {
		return items, ""
	}
	return items[:verifyPkgsMax], "  # 另有 " + strconv.Itoa(len(items)-verifyPkgsMax) + " " + unit + "，按需补"
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
