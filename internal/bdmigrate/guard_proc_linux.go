//go:build linux

package bdmigrate

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// startedByGofer reports whether the process carries the marker gofer puts in
// every bd child it spawns (the marker is inherited by grandchildren).
func startedByGofer(pid string) bool {
	env, err := os.ReadFile("/proc/" + pid + "/environ")
	return err == nil && bytes.Contains(env, []byte("\x00"+childMarker+"\x00"))
}

// settleChildren waits (bounded) for bd descendants gofer itself started in root
// to exit, so a dry-run's export leaves nothing behind for the next command.
func settleChildren(root string, limit time.Duration) {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if len(markedProcesses(root)) == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func markedProcesses(root string) []string { return scanBdProcesses(root, true) }

// findBdProcesses scans /proc for bd / dolt processes whose working directory
// is root or below.
func findBdProcesses(root string) []string { return scanBdProcesses(root, false) }

// scanBdProcesses lists bd / dolt processes under root. onlyMarked=false skips
// the ones gofer started itself; onlyMarked=true lists exactly those.
func scanBdProcesses(root string, onlyMarked bool) []string {
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
		if startedByGofer(e.Name()) != onlyMarked {
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
