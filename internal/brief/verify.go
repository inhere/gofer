package brief

import (
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// verifyPkgsMax caps the packages named in the go test command.
const verifyPkgsMax = 8

// verifySection derives issue-specific verification commands from the code-entry
// files: the Go packages they sit in (one `go test` line) and, when web/ files are
// involved, the web checks. It sits before the generic rule memories, which stay
// authoritative (flags, -count and platform extras come from them).
func verifySection(root string, files []string) Section {
	sec := Section{Title: "本 issue 的验证命令", More: "gofer memory ls verify"}
	var pkgs []string
	seen := map[string]bool{}
	web, tagged := false, false
	for _, f := range files {
		switch {
		case strings.HasPrefix(f, "web/"):
			web = true
		case strings.HasSuffix(f, ".go"):
			dir := path.Dir(f)
			if root != "" {
				if st, err := os.Stat(filepath.Join(root, filepath.FromSlash(dir))); err != nil || !st.IsDir() {
					continue // the package is gone at the current checkout
				}
			}
			if !seen[dir] {
				seen[dir] = true
				pkgs = append(pkgs, "./"+dir)
			}
			base := strings.TrimSuffix(path.Base(f), ".go")
			if strings.HasSuffix(base, "_windows") || strings.HasSuffix(base, "_linux") || strings.HasSuffix(base, "_darwin") {
				tagged = true
			}
		}
	}
	sort.Strings(pkgs)
	if len(pkgs) == 0 && !web {
		sec.Note = "代码入口里没有 Go 包或 web/ 文件，验证命令见下方 rule 记忆"
		return sec
	}
	if len(pkgs) > 0 {
		list := pkgs
		extra := ""
		if len(list) > verifyPkgsMax {
			list, extra = list[:verifyPkgsMax], "  # 另有 "+strconv.Itoa(len(pkgs)-verifyPkgsMax)+" 个包，按需补"
		}
		sec.Lines = append(sec.Lines,
			"- `go build ./... && go vet ./...`",
			"- `go test -race -count=1 "+strings.Join(list, " ")+"`"+extra)
		if tagged {
			sec.Lines = append(sec.Lines, "- 入口含平台构建标签文件：另跑 `GOOS=darwin go vet ./...`、`GOOS=windows go vet ./...`")
		}
	}
	if web {
		sec.Lines = append(sec.Lines, "- web/：`cd web && npx vue-tsc --noEmit && npx vitest run && npx vite build`")
	}
	sec.Lines = append(sec.Lines, "- （由代码入口文件推导；全量、Windows、已知偶发等以下方 rule 记忆为准）")
	return sec
}
