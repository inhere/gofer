package job

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/util"
)

// The two placeholders a project's worktree.dir template may carry. They mirror the
// unexported ones of internal/config (which renders the template); the sweep parses the
// SAME syntax to read a job id back out of a checkout dir name.
const (
	worktreeNameTmpl  = "{name}"
	worktreeJobIDTmpl = "{job_id}"
)

// tempCheckoutSweepDirs caps how many distinct job working directories one project's
// sweep resolves to repository tops: each one costs a git call, and the sweep runs on
// every serve start plus every tempCheckoutSweepInterval tick.
const tempCheckoutSweepDirs = 64

// SweepStaleTempCheckouts removes the temporary `ref:` checkouts a crashed, killed or
// restarted process left behind (design 2026-10-21 §一·3). A job removes its own temp
// checkout when it ends (see cleanupTempWorktree), so anything still sitting in a
// project's worktree dir belongs either to a job that is STILL running (kept) or to a job
// that no longer owns it (removed).
//
// The scan is driven by the PROJECT's worktree.dir template, not by the default one: a
// project that puts its checkouts under `.worktrees/job-{job_id}` had its leftovers
// invisible to a hard-coded `tmp/gofer/wt` (gofer-bgkk — nine temp checkouts of a failed
// workflow survived several serve restarts that way). The job id is read back out of the
// directory name through the same template, so "does a live job still own this?" is
// answerable for ANY template.
//
// A checkout whose HEAD is on a branch is never touched: that is a `--worktree` job's
// deliverable (or a hand-made worktree), not a throwaway `ref:` checkout.
//
// It scans every repository one of the project's jobs ran in, as this process sees them
// (Config.ExecPath, G002) — see tempCheckoutRoots — and returns how many checkouts it
// removed (serve/worker log the count).
func (s *Service) SweepStaleTempCheckouts(ctx context.Context) int {
	cfg := s.Config()
	if cfg == nil {
		return 0
	}
	removed := 0
	scanned := make(map[string]bool) // two projects on one checkout are scanned once
	for key := range cfg.Projects {
		proj := cfg.Projects[key]
		top := cfg.ExecPath(proj)
		if top == "" || !isDir(top) {
			continue
		}
		for _, root := range s.tempCheckoutRoots(ctx, key, top, scanned) {
			removed += s.sweepProjectTempCheckouts(ctx, key, root, proj)
		}
	}
	return removed
}

// tempCheckoutRoots returns the repository tops to scan for one project: the project
// checkout itself, then every NESTED repository its jobs ran in.
//
// A `ref:` / `--worktree` checkout is created at the job's nearest git toplevel
// (prepareTempWorktree), NOT at the project root: a project that carries other
// repositories (inhere-tools/gofer inside my-tools-dev) leaves its leftovers deeper than
// a scan of `<project root>/<worktree.dir parent>` can see — gofer-l0zi, where nine
// checkouts under the nested repo survived the gofer-bgkk fix. The repositories to scan
// are taken from what the job rows RECORDED (`Cwd`, see ListJobDirs) and not from a walk
// of the project tree, which can be arbitrarily large.
//
// scanned carries the tops already handled (including by another project) so a directory
// is never swept twice.
func (s *Service) tempCheckoutRoots(ctx context.Context, projectKey, top string, scanned map[string]bool) []string {
	roots := make([]string, 0, 2)
	add := func(dir string) {
		if dir == "" || !isDir(dir) {
			return
		}
		key := util.RealPath(dir)
		if scanned[key] {
			return
		}
		scanned[key] = true
		roots = append(roots, dir)
	}
	add(top)
	for _, dir := range s.jobDirs(projectKey, top) {
		add(repoTopOf(ctx, dir))
	}
	return roots
}

