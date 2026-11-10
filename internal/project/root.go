package project

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// root.go holds the os.Root based file helpers every "read/write a project-relative
// file on behalf of a caller" path shares (transfer get/put on the server and on a
// worker, job attachment placement). SafeJoin stays the literal pre-check (".." /
// absolute paths, early errors); these helpers are the symlink boundary: os.Root
// follows links but refuses any that leave the root, and it does so one directory
// handle at a time, so there is no check-then-open race like SafeJoinReal has.
//
// Limits inherited from os.Root: a link whose target is an absolute path is refused
// even when it points inside the root (on Windows that includes every directory
// junction). Errors carry only the relative name, never the resolved path.

// tmpMarker joins a destination name and its temp-file suffix.
const tmpMarker = ".gofer-tmp-"

// OpenInRoot opens the file rel (slash or OS separators, relative to root) for
// reading, refusing any link on the way that escapes root. A missing file keeps
// the os.IsNotExist semantics; an escaping link is a different error.
func OpenInRoot(root, rel string) (*os.File, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	return r.Open(filepath.FromSlash(rel))
}

// ExistsInRoot reports whether something (file, directory or any link, even a
// dangling or escaping one) exists at rel, using Lstat so the link itself is
// judged. A missing path (or a missing parent) is false, nil; an intermediate
// directory that escapes root is an error, not "absent".
func ExistsInRoot(root, rel string) (bool, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil // no root yet: nothing can exist under it
		}
		return false, err
	}
	defer func() { _ = r.Close() }()
	if _, err := r.Lstat(filepath.FromSlash(rel)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Placement is a destination being written inside a root: the caller writes the
// payload to File() (and does its own size / sha256 checks), then Commit()s to
// move it into place or Abort()s to drop it. A half-written file is therefore
// never visible under the destination name.
type Placement struct {
	root *os.Root
	rel  string
	tmp  string
	f    *os.File
	done bool
}

// PlaceInRoot creates the parent directories of rel inside root and opens the temp
// file rel+".gofer-tmp-"+tmpSuffix next to the destination (same directory, so the
// final Rename stays on one filesystem and is atomic). Errors are wrapped as
// "create destination directory: …" / "open temp file: …" (the callers' historic
// wording) and keep the os.Root error underneath. Any link on the way that
// escapes root fails the call and leaves nothing outside root.
func PlaceInRoot(root, rel, tmpSuffix string) (*Placement, error) {
	// The root itself may not exist yet (a machine's own attachment dir is created by
	// its first placement); the old MkdirAll over the full path created it too.
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create destination directory: %w", err)
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	rel = filepath.FromSlash(rel)
	if err := r.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
		_ = r.Close()
		return nil, fmt.Errorf("create destination directory: %w", err)
	}
	tmp := rel + tmpMarker + tmpSuffix
	f, err := r.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		_ = r.Close()
		return nil, fmt.Errorf("open temp file: %w", err)
	}
	return &Placement{root: r, rel: rel, tmp: tmp, f: f}, nil
}

// File is the open temp file the caller writes the payload to. Commit and Abort
// close it; do not close it yourself.
func (p *Placement) File() *os.File { return p.f }

// Commit closes the temp file and renames it onto the destination (replacing an
// existing file or link at that name). On any failure the temp file is removed.
// Commit and Abort are mutually final: the first call wins.
func (p *Placement) Commit() error {
	if p.done {
		return errors.New("placement already finished")
	}
	p.done = true
	defer func() { _ = p.root.Close() }()
	if err := p.f.Close(); err != nil {
		_ = p.root.Remove(p.tmp)
		return err
	}
	if err := p.root.Rename(p.tmp, p.rel); err != nil {
		_ = p.root.Remove(p.tmp)
		return err
	}
	return nil
}

// Abort closes the temp file and removes it. Safe to call repeatedly, and after
// Commit (then it does nothing).
func (p *Placement) Abort() {
	if p.done {
		return
	}
	p.done = true
	_ = p.f.Close()
	_ = p.root.Remove(p.tmp)
	_ = p.root.Close()
}
