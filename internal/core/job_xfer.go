package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/xfer"
)

// job_xfer.go is XFER-01 X2's hub side of the job file seam: it is the ONE place
// that knows both the job package's contract (job.XferBridge) and the transfer
// manager, exactly like buildXferManager does for the wire adapter (G022 — neither
// package imports the other).
//
//   - FetchUpload materializes a staged upload in the job's cwd on THIS machine
//     (the hub IS the executing machine for a local job). No HTTP, no wire: the
//     payload is already in the staging area.
//   - PullCollected fetches a file the WORKER collected, through X1's existing pull
//     path (StageGetForJob → Deliver → the worker uploads the bytes), and hands the
//     bytes to the job's artifact directory.
type hubJobXfer struct {
	mgr *xfer.Manager
}

// hubJobXferFor builds the hub's job file seam over an assembled transfer manager.
func hubJobXferFor(mgr *xfer.Manager) job.XferBridge { return hubJobXfer{mgr: mgr} }

// FetchUpload copies a staged put to rel under root (the job's workDir or result dir; the
// job package already vetted rel with SafeJoin). The write goes through os.Root, so a
// link inside root cannot lead the upload out of it. The record must be a put of the job's own
// project in a staged state: those are the same conditions the HTTP submit validated,
// re-checked here because this path does not go through HTTP.
func (h hubJobXfer) FetchUpload(_ context.Context, xferID, projectKey, root, rel string) error {
	rec, ok, err := h.mgr.Get(xferID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("unknown transfer %s", xferID)
	}
	if rec.Op != string(xfer.OpPut) {
		return fmt.Errorf("transfer %s is a %s; a job upload needs a put", xferID, rec.Op)
	}
	if rec.ProjectKey != projectKey {
		return fmt.Errorf("transfer %s belongs to project %s", xferID, rec.ProjectKey)
	}
	if xfer.State(rec.State) != xfer.StateStaged {
		return fmt.Errorf("transfer %s is %s, not staged", xferID, rec.State)
	}
	src, err := h.mgr.Store().Reader(xferID)
	if err != nil {
		return err
	}
	defer src.Close()
	return placeVerified(src, root, rel, xferID, rec.Size, rec.SHA256)
}

// PullCollected stages a get for the collecting worker, drives it to done, copies the
// payload into the pull's destination and releases the staged copy. The transfer's own
// journal row (state done, job id recorded) stays for audit.
func (h hubJobXfer) PullCollected(ctx context.Context, p job.CollectedPull) (int64, error) {
	rec, err := h.mgr.StageGetForJob(p.JobID, p.Runner, p.ProjectKey, p.Name)
	if err != nil {
		return 0, err
	}
	if err := h.mgr.Deliver(ctx, rec.ID); err != nil {
		failed, _, _ := h.mgr.Get(rec.ID)
		if failed.Error != "" {
			return 0, fmt.Errorf("%s", failed.Error)
		}
		return 0, err
	}
	settled, _, err := h.mgr.Get(rec.ID)
	if err != nil {
		return 0, err
	}
	src, err := h.mgr.Store().Reader(rec.ID)
	if err != nil {
		return 0, err
	}
	defer src.Close()
	if err := writeVerified(src, p.Dst, settled.Size, settled.SHA256); err != nil {
		return 0, err
	}
	// The bytes now live in the job's artifacts; the staged second copy is released
	// (best-effort: a leftover directory is swept by the TTL).
	_ = h.mgr.Release(rec.ID)
	return settled.Size, nil
}

// CollectLimits are this deployment's server.xfer caps: per file (MaxBytes) and per
// job (CollectMaxBytes).
func (h hubJobXfer) CollectLimits() job.CollectLimits {
	lim := h.mgr.Limits()
	return job.CollectLimits{MaxFile: lim.MaxBytes, MaxTotal: lim.CollectMaxBytes}
}

// OwnsArtifacts is true: the hub owns the job row's result dir, so a collected file is
// copied straight into artifacts/collected/ (and a remote job's files are pulled back
// here after its outcome arrives).
func (h hubJobXfer) OwnsArtifacts() bool { return true }

// writeVerified streams src into dst (creating parents), hashing on the way, and only
// then renames it into place — a half-written file must never be visible where the
// job's tools read it. A size/sha the transfer journal declared is enforced against
// what actually landed.
func writeVerified(src io.Reader, dst string, size int64, sha string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}
	tmp := dst + ".gofer-part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), src)
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil {
		reason := copyErr
		if reason == nil {
			reason = closeErr
		}
		_ = os.Remove(tmp)
		return reason
	}
	if err := checkCopied(n, hex.EncodeToString(h.Sum(nil)), size, sha); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// placeVerified is writeVerified for a destination INSIDE root: the same copy-hash-verify
// -then-rename sequence, but the write goes through project.PlaceInRoot (os.Root), so the
// parent directories, the temp file and the final rename cannot follow a link out of root.
// The temp file sits next to the destination so a half-written file is never visible
// where the job's tools read it.
func placeVerified(src io.Reader, root, rel, tmpSuffix string, size int64, sha string) error {
	pl, err := project.PlaceInRoot(root, rel, tmpSuffix)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(pl.File(), h), src)
	if copyErr != nil {
		pl.Abort()
		return copyErr
	}
	if err := checkCopied(n, hex.EncodeToString(h.Sum(nil)), size, sha); err != nil {
		pl.Abort()
		return err
	}
	return pl.Commit()
}

// checkCopied enforces the size / sha256 the transfer journal declared (zero = not
// declared) against what actually landed.
func checkCopied(n int64, got string, size int64, sha string) error {
	if size > 0 && n != size {
		return fmt.Errorf("size mismatch: copied %d bytes, staged %d", n, size)
	}
	if sha != "" && !strings.EqualFold(sha, got) {
		return fmt.Errorf("sha256 mismatch: copied %s, staged %s", got, sha)
	}
	return nil
}
