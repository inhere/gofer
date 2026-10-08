package hookrelay

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// EnvIgnoreCwds extends the ignored-directory list: paths separated by the OS
// path-list separator (":" on unix, ";" on Windows).
const EnvIgnoreCwds = "GOFER_HOOK_IGNORE_CWDS"

// DefaultIgnoreCwds are agent-internal working directories whose sessions are not
// the person's work (N1 §C, gofer-v74l): codex runs its memory-consolidation agent
// with cwd ~/.codex/memories, which would otherwise register a phantom session and
// a work item on every run. Relative to the user's home directory.
var DefaultIgnoreCwds = []string{filepath.Join(".codex", "memories")}

// IgnoredCwd reports whether a hook payload's cwd lies in (or is) an ignored
// directory, matching by path prefix on a separator boundary. home is the user's
// home directory (empty = look it up); getenv resolves EnvIgnoreCwds (nil = os).
// Windows compares case-insensitively and treats / and \ alike.
func IgnoredCwd(cwd, home string, getenv func(string) string) bool {
	if strings.TrimSpace(cwd) == "" {
		return false
	}
	if getenv == nil {
		getenv = os.Getenv
	}
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	var dirs []string
	if home != "" {
		for _, rel := range DefaultIgnoreCwds {
			dirs = append(dirs, filepath.Join(home, rel))
		}
	}
	for _, d := range filepath.SplitList(getenv(EnvIgnoreCwds)) {
		if d = strings.TrimSpace(d); d != "" {
			dirs = append(dirs, expandHome(d, home))
		}
	}
	return ignoredCwdIn(cwd, dirs, runtime.GOOS == "windows")
}

func expandHome(p, home string) string {
	if home != "" && (p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`)) {
		return filepath.Join(home, p[1:])
	}
	return p
}

func normIgnorePath(p string, fold bool) string {
	if fold {
		p = strings.ReplaceAll(p, `\`, "/")
		p = strings.ToLower(p)
	}
	p = filepath.ToSlash(filepath.Clean(p))
	return strings.TrimRight(p, "/")
}

func ignoredCwdIn(cwd string, dirs []string, fold bool) bool {
	c := normIgnorePath(cwd, fold)
	for _, d := range dirs {
		n := normIgnorePath(d, fold)
		if n == "" || n == "." {
			continue
		}
		if c == n || strings.HasPrefix(c, n+"/") {
			return true
		}
	}
	return false
}
