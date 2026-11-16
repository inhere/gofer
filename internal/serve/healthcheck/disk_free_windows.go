//go:build windows

package healthcheck

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// freeSpace reads the free and total bytes of the volume holding dir with
// GetDiskFreeSpaceEx (the Win32 call for "how much room is left"), which is why the
// Windows build needs no shell-out and no extra dependency.
func freeSpace(dir string) (total, free uint64, err error) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, 0, fmt.Errorf("disk probe: %w", err)
	}
	var freeAvailable, totalBytes, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &freeAvailable, &totalBytes, &totalFree); err != nil {
		return 0, 0, fmt.Errorf("disk probe: %s: %w", dir, err)
	}
	return totalBytes, freeAvailable, nil
}
