package brief

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// symbolsMax caps the key symbols listed.
const symbolsMax = 15

var (
	hunkHeader = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@ ?(.*)$`)
	// goFunc matches a Go func / method declaration: group 1 receiver type, group 2 name.
	goFunc = regexp.MustCompile(`^func\s+(?:\(\s*\w*\s*\*?\s*(\w+)(?:\[[^\]]*\])?\s*\)\s*)?(\w+)`)
	// tsFunc matches a TS function declaration or a top-level arrow-function const.
	tsFunc = regexp.MustCompile(`^(?:export\s+)?(?:default\s+)?(?:async\s+)?(?:function\s*\*?\s*(\w+)|const\s+(\w+)\s*(?::[^=]+)?=\s*(?:async\s*)?(?:\([^)]*\)|\w+)\s*(?::[^=]+)?=>)`)
)

// symbol is a func the related commits added or changed.
type symbol struct {
	file, name string
	commits    int
}

// declName returns the symbol name a source line declares, or "".
func declName(file, line string) string {
	switch {
	case strings.HasSuffix(file, ".go"):
		if m := goFunc.FindStringSubmatch(line); m != nil {
			if m[1] != "" {
				return m[1] + "." + m[2]
			}
			return m[2]
		}
	case isTSFile(file):
		if m := tsFunc.FindStringSubmatch(line); m != nil {
			if m[1] != "" {
				return m[1]
			}
			return m[2]
		}
	}
	return ""
}

func isTSFile(file string) bool {
	for _, ext := range []string{".ts", ".tsx", ".vue"} {
		if strings.HasSuffix(file, ext) {
			return true
		}
	}
	return false
}

// keySymbols lists the Go / TS funcs the commits added or changed inside files (the
// code entries): a func counts when a diff line declares it or a hunk sits inside
// it (git's hunk header names the enclosing func). The result is ranked by how many
// commits touched the symbol, bounded, and carries file:line of the current checkout.
// Funcs that no longer exist are dropped.
func keySymbols(root string, commits []commit, files []string) []string {
	var paths []string
	for _, f := range files {
		if strings.HasSuffix(f, ".go") || isTSFile(f) {
			paths = append(paths, f)
		}
	}
	if len(commits) == 0 || len(paths) == 0 {
		return nil
	}
	args := []string{"show", "-m", "--first-parent", "-U0", "--no-color", "--format=%x1e"}
	for _, c := range commits {
		args = append(args, c.Hash)
	}
	args = append(args, "--")
	raw, err := git(root, append(args, paths...)...)
	if err != nil {
		return nil
	}
	counts := map[[2]string]int{}
	for _, rec := range strings.Split(raw, gitRecordSep) {
		seen := map[[2]string]bool{}
		file := ""
		for _, line := range strings.Split(rec, "\n") {
			switch {
			case strings.HasPrefix(line, "+++ "):
				file = strings.TrimPrefix(strings.TrimPrefix(line, "+++ "), "b/")
			case strings.HasPrefix(line, "@@"):
				if m := hunkHeader.FindStringSubmatch(line); m != nil && file != "" {
					if name := declName(file, m[2]); name != "" {
						seen[[2]string{file, name}] = true
					}
				}
			case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") && file != "":
				if name := declName(file, line[1:]); name != "" {
					seen[[2]string{file, name}] = true
				}
			}
		}
		for k := range seen {
			counts[k]++
		}
	}
	syms := make([]symbol, 0, len(counts))
	for k, n := range counts {
		syms = append(syms, symbol{file: k[0], name: k[1], commits: n})
	}
	sort.Slice(syms, func(i, j int) bool {
		a, b := syms[i], syms[j]
		if a.commits != b.commits {
			return a.commits > b.commits
		}
		if a.file != b.file {
			return a.file < b.file
		}
		return a.name < b.name
	})
	located := map[string]map[string]int{}
	var out []string
	for _, s := range syms {
		lines, ok := located[s.file]
		if !ok {
			lines = declLines(filepath.Join(root, filepath.FromSlash(s.file)), s.file)
			located[s.file] = lines
		}
		line, ok := lines[s.name]
		if !ok {
			continue
		}
		out = append(out, fmt.Sprintf("%s:%d  %s", s.file, line, s.name))
		if len(out) >= symbolsMax {
			break
		}
	}
	return out
}

// declLines maps each declared symbol of a source file to its first line.
func declLines(path, rel string) map[string]int {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	out := map[string]int{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		if name := declName(rel, sc.Text()); name != "" {
			if _, dup := out[name]; !dup {
				out[name] = n
			}
		}
	}
	return out
}
