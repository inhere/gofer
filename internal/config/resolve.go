package config

import (
	"path/filepath"
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
	cwd = filepath.Clean(cwd)
	best, bestRoot, bestLen, tie := "", "", -1, false
	for k, p := range c.Projects {
		ep := filepath.Clean(c.ExecPath(p))
		if ep == "" || ep == "." {
			continue
		}
		if cwd == ep || strings.HasPrefix(cwd, ep+string(filepath.Separator)) {
			switch {
			case len(ep) > bestLen:
				best, bestRoot, bestLen, tie = k, ep, len(ep), false
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
