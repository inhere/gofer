package bdmigrate

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/inhere/gofer/internal/procattr"
)

// GitPlan covers core.hooksPath and the hook scripts bd keeps in .beads/hooks.
type GitPlan struct {
	HooksPath     string   `json:"core_hooks_path,omitempty"`
	PointsAtBeads bool     `json:"points_at_beads_hooks"`
	Custom        []string `json:"custom_hooks,omitempty"`    // .beads/hooks files with content besides bd's block
	GitDirShims   []string `json:"gitdir_bd_shims,omitempty"` // $GIT_DIR/hooks files that also call bd
	Action        string   `json:"action"`                    // none | unset | keep
	Reason        string   `json:"reason,omitempty"`
}

var bdHookBlockRe = regexp.MustCompile(`(?s)# --- BEGIN BEADS INTEGRATION.*?# --- END BEADS INTEGRATION[^\n]*\n?`)

// customHookContent reports whether a hook script has anything beyond bd's
// managed block, a shebang, comments and blank lines.
func customHookContent(body string) bool {
	rest := bdHookBlockRe.ReplaceAllString(body, "")
	for _, line := range strings.Split(rest, "\n") {
		t := strings.TrimSpace(line)
		if t != "" && !strings.HasPrefix(t, "#") {
			return true
		}
	}
	return false
}

func gitOut(root string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	procattr.Background(cmd)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// planGit inspects (read-only) how git hooks reach .beads/hooks.
func planGit(root string) GitPlan {
	plan := GitPlan{Action: "none"}
	if top, err := gitOut(root, "rev-parse", "--show-toplevel"); err != nil {
		return plan // not a git repository: nothing to do
	} else if !samePath(top, root) {
		plan.Reason = "this directory is inside the git repository at " + top + "; core.hooksPath belongs to that repository and is not touched"
		return plan
	}
	value, err := gitOut(root, "config", "--local", "--get", "core.hooksPath")
	if err != nil || value == "" {
		return plan
	}
	plan.HooksPath = value
	norm := strings.TrimSuffix(strings.ReplaceAll(value, "\\", "/"), "/")
	plan.PointsAtBeads = norm == ".beads/hooks" || strings.HasSuffix(norm, "/.beads/hooks")
	if !plan.PointsAtBeads {
		plan.Reason = "core.hooksPath does not point at .beads/hooks; left alone"
		return plan
	}
	entries, _ := os.ReadDir(filepath.Join(root, ".beads", "hooks"))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, ".beads", "hooks", e.Name()))
		if err == nil && customHookContent(string(b)) {
			plan.Custom = append(plan.Custom, e.Name())
		}
	}
	if gitDir, err := gitOut(root, "rev-parse", "--git-dir"); err == nil {
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(root, gitDir)
		}
		shims, _ := os.ReadDir(filepath.Join(gitDir, "hooks"))
		for _, e := range shims {
			if e.IsDir() || strings.HasSuffix(e.Name(), ".sample") {
				continue
			}
			if b, err := os.ReadFile(filepath.Join(gitDir, "hooks", e.Name())); err == nil && strings.Contains(string(b), "BEADS INTEGRATION") {
				plan.GitDirShims = append(plan.GitDirShims, e.Name())
			}
		}
	}
	switch {
	case len(plan.Custom) > 0:
		plan.Action = "keep"
		plan.Reason = fmt.Sprintf(".beads/hooks has custom content in %s; core.hooksPath kept, move those hooks by hand first", strings.Join(plan.Custom, ", "))
	case len(plan.GitDirShims) > 0:
		plan.Action = "keep"
		plan.Reason = fmt.Sprintf("unsetting core.hooksPath would activate bd shims in the git dir hooks (%s); remove them first", strings.Join(plan.GitDirShims, ", "))
	default:
		plan.Action = "unset"
		plan.Reason = ".beads/hooks holds only bd's own hook scripts"
	}
	return plan
}

func unsetHooksPath(root string) error {
	cmd := exec.Command("git", "-C", root, "config", "--local", "--unset", "core.hooksPath")
	procattr.Background(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("unset core.hooksPath: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// samePath compares two directory paths after resolving symlinks.
func samePath(a, b string) bool {
	resolve := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			p = r
		}
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		return filepath.Clean(p)
	}
	ra, rb := resolve(a), resolve(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(ra, rb)
	}
	return ra == rb
}