// jobDirs returns the working directories this project's jobs recorded. A relative one
// (a row written before the cwd was resolved, or by hand) is read against the project
// checkout, which is what it was relative to.
func (s *Service) jobDirs(projectKey, projectRoot string) []string {
	dirs, err := s.meta.ListJobDirs(projectKey, tempCheckoutSweepDirs)
	if err != nil {
		slog.Warn("job.temp_checkout_sweep_dirs_failed", "event", "job.temp_checkout_sweep_dirs_failed",
			"component", "job", "project", projectKey, "err", err)
		return nil
	}
	for i, dir := range dirs {
		if !filepath.IsAbs(dir) {
			dirs[i] = filepath.Join(projectRoot, filepath.FromSlash(dir))
		}
	}
	return dirs
}

// repoTopOf returns the MAIN checkout of the repository that contains dir — the directory
// `git worktree add` was run in when a job created a checkout there. It answers for a job
// cwd that was itself a `ref:` checkout (a linked worktree): git lists the main worktree
// first, so the throwaway checkout resolves to the repository it was cut from instead of
// to itself. "" when dir is gone or no longer inside a git checkout.
func repoTopOf(ctx context.Context, dir string) string {
	out, err := gitOut(ctx, dir, "worktree", "list", "--porcelain")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		path, ok := strings.CutPrefix(strings.TrimSpace(line), "worktree ")
		if ok && strings.TrimSpace(path) != "" {
			return filepath.Clean(strings.TrimSpace(path))
		}
	}
	return ""
}

// sweepProjectTempCheckouts is the per-project scan: list the project's worktree dir and
// remove every entry that names a job which no longer owns a checkout.
func (s *Service) sweepProjectTempCheckouts(ctx context.Context, projectKey, top string, proj config.ProjectConfig) int {
	parent, nameRe := tempCheckoutScan(proj)
	if parent == "" || nameRe == nil {
		return 0
	}
	root := filepath.Join(top, filepath.FromSlash(parent))
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0 // no checkouts in this project (the common case)
	}
	removed := 0
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		id, ok := tempCheckoutJobID(nameRe, ent.Name())
		if !ok || s.jobActive(id) {
			continue
		}
		path := filepath.Join(root, ent.Name())
		if branch, isRepo := checkoutBranch(ctx, path); isRepo && branch != "" {
			// A branch checkout is a deliverable (--worktree) or somebody's own worktree.
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, worktreeTimeout)
		rmErr := removeTempCheckout(cctx, &worktreeRef{Path: path, Top: top, Temp: true})
		cancel()
		if rmErr != nil {
			slog.Warn("job.temp_checkout_sweep_failed", "event", "job.temp_checkout_sweep_failed",
				"component", "job", "project", projectKey, "job_id", id, "path", path, "err", rmErr)
			continue
		}
		removed++
		slog.Info("job.temp_checkout_swept", "event", "job.temp_checkout_swept", "component", "job",
			"project", projectKey, "job_id", id, "path", path, "reason", "stale temporary ref checkout")
		// The row (when it still exists) records that the checkout outlived its job and
		// this sweep completed the removal the finish path could not.
		if rec, found, gerr := s.meta.GetJob(id); gerr == nil && found && isFinished(rec.Status) {
			s.recordEvent(id, EventJobTempCheckoutSwept, map[string]any{
				"path": path, "project": projectKey,
			})
		}
	}
	return removed
}

// tempCheckoutScan turns a project's worktree.dir template into the directory to list plus
// a regexp matching ONE checkout dir name, whose every placeholder group captures the job
// id (a `ref:` job sets no worktree name, so {name} and {job_id} carry the same value).
// It returns "" / nil when the template cannot answer "which job is this dir?" — no
// placeholder at all, or a placeholder ABOVE the scanned directory — because such a
// template does not name one directory per job.
func tempCheckoutScan(proj config.ProjectConfig) (string, *regexp.Regexp) {
	tmpl := config.DefaultWorktreeDir
	if proj.Worktree != nil && strings.TrimSpace(proj.Worktree.Dir) != "" {
		tmpl = proj.Worktree.Dir
	}
	tmpl = strings.Trim(strings.ReplaceAll(strings.TrimSpace(tmpl), "\\", "/"), "/")
	first := templatePlaceholderIndex(tmpl)
	if first < 0 {
		return "", nil
	}
	slash := strings.LastIndex(tmpl[:first], "/")
	if slash < 0 {
		return "", nil // checkouts land in the repo top: too coarse to scan safely
	}
	parent, nameTmpl := tmpl[:slash], tmpl[slash+1:]
	if templatePlaceholderIndex(parent) >= 0 {
		return "", nil // the parent itself varies per job: not one dir to list
	}
	if strings.HasPrefix(parent, "..") {
		return "", nil
	}
	re, err := regexp.Compile("^" + placeholderRegexp(nameTmpl) + "$")
	if err != nil {
		return "", nil
	}
	return parent, re
}

