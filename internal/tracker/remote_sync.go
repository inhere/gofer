package tracker

import (
	"path"
	"strings"
)

// RepoCwd turns the repository path a sync reported (an absolute path on the machine
// that ran `repo sync`) into a cwd relative to one of the project's root views.
// roots are tried in order (the runner's own view first, then the other views of the
// project). The result is slash separated, "." for the project root itself. ok is
// false when the repository lies in none of the roots.
func RepoCwd(repoPath string, roots []string) (string, bool) {
	repo := normSyncPath(repoPath)
	if repo == "" {
		return "", false
	}
	for _, r := range roots {
		root := normSyncPath(r)
		if root == "" {
			continue
		}
		if rel, ok := relUnder(repo, root); ok {
			return rel, true
		}
	}
	return "", false
}

// normSyncPath cleans a path to forward slashes; a Windows drive letter is
// lowercased so D:\x and d:/x compare equal.
func normSyncPath(p string) string {
	p = strings.TrimSpace(strings.ReplaceAll(p, `\`, "/"))
	if p == "" {
		return ""
	}
	p = path.Clean(p)
	if len(p) >= 2 && p[1] == ':' {
		p = strings.ToLower(p[:1]) + p[1:]
	}
	return p
}

func relUnder(p, root string) (string, bool) {
	if p == root {
		return ".", true
	}
	prefix := strings.TrimSuffix(root, "/") + "/"
	if strings.HasPrefix(p, prefix) {
		return strings.TrimPrefix(p, prefix), true
	}
	return "", false
}
