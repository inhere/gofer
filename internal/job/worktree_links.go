package job

import (
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/inhere/gofer/internal/config"
)

// worktreeCreateOptions is the resolved "where does this job's worktree go" decision
// (2026-10-21 design「一、worktree 的位置、分支名与依赖链接」): the project's dir/branch
// templates rendered for this job, an explicit --branch override, and the project's
// dependency links. It is resolved ONCE on the submitting side and carried on the
// request, so the executing machine (serve-local or a worker) needs no project config
// of its own to lay out the checkout.
type worktreeCreateOptions struct {
	// Name is --worktree-name, "" = the job id (the historical layout).
	Name string
	// Dir is the checkout dir relative to the repository toplevel.
	Dir string
	// Branch is the branch the job's commits land on.
	Branch string
	// Links are project-relative directories to link from the main checkout into the
	// fresh worktree (see linkWorktreeDeps).
	Links []string
}

// worktreeOptionsFor resolves a --worktree job's create options from the project's
// worktree section, the request's --worktree-name / --branch, and the job id. A name
// that cannot safely be part of a path and a branch name git will refuse are rejected
// at submit — before anything is created — because the worktree is keyed by them.
func worktreeOptionsFor(proj config.ProjectConfig, req *JobRequest, jobID string) (worktreeCreateOptions, error) {
	name := strings.TrimSpace(req.WorktreeName)
	if !config.WorktreeNameValid(name) {
		return worktreeCreateOptions{}, fmt.Errorf("%w: worktree name %q may only contain letters, digits, '.', '_' and '-'", ErrInvalidRequest, name)
	}
	dir := proj.RenderWorktreeDir(name, jobID)
	if err := checkWorktreeRelDir(dir); err != nil {
		return worktreeCreateOptions{}, err
	}
	branch := strings.TrimSpace(req.WorktreeBranch)
	if branch == "" {
		branch = proj.RenderWorktreeBranch(name, jobID)
	}
	if err := checkWorktreeBranchName(branch); err != nil {
		return worktreeCreateOptions{}, err
	}
	return worktreeCreateOptions{Name: name, Dir: dir, Branch: branch, Links: proj.WorktreeLinks()}, nil
}

// checkWorktreeRelDir requires a worktree dir that stays inside the repository: the
// value comes from a project template, and a template escaping the checkout would put
// a git worktree (and the job's writes) somewhere nobody looks.
func checkWorktreeRelDir(dir string) error {
	slashed := strings.ReplaceAll(strings.TrimSpace(dir), "\\", "/")
	cleaned := path.Clean(slashed)
	if cleaned == "" || cleaned == "." || path.IsAbs(cleaned) ||
		cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("%w: worktree dir %q must be a path inside the repository", ErrInvalidRequest, dir)
	}
	return nil
}

// checkWorktreeBranchName rejects what git would refuse anyway, at submit instead of
// after the worktree checkout was already created.
func checkWorktreeBranchName(branch string) error {
	if branch == "" || strings.HasPrefix(branch, "-") || strings.Contains(branch, "..") ||
		strings.HasSuffix(branch, ".") || strings.HasSuffix(branch, ".lock") ||
		strings.ContainsAny(branch, " \t\r\n~^:?*[\\") {
		return fmt.Errorf("%w: invalid worktree branch %q", ErrInvalidRequest, branch)
	}
	return nil
}

// worktreeLinks is the outcome of laying the project's dependency links into a fresh
// worktree, as the job's timeline event reports it: what was linked, and why an entry
// was left alone. Every entry is best-effort — a missing source or a refused link is
// recorded and the job still runs, because a worktree job (or its branch) must not be
// blocked by a dependency directory that is not there.
type worktreeLinks struct {
	Linked  []string          `json:"linked,omitempty"`
	Skipped map[string]string `json:"skipped,omitempty"`
}