// placeholderRegexp turns a template segment into a regexp that captures every placeholder
// value (literal text quoted, each {name} / {job_id} one capture group).
func placeholderRegexp(seg string) string {
	var b strings.Builder
	for i := 0; i < len(seg); {
		if n := placeholderLen(seg[i:]); n > 0 {
			b.WriteString(`([^/\\]+)`)
			i += n
			continue
		}
		j := i
		for j < len(seg) && placeholderLen(seg[j:]) == 0 {
			j++
		}
		b.WriteString(regexp.QuoteMeta(seg[i:j]))
		i = j
	}
	return b.String()
}

// placeholderLen returns the length of the template placeholder at the start of s, or 0.
func placeholderLen(s string) int {
	switch {
	case strings.HasPrefix(s, worktreeNameTmpl):
		return len(worktreeNameTmpl)
	case strings.HasPrefix(s, worktreeJobIDTmpl):
		return len(worktreeJobIDTmpl)
	}
	return 0
}

// templatePlaceholderIndex returns the offset of the first placeholder in tmpl, or -1.
func templatePlaceholderIndex(tmpl string) int {
	first := -1
	for _, ph := range []string{worktreeNameTmpl, worktreeJobIDTmpl} {
		if i := strings.Index(tmpl, ph); i >= 0 && (first < 0 || i < first) {
			first = i
		}
	}
	return first
}

// tempCheckoutJobID reads the job id back out of a checkout dir name. Every placeholder
// group must hold the same value, which is what the renderer wrote for a `ref:` job.
func tempCheckoutJobID(nameRe *regexp.Regexp, name string) (string, bool) {
	m := nameRe.FindStringSubmatch(name)
	if m == nil {
		return "", false
	}
	id := ""
	for _, g := range m[1:] {
		if id == "" {
			id = g
			continue
		}
		if g != id {
			return "", false
		}
	}
	return id, id != ""
}

// checkoutBranch reports the branch checked out at path and whether path is a git checkout
// OF ITS OWN. A temporary `ref:` checkout is DETACHED (empty branch) — that is how it is
// told apart from a delivered `--worktree` branch, which must never be swept. A plain
// directory inside the enclosing checkout is NOT its own checkout (git walks up and would
// report the MAIN branch for it), so it reports false and is swept as a leftover.
func checkoutBranch(ctx context.Context, path string) (string, bool) {
	top, err := gitOut(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil || util.RealPath(top) != util.RealPath(path) {
		return "", false
	}
	out, err := gitOut(ctx, path, "branch", "--show-current")
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(out), true
}

// jobActive reports whether id names a job that may still be using a checkout: an
// in-process entry that has not finished, or a stored row in a non-terminal state. A
// stored row in `recovering` counts as active too — its worker may still be running it.
func (s *Service) jobActive(id string) bool {
	if entry := s.entry(id); entry != nil {
		return !IsFinished(entry.snapshot().Status)
	}
	if rec, ok, err := s.meta.GetJob(id); err == nil && ok {
		return !IsFinished(rec.Status)
	}
	return false
}

// SweepStaleTempCheckouts must run BEFORE the orphan reconcile's jobs resume: a stale
// directory whose job row is still non-terminal is kept, so nothing a live job owns is
// ever deleted.
