package job

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// checkoutOwnerFile is the file, inside a temporary checkout's git admin dir (never in the
// working tree, so it cannot dirty it), naming the service that created the checkout.
const checkoutOwnerFile = "gofer-owner"

// checkoutOwner names this service for checkout ownership: the instance id of its job
// database. "" when it has no database or the id cannot be read — such a service neither
// stamps nor defers to a stamp, which is the behavior from before checkouts were stamped.
func (s *Service) checkoutOwner() string {
	s.ownerOnce.Do(func() {
		if s.meta == nil {
			return
		}
		id, err := s.meta.InstanceID()
		if err != nil {
			slog.Warn("job.checkout_owner_unavailable", "event", "job.checkout_owner_unavailable", "component", "job", "err", err)
			return
		}
		s.owner = id
	})
	return s.owner
}

// stampCheckoutOwner records who created the temporary checkout at path. Best-effort: an
// unstamped checkout is merely treated like a legacy one by the sweep.
func (s *Service) stampCheckoutOwner(ctx context.Context, path string) {
	owner := s.checkoutOwner()
	if owner == "" {
		return
	}
	dir, err := gitOut(ctx, path, "rev-parse", "--absolute-git-dir")
	if err == nil {
		err = os.WriteFile(filepath.Join(strings.TrimSpace(dir), checkoutOwnerFile), []byte(owner+"\n"), 0o644)
	}
	if err != nil {
		slog.Warn("job.checkout_owner_stamp_failed", "event", "job.checkout_owner_stamp_failed", "component", "job",
			"path", path, "err", err)
	}
}

// foreignCheckout reports whether the checkout at path was created by ANOTHER service. A
// hub and a worker can share one repository on disk (a docker worker bind-mounting the
// hub's checkout) while each keeps its own job database, so "no row for this id here" says
// nothing about whether the other service's job is still running in it (gofer-0eks). An
// unstamped checkout (made before stamping, or by hand) is not foreign.
//
// top is the repository the checkout was cut from. The stamp is found through git when it
// can resolve the checkout, else through the checkout's `.git` file: a checkout made in a
// container records `gitdir: /d/...`, which git on the Windows host cannot follow, but the
// admin dir's name is still the last element of that path under top's `.git/worktrees/`.
func (s *Service) foreignCheckout(ctx context.Context, top, path string) bool {
	me := s.checkoutOwner()
	if me == "" {
		return false
	}
	owner := ""
	if dir, err := gitOut(ctx, path, "rev-parse", "--absolute-git-dir"); err == nil {
		owner = readCheckoutOwner(strings.TrimSpace(dir))
	}
	if owner == "" && top != "" {
		if name := checkoutAdminName(path); name != "" {
			owner = readCheckoutOwner(filepath.Join(top, ".git", "worktrees", name))
		}
	}
	return owner != "" && owner != me
}

// readCheckoutOwner reads the owner stamp in a checkout's git admin dir ("" when absent).
func readCheckoutOwner(adminDir string) string {
	b, err := os.ReadFile(filepath.Join(adminDir, checkoutOwnerFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// checkoutAdminName is the admin dir name a linked checkout's `.git` file points at
// (`gitdir: <common>/.git/worktrees/<name>`), read without git so a path written on
// another OS still yields it. "" when path has no such file.
func checkoutAdminName(path string) string {
	b, err := os.ReadFile(filepath.Join(path, ".git"))
	if err != nil {
		return ""
	}
	gitdir, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
	if !ok {
		return ""
	}
	gitdir = strings.TrimRight(strings.ReplaceAll(strings.TrimSpace(gitdir), "\\", "/"), "/")
	i := strings.LastIndex(gitdir, "/")
	if i < 0 || !strings.HasSuffix(gitdir[:i], "/worktrees") {
		return ""
	}
	return gitdir[i+1:]
}
