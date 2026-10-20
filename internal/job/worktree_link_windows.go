//go:build windows

package job

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/sys/windows"

	"github.com/inhere/gofer/internal/procattr"
)

// createDirLink links src (a directory in the main checkout) into dst (a path inside
// the worktree) on Windows.
//
// It uses a DIRECTORY JUNCTION (`mklink /J`), not a symlink: creating a directory
// symlink needs SeCreateSymbolicLinkPrivilege (administrator or developer mode), which
// an unattended agent job does not have, while a junction needs no privilege at all and
// is what Windows tools treat as an ordinary directory.
func createDirLink(src, dst string) error {
	cmd := exec.Command("cmd", "/c", "mklink", "/J", dst, src)
	procattr.Background(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("mklink /J %s: %s", dst, msg)
	}
	return nil
}

// isDirLink reports whether path is a link that can be removed WITHOUT touching what it
// points at. Go's os.Lstat does not flag a directory junction as ModeSymlink on Windows
// (it is a reparse point, reported as ModeIrregular), so the reparse-point attribute is
// what decides here — a plain directory must never be deleted by the unlink step.
func isDirLink(fi os.FileInfo, path string) bool {
	if fi.Mode()&os.ModeSymlink != 0 {
		return true
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	attrs, err := windows.GetFileAttributes(p)
	if err != nil {
		return false
	}
	return attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
