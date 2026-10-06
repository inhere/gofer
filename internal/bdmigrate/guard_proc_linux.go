//go:build linux

package bdmigrate

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// findBdProcesses scans /proc for bd / dolt processes whose working directory
// is root or below.
func findBdProcesses(root string) []string {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	absRoot, _ := filepath.Abs(root)
	var out []string
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		cmdline, err := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if err != nil || len(cmdline) == 0 {
			continue
		}
		argv0 := strings.SplitN(string(cmdline), "\x00", 2)[0]
		switch filepath.Base(argv0) {
		case "bd", "dolt":
		default:
			continue
		}
		cwd, err := os.Readlink("/proc/" + e.Name() + "/cwd")
		if err != nil {
			continue
		}
		if cwd == absRoot || strings.HasPrefix(cwd, absRoot+string(filepath.Separator)) {
			out = append(out, fmt.Sprintf("%s process pid %d is running in %s", filepath.Base(argv0), pid, cwd))
		}
	}
	return out
}