// linkWorktreeDeps creates one link per configured project-relative entry inside the
// worktree, pointing at the same path in the MAIN checkout (Windows directory junction,
// elsewhere a symlink). The link is what lets a job in the worktree use dependencies
// nobody commits (a vendored node_modules): the main checkout owns the bytes, the
// worktree borrows them.
func linkWorktreeDeps(wt *worktreeRef, links []string) worktreeLinks {
	var out worktreeLinks
	for _, rel := range links {
		rel = strings.TrimSpace(rel)
		if rel == "" {
			continue
		}
		from := filepath.FromSlash(rel)
		src := filepath.Join(wt.Top, from)
		dst := filepath.Join(wt.Path, from)
		if fi, err := os.Stat(src); err != nil || !fi.IsDir() {
			out.skip(rel, "source directory is not present in the main checkout")
			continue
		}
		if _, err := os.Lstat(dst); err == nil {
			// The worktree already has that path (committed, or created by an earlier
			// run of this job): never clobber it with a link.
			out.skip(rel, "path already exists in the worktree")
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			out.skip(rel, "create parent dir: "+err.Error())
			continue
		}
		if err := createDirLink(src, dst); err != nil {
			out.skip(rel, "create link: "+err.Error())
			continue
		}
		out.Linked = append(out.Linked, rel)
	}
	return out
}

func (l *worktreeLinks) skip(rel, reason string) {
	slog.Warn("worktree link skipped", "path", rel, "reason", reason)
	if l.Skipped == nil {
		l.Skipped = make(map[string]string)
	}
	l.Skipped[rel] = reason
}

// removeWorktreeLinks deletes the configured links from a worktree checkout BEFORE the
// checkout itself is removed. It only unlinks a path that IS a link (directory
// junction / symlink) — a real directory by that name belongs to the job's tree and is
// git's business. Deleting the link removes the link, never the directory it points
// at, so the main checkout's copy (and its contents) survives the cleanup.
func removeWorktreeLinks(wtPath string, links []string) []string {
	var removed []string
	for _, rel := range links {
		rel = strings.TrimSpace(rel)
		if rel == "" {
			continue
		}
		dst := filepath.Join(wtPath, filepath.FromSlash(rel))
		fi, err := os.Lstat(dst)
		if err != nil || !isDirLink(fi, dst) {
			continue
		}
		if err := os.Remove(dst); err != nil {
			// Never fall through to a recursive delete: the remaining risk is a stale
			// link inside a checkout we are about to remove anyway, not a lost main tree.
			slog.Warn("remove worktree link", "path", dst, "err", err)
			continue
		}
		removed = append(removed, rel)
	}
	return removed
}

// mergeWorktreeLinks merges the recorded links (what the checkout actually carries) with
// the project's current ones, keeping the record's order and dropping blanks/duplicates.
func mergeWorktreeLinks(recorded, configured []string) []string {
	seen := make(map[string]bool, len(recorded)+len(configured))
	out := make([]string, 0, len(recorded)+len(configured))
	for _, group := range [][]string{recorded, configured} {
		for _, rel := range group {
			rel = strings.TrimSpace(rel)
			if rel == "" || seen[rel] {
				continue
			}
			seen[rel] = true
			out = append(out, rel)
		}
	}
	return out
}

// sweepWorktreeLinks is the second line of defence before a worktree checkout is
// deleted: it unlinks every link / junction found at the worktree's top level and on
// the way down to each known link path. Only the LINK is removed (never the directory it
// points at), so this can never reach into the main checkout.
func sweepWorktreeLinks(wtPath string, known []string) []string {
	var removed []string
	unlink := func(p string) {
		fi, err := os.Lstat(p)
		if err != nil || !isDirLink(fi, p) {
			return
		}
		if err := os.Remove(p); err != nil {
			slog.Warn("sweep worktree link", "path", p, "err", err)
			return
		}
		removed = append(removed, p)
	}
	if entries, err := os.ReadDir(wtPath); err == nil {
		for _, e := range entries {
			unlink(filepath.Join(wtPath, e.Name()))
		}
	}
	for _, rel := range known {
		rel = strings.TrimSpace(rel)
		if rel == "" {
			continue
		}
		cur := wtPath
		for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
			if part == "" || part == "." || part == ".." {
				break
			}
			cur = filepath.Join(cur, part)
			fi, err := os.Lstat(cur)
			if err != nil {
				break
			}
			if isDirLink(fi, cur) {
				unlink(cur)
				break
			}
			if !fi.IsDir() {
				break
			}
		}
	}
	return removed
}
