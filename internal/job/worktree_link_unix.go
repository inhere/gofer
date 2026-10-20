//go:build !windows

package job

import "os"

// createDirLink links src (a directory in the main checkout) into dst (a path inside
// the worktree) with a symlink. Everywhere but Windows that needs no privilege, and
// git, build tools and the shell all follow it.
func createDirLink(src, dst string) error {
	return os.Symlink(src, dst)
}

// isDirLink reports whether path is a link (a symlink is the only directory link
// outside Windows) that can be removed without touching its target.
func isDirLink(fi os.FileInfo, _ string) bool {
	return fi.Mode()&os.ModeSymlink != 0
}
