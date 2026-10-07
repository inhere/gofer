//go:build linux

package bdmigrate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeBd copies `sleep` to <dir>/bd so the process shows up as argv0 "bd".
func fakeBd(t *testing.T, dir string) string {
	t.Helper()
	sleepBin, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep binary")
	}
	data, err := os.ReadFile(sleepBin)
	if err != nil {
		t.Skip(err)
	}
	bd := filepath.Join(dir, "bd")
	if err := os.WriteFile(bd, data, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command(bd, "0").Run(); err != nil {
		t.Skip("copied sleep is not runnable: ", err)
	}
	return bd
}

func TestActivityGuardIgnoresGoferOwnBdProcesses(t *testing.T) {
	t.Chdir(t.TempDir())
	root := t.TempDir()
	bin := t.TempDir()
	bd := fakeBd(t, bin)

	// A bd someone else runs in the repo is still reported.
	foreign := exec.Command(bd, "30")
	foreign.Dir = root
	if err := foreign.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = foreign.Process.Kill(); _ = foreign.Wait() })

	// One gofer started (marker inherited) is not.
	own := exec.Command(bd, "30")
	own.Dir = root
	own.Env = append(os.Environ(), childMarker)
	if err := own.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = own.Process.Kill(); _ = own.Wait() })

	deadline := time.Now().Add(2 * time.Second)
	var got []string
	for time.Now().Before(deadline) {
		if got = findBdProcesses(root); len(got) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(got) != 1 || !strings.Contains(got[0], "pid") || strings.Contains(got[0], "pid "+itoa(own.Process.Pid)+" ") {
		t.Fatalf("want only the foreign bd, got %v", got)
	}
	if !strings.Contains(got[0], itoa(foreign.Process.Pid)) {
		t.Fatalf("foreign pid missing: %v", got)
	}
	if marked := markedProcesses(root); len(marked) != 1 || !strings.Contains(marked[0], itoa(own.Process.Pid)) {
		t.Fatalf("marked=%v", marked)
	}
}

// A bd that leaves a helper running after it exits (the dry-run export case):
// run must not return until the helper is gone, and the guard must not flag it.
func TestRunWaitsForBdHelperBeforeReturning(t *testing.T) {
	t.Chdir(t.TempDir())
	root := t.TempDir()
	bd := fakeBd(t, t.TempDir())
	script := filepath.Join(t.TempDir(), "bdwrap")
	body := "#!/bin/sh\n" + bd + " 1 >/dev/null 2>&1 &\necho '{}'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	r := &bdRunner{bin: script, root: root, timeout: 5 * time.Second}
	start := time.Now()
	if _, _, err := r.run(nil, "--readonly", "export"); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 500*time.Millisecond {
		t.Fatalf("run returned before the helper exited (%s)", time.Since(start))
	}
	if left := markedProcesses(root); len(left) != 0 {
		t.Fatalf("helper still running: %v", left)
	}
	if got := checkActivity(root, 5*time.Minute, time.Now()); len(got) != 0 {
		t.Fatalf("guard tripped right after run: %v", got)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
