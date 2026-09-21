package xfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
)

// LocalRunner is the `runner=local` half of XFER-01: it moves the payload on the
// SERVER's own machine (the canonical `local` spelling, also written `server`), with
// no worker and no wire involved.
//
// It is a thin Runner over the staging Store, deliberately narrow: it knows how to
// resolve a project root and how to copy bytes, and nothing about the journal. The
// journal is settled by Manager.Deliver from Run's outcome — and, for a get, from the
// digest this runner reports through OnGetContent (Run cannot return the size/sha a get
// produced, and the alternative — handing this type the journal — would put the
// transfer's state machine in two places).
//
// Every path decision is re-read from Config() PER CALL: the server hot-reloads its
// config (SIGHUP / a web edit), so a cached ProjectConfig would let a transfer land in
// a project root the operator has already moved.
type LocalRunner struct {
	// Config returns the config in force NOW (nil means "not assembled").
	Config func() *config.Config
	// Store is the staging area the payload passes through: the source of a put, the
	// destination of a get.
	Store *Store
	// OnGetContent is called with the size + sha256 of the payload a get just staged,
	// and its error FAILS the transfer. The assembly points it at the manager's
	// CommitGet (which makes the staged payload visible and records the digest); a
	// failure there must not be swallowed, or the transfer would be reported done with
	// nothing to download.
	OnGetContent func(id string, size int64, sha256 string) error
}

// Run implements Runner: it resolves the record's target on this machine and moves the
// bytes. ErrExists and ErrNotFound are the two sentinel outcomes Manager.Deliver maps
// onto the operator-facing reason ("exists" / "source not found").
func (r *LocalRunner) Run(ctx context.Context, rec jobstore.XferRecord) error {
	target, err := r.resolve(rec.ProjectKey, rec.Path)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	switch Op(rec.Op) {
	case OpPut:
		return r.runPut(rec, target)
	case OpGet:
		return r.runGet(rec, target)
	default:
		return fmt.Errorf("unknown transfer op %q", rec.Op)
	}
}

// resolve maps a project-relative path onto the server's own filesystem, refusing
// anything that escapes the project's execution root. It is the SAME boundary a job's
// `--cwd` obeys (design §一.1), so a transfer can never reach outside what a job could.
func (r *LocalRunner) resolve(projectKey, path string) (string, error) {
	cfg := r.cfg()
	if cfg == nil {
		return "", errors.New("xfer: server config is not available")
	}
	proj, ok := cfg.Projects[projectKey]
	if !ok {
		return "", fmt.Errorf("unknown project %q", projectKey)
	}
	root := cfg.ExecPath(proj)
	if root == "" {
		return "", fmt.Errorf("project %q has no execution path", projectKey)
	}
	target, err := project.SafeJoin(root, path)
	if err != nil {
		return "", fmt.Errorf("path escapes project: %w", err)
	}
	return target, nil
}

func (r *LocalRunner) cfg() *config.Config {
	if r.Config == nil {
		return nil
	}
	return r.Config()
}

// runPut copies the staged payload onto the destination, temp file first so a
// half-written file is never visible where the project's tools read it.
func (r *LocalRunner) runPut(rec jobstore.XferRecord, dst string) error {
	if rec.Force == 0 {
		if _, err := os.Stat(dst); err == nil {
			return ErrExists
		}
	}
	if r.Store == nil {
		return errors.New("xfer: local runner has no staging store")
	}
	src, err := r.Store.Reader(rec.ID)
	if err != nil {
		return err
	}
	defer src.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}
	tmp := dst + ".gofer-tmp-" + rec.ID
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("open temp file: %w", err)
	}
	h := sha256.New()
	n, cerr := io.Copy(io.MultiWriter(f, h), src)
	closeErr := f.Close()
	if cerr != nil || closeErr != nil {
		reason := cerr
		if reason == nil {
			reason = closeErr
		}
		_ = os.Remove(tmp)
		return fmt.Errorf("write destination: %w", reason)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	// Verify what actually landed. The staged payload was already checked against the
	// client's declaration at upload time; this catches the remaining case — the
	// staging file no longer being the bytes the journal describes.
	if rec.Size > 0 && n != rec.Size {
		_ = os.Remove(tmp)
		return fmt.Errorf("size mismatch: copied %d bytes, staged %d", n, rec.Size)
	}
	if rec.SHA256 != "" && !strings.EqualFold(rec.SHA256, sum) {
		_ = os.Remove(tmp)
		return fmt.Errorf("sha256 mismatch: copied %s, staged %s", sum, rec.SHA256)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("move payload into place: %w", err)
	}
	return nil
}

// runGet copies the source INTO the staging area so the client can download it, and
// reports the size + sha256 it read.
func (r *LocalRunner) runGet(rec jobstore.XferRecord, src string) error {
	if r.Store == nil {
		return errors.New("xfer: local runner has no staging store")
	}
	f, err := os.Open(src)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return fmt.Errorf("open source: %w", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat source: %w", err)
	}
	if st.IsDir() {
		return errors.New("source is a directory; XFER-01 transfers a single file")
	}
	w, err := r.Store.Writer(rec.ID)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, cerr := io.Copy(io.MultiWriter(w, h), f)
	closeErr := w.Close()
	if cerr != nil || closeErr != nil {
		reason := cerr
		if reason == nil {
			reason = closeErr
		}
		return fmt.Errorf("stage source: %w", reason)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if r.OnGetContent != nil {
		// CommitGet finalizes (temp → data) and records the digest; a failure here
		// fails the transfer instead of marking it done without a payload.
		return r.OnGetContent(rec.ID, n, sum)
	}
	// No journal hook wired: make the payload visible ourselves so a caller reading the
	// staging area directly (a standalone runner, a test) gets a usable transfer.
	return r.Store.Finalize(rec.ID, n)
}
