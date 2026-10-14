package job

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/util"
)

// adddir.go is the nested-repository half of the extra-directory injection: an agent
// that declares add_dir_args (config.AgentConfig.AddDirArgs, rendered from {{dir}}) is
// handed the project root when its job runs INSIDE one of the repositories the project
// contains.
//
// Why: a workspace project is often a root holding several checkouts (`.worktrees/`,
// `gofer/`, `web/`, …). A job whose cwd is one of those inner repositories runs in a git
// repository of its own, so everything the operator keeps at the workspace level — the
// rules files, the tracker, the docs an enclosing project carries — is invisible to the
// agent unless gofer passes it. Read-only is the caller's choice of template (the
// built-in suga one uses `:ro`): the enclosing directory is context to read, not a
// second place to write.
const addDirProbeTimeout = 5 * time.Second

// addDirArgs renders ac.AddDirArgs for a job about to run in absCwd, or returns nil when
// nothing should be appended:
//
//   - the agent declares no fragment, or is not a cli-agent (an acp-agent's argv is the
//     protocol's), or there is no cwd / no project root to speak of;
//   - absCwd is in the project root's OWN repository (or an ancestor of it): the agent
//     already sees the workspace, adding it again would only be noise;
//   - the repository of absCwd cannot be established at all (no git on PATH, no
//     checkout): guessing there would hand the agent a directory nobody asked for, and a
//     read-only extra directory does not earn an error where a job would otherwise run.
func addDirArgs(cfg *config.Config, proj config.ProjectConfig, ac config.AgentConfig, absCwd string) []string {
	if cfg == nil || len(ac.AddDirArgs) == 0 || ac.Type != agent.TypeCLIAgent || strings.TrimSpace(absCwd) == "" {
		return nil
	}
	root := strings.TrimSpace(cfg.ExecPath(proj))
	if root == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), addDirProbeTimeout)
	defer cancel()
	top := strings.TrimSpace(string(runGit(ctx, absCwd, 4096, "rev-parse", "--show-toplevel")))
	if top == "" || sameDir(top, root) {
		return nil
	}
	return agent.Render(ac.AddDirArgs, agent.Vars{Dir: root})
}

// sameDir reports whether two paths name the same directory. Symlinks are resolved
// first (a project root reached through one must still count as its own repository's
// top level) and the comparison folds case where the filesystem does, which is what
// filepath.Rel does on Windows and does not on Unix.
func sameDir(a, b string) bool {
	rel, err := filepath.Rel(util.RealPath(a), util.RealPath(b))
	return err == nil && rel == "."
}
