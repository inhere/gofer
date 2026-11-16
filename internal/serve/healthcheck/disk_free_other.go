//go:build !windows

package healthcheck

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// freeSpace reads the free and total bytes of the filesystem holding dir with statfs(2).
// Bavail (space available to an unprivileged user) is the number that matters: a volume
// whose free blocks are all reserved would still fail to take a write.
func freeSpace(dir string) (total, free uint64, err error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, 0, fmt.Errorf("disk probe: %s: %w", dir, err)
	}
	bsize := uint64(st.Bsize)
	return st.Blocks * bsize, st.Bavail * bsize, nil
}
