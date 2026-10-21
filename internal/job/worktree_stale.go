package job

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
)

// SweepStaleTempCheckouts removes the temporary `ref:` checkouts a crashed or restarted
// server left behind (design 2026-10-21 §一·3). A job removes its own temp checkout when
// it ends, so anything still sitting in `<repo top>/tmp/gofer/wt/<job-id>` belongs either
// to a job that is STILL running (kept) or to a process that never got to finish
// (removed). The cleanup is the finish path's, step for step: unlink the dependency links
// first, sweep any leftover link, `git worktree remove --force`, then `git worktree
// prune` so the repository's worktree list drops the entry too.
//
// It walks every configured project's checkout as this process sees it (Config.ExecPath,
// G002) and returns how many checkouts it removed (serve logs the count).
func (s *Service) SweepStaleTempCheckouts(ctx context.Context) int {
	cfg := s.Config()
	if cfg == nil {
		return 0
	}
	removed := 0
	for key, proj := range cfg.Projects {
		top := cfg.ExecPath(proj)
		if top == "" {
			continue
		}
		root := filepath.Join(top, filepath.FromSlash(worktreeSubdir))
		entries, err := os.ReadDir(root)
		if err != nil {
			continue // no temp checkouts in this project (the common case)
		}
		for _, ent := range entries {
			if !ent.IsDir() {
				continue
			}
			id := ent.Name()
			if s.jobActive(id) {
				continue // a live job owns this checkout
			}
			path := filepath.Join(root, id)
			cctx, cancel := context.WithTimeout(ctx, worktreeTimeout)
			err := removeWorktree(cctx, &worktreeRef{Path: path, Top: top, Temp: true}, true, false)
			if err != nil {
				// A half-created checkout (a crash between mkdir and `worktree add`) is not a
				// registered worktree, so git refuses it. Unlink whatever hangs off it first
				// — never a recursive delete through a junction — then drop the directory.
				sweepWorktreeLinks(path, nil)
				if rerr := os.RemoveAll(path); rerr != nil {
					err = rerr
				} else {
					err = nil
				}
			}
			if err == nil {
				// The directory is gone; drop any dangling entry from the main checkout's
				// worktree list, as the finish path does after a removal.
				_, err = gitOut(cctx, top, "worktree", "prune")
			}
			cancel()
			if err != nil {
				slog.Warn("job.temp_checkout_sweep_failed", "event", "job.temp_checkout_sweep_failed",
					"component", "job", "project", key, "job_id", id, "path", path, "err", err)
				continue
			}
			removed++
			slog.Info("job.temp_checkout_swept", "event", "job.temp_checkout_swept", "component", "job",
				"project", key, "job_id", id, "path", path, "reason", "stale temporary ref checkout")
		}
	}
	return removed
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
