package config

import (
	"path/filepath"
	"runtime"
	"strings"
)

// ProjectForPath resolves cwd to the project whose execution-root (ExecPath) it
// falls under — the standalone-only fallback source for `--project auto` when the
// dir has no `.gofer.project.yaml` key: (xu64.15). A project matches when cwd
// equals its ExecPath or sits below it; on multiple matches the LONGEST prefix
// wins (nested projects), and two equal-length matches are ambiguous → ("", false)
// so the caller errors rather than guessing. No match → ("", false). Projects
// with an empty/"." ExecPath are skipped. The tie test is independent of map
// iteration order (a strictly-longer match resets any earlier tie).
//
// Note: cfg.Projects is map[string]ProjectConfig, so the key is the map key
// (ProjectConfig itself carries no Key field). ExecPath honours server.path_view
// (host_path by default, container_path under path_view=container), matching the
// path view the rest of the gofer process uses (G002).
func (c *Config) ProjectForPath(cwd string) (key string, ok bool) {
	key, _, ok = c.ProjectMatchForPath(cwd)
	return key, ok
}

// ProjectMatchForPath is ProjectForPath plus the matched project's exec root
// (the longest path prefix that decided the match), for messages that must say
// WHY a directory belongs to a project.
func (c *Config) ProjectMatchForPath(cwd string) (key, root string, ok bool) {
	cwd = resolveMatchPath(cwd)
	best, bestRoot, bestLen, tie := "", "", -1, false
	for k, p := range c.Projects {
		// Match on the resolved paths, but report the root as configured: the caller shows it
		// to a person and walks the cwd up to it lexically (nestedOuterRepo), and a resolved
		// root (/private/var on macOS, a long name for a Windows 8.3 path) matches neither.
		configured := filepath.Clean(c.ExecPath(p))
		ep := resolveMatchPath(configured)
		if ep == "" || ep == "." {
			continue
		}
		if matchPathPrefix(cwd, ep) {
			switch {
			case len(ep) > bestLen:
				best, bestRoot, bestLen, tie = k, configured, len(ep), false
			case len(ep) == bestLen:
				tie = true
			}
		}
	}
	if best == "" || tie {
		return "", "", false
	}
	return best, bestRoot, true
}

// resolveMatchPath makes project matching agree across symlinked workspace paths. A path may
// not exist yet (for example, a configured project root), so a failed resolution falls back to
// the lexical clean path. The two sides must use the same fallback rule.
func resolveMatchPath(path string) string {
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	return path
}

func matchPathPrefix(path, root string) bool {
	if runtime.GOOS == "windows" {
		path, root = strings.ToLower(path), strings.ToLower(root)
	}
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}
