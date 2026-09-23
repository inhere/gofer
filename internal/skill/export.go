package skill

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Export streams the skill as a zip archive: entry names relative to the skill dir,
// slash-separated, so `skill export` output re-imports to the same tree.
//
// Export owns the zip writer (it must Close it to emit the central directory) but
// never w and never Close()s it — the caller may be writing to a file, a buffer or
// a response body. On a walk error the writer is left under the caller's control
// with a partial archive on w: the caller discards it, exactly like any other
// failed stream.
func (s *Store) Export(name string, w io.Writer) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir, err := s.dirOf(name)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(w)
	if err := writeZipTree(zw, dir); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("skill: close archive for %q: %w", name, err)
	}
	return nil
}

// writeZipTree adds every file under dir to zw under its relative slash path.
func writeZipTree(zw *zip.Writer, dir string) error {
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return rerr
		}
		name := filepath.ToSlash(rel)
		info, ierr := d.Info()
		if ierr != nil {
			return fmt.Errorf("skill: stat %s: %w", name, ierr)
		}
		hdr, herr := zip.FileInfoHeader(info)
		if herr != nil {
			return fmt.Errorf("skill: archive header %s: %w", name, herr)
		}
		hdr.Name = name
		// The export carries the skill's modes, not whatever the store happened to
		// hold: 0644 keeps an exported-then-reimported skill byte-identical in every
		// way that matters (sha256 per file), and the exec bit can never ride along.
		hdr.SetMode(0o644)
		hdr.Method = zip.Deflate
		zfw, zerr := zw.CreateHeader(hdr)
		if zerr != nil {
			return fmt.Errorf("skill: archive %s: %w", name, zerr)
		}
		f, oerr := os.Open(p)
		if oerr != nil {
			return fmt.Errorf("skill: read %s: %w", name, oerr)
		}
		defer f.Close()
		if _, cerr := io.Copy(zfw, f); cerr != nil {
			return fmt.Errorf("skill: archive %s: %w", name, cerr)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("skill: export %s: %w", dir, err)
	}
	return nil
}

// Mount copies the skill's tree into dstDir (created, parents included) and returns
// the bytes written. This is how a job actually gets a skill (design §一.4): the
// caller points dstDir at the job's PRIVATE directory — <result_dir>/skills/<name>
// on the executing machine — so the project working tree never sees it, concurrent
// jobs never share it, and the files expire with the job's result dir.
//
// The destination must be disjoint from the skill dir: a dstDir inside the source
// would copy the tree into itself until the disk fills, and a dstDir that CONTAINS
// the source has the same effect from the other end. Both are refused rather than
// half-copied, which is also why an existing dstDir is written INTO (files
// overwritten, nothing pruned) instead of being replaced.
func (s *Store) Mount(name, dstDir string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	src, err := s.dirOf(name)
	if err != nil {
		return 0, err
	}
	dst, err := absPath(dstDir)
	if err != nil {
		return 0, err
	}
	if within(src, dst) || within(dst, src) {
		return 0, fmt.Errorf("%w: mount dir %s overlaps the skill dir %s", ErrInvalid, dstDir, src)
	}
	total, err := copyTree(dst, src, s.limits)
	if err != nil {
		return 0, fmt.Errorf("skill: mount %q into %s: %w", name, dstDir, err)
	}
	return total, nil
}
