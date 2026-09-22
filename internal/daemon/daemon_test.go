package daemon

import (
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDaemonized(t *testing.T) {
	t.Setenv(EnvSentinel, "")
	if Daemonized() {
		t.Fatal("Daemonized() should be false when sentinel unset")
	}
	t.Setenv(EnvSentinel, "1")
	if !Daemonized() {
		t.Fatal("Daemonized() should be true when sentinel=1")
	}
}

func TestPIDFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "serve.pid")
	if err := WritePIDFile(path, 4242); err != nil {
		t.Fatalf("WritePIDFile: %v", err)
	}
	got, err := ReadPIDFile(path)
	if err != nil {
		t.Fatalf("ReadPIDFile: %v", err)
	}
	if got != 4242 {
		t.Fatalf("pid = %d, want 4242", got)
	}
	RemovePIDFile(path)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("pidfile should be gone after RemovePIDFile, stat err=%v", err)
	}
}

func TestReadPIDFileMissing(t *testing.T) {
	if _, err := ReadPIDFile(filepath.Join(t.TempDir(), "nope.pid")); err == nil {
		t.Fatal("expected error reading a missing pidfile")
	}
}

func TestReadPIDFileInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.pid")
	if err := os.WriteFile(path, []byte("not-a-number"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := ReadPIDFile(path); err == nil {
		t.Fatal("expected error parsing a non-numeric pidfile")
	}
}

// TestPIDAlive: the running test process is alive on every platform (Windows
// included — the liveness check is a real implementation there, not a stub), a
// process that has exited and been reaped is not, and pid 0 is never alive.
func TestPIDAlive(t *testing.T) {
	if !PIDAlive(os.Getpid()) {
		t.Fatalf("PIDAlive(self=%d) should be true", os.Getpid())
	}
	if PIDAlive(0) {
		t.Fatal("PIDAlive(0) should be false")
	}
	if pid := deadChildPid(t); PIDAlive(pid) {
		t.Fatalf("PIDAlive(%d) for an exited child should be false", pid)
	}
}

// deadChildPid starts a short-lived child, reaps it and returns its (now dead)
// pid — the "a process that exited" fixture PIDAlive must report as not alive.
func deadChildPid(t *testing.T) int {
	t.Helper()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/c", "exit 0")
	} else {
		cmd = exec.Command("true")
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start short-lived child: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait short-lived child: %v", err)
	}
	return pid
}

// TestClaimOwnsAndReleasesOnlyOwnPID: Claim records our pid and only releases a
// pidfile that still holds our pid — a file another LIVE process owns is neither
// taken over nor deleted.
func TestClaimOwnsAndReleasesOnlyOwnPID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claim.pid")

	release, owned := Claim(path)
	if !owned {
		t.Fatal("Claim on an absent pidfile must be owned")
	}
	got, err := ReadPIDFile(path)
	if err != nil {
		t.Fatalf("ReadPIDFile after Claim: %v", err)
	}
	if got != os.Getpid() {
		t.Fatalf("pidfile holds %d, want our pid %d", got, os.Getpid())
	}
	// Our own pid is not "another live process": a second Claim (the -d child
	// re-claiming the pidfile its parent already wrote) still owns the file.
	if _, owned := Claim(path); !owned {
		t.Fatal("Claim must be idempotent for our own pid")
	}

	// Hand the file to a live process that is not us: release must not delete it.
	parent := os.Getppid()
	if err := WritePIDFile(path, parent); err != nil {
		t.Fatalf("rewrite pidfile: %v", err)
	}
	release()
	got, err = ReadPIDFile(path)
	if err != nil {
		t.Fatalf("pidfile must survive a release by a non-owner: %v", err)
	}
	if got != parent {
		t.Fatalf("pidfile holds %d, want the parent pid %d", got, parent)
	}

	otherRelease, owned := Claim(path)
	if owned {
		t.Fatal("Claim must not take over a pidfile owned by another live process")
	}
	otherRelease()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("a non-owner release must not delete the pidfile: %v", err)
	}
	if got, err := ReadPIDFile(path); err != nil || got != parent {
		t.Fatalf("pidfile changed by a non-owner claim: pid=%d err=%v", got, err)
	}
}

// TestTerminateDeliversToSelf: Terminate reaches NotifyStop's channel, so a
// graceful stop works on every platform (real SIGTERM on unix, the named stop
// event on Windows).
func TestTerminateDeliversToSelf(t *testing.T) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM)
	defer signal.Stop(ch)
	NotifyStop(ch)

	if err := Terminate(os.Getpid()); err != nil {
		t.Fatalf("Terminate(self): %v", err)
	}
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("Terminate(self) delivered nothing to NotifyStop within 2s")
	}
}

// TestSpawnDetachedRoundTrip drives the whole detach contract end to end: Spawn
// re-execs THIS test binary as the helper child (TestHelperDaemonChild), which
// records its readiness in the sidecar log; the parent then sees it alive,
// recorded in the pidfile, stops it through Terminate and watches it exit.
func TestSpawnDetachedRoundTrip(t *testing.T) {
	saved := os.Args
	defer func() { os.Args = saved }()
	os.Args = []string{os.Args[0], "-test.run=^TestHelperDaemonChild$"}

	dir := t.TempDir()
	pidPath := filepath.Join(dir, "t.pid")
	logPath := filepath.Join(dir, "t.out.log")

	pid, err := Spawn(Options{Name: "t", PIDPath: pidPath, LogPath: logPath})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if pid <= 0 {
		t.Fatalf("Spawn returned pid=%d", pid)
	}
	// Whatever the assertions below do, do not leave a detached child behind.
	defer func() { _ = Terminate(pid) }()

	waitForLogText(t, logPath, "child-ready", 10*time.Second)
	if !PIDAlive(pid) {
		t.Fatalf("detached child pid=%d should be alive once it logged child-ready", pid)
	}
	got, err := ReadPIDFile(pidPath)
	if err != nil {
		t.Fatalf("ReadPIDFile: %v", err)
	}
	if got != pid {
		t.Fatalf("pidfile holds %d, want the spawned pid %d", got, pid)
	}

	if err := Terminate(pid); err != nil {
		t.Fatalf("Terminate(%d): %v", pid, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && PIDAlive(pid) {
		time.Sleep(50 * time.Millisecond)
	}
	if PIDAlive(pid) {
		t.Fatalf("detached child pid=%d still alive 5s after Terminate", pid)
	}
	waitForLogText(t, logPath, "child-stopped", 5*time.Second)
}

// TestHelperDaemonChild is the detached child of TestSpawnDetachedRoundTrip: it
// only runs when re-executed with the EnvSentinel (otherwise it is skipped), and
// exits on the stop signal the parent sends.
func TestHelperDaemonChild(t *testing.T) {
	if !Daemonized() {
		t.Skip("helper for TestSpawnDetachedRoundTrip; runs only in the detached child")
	}
	os.Stdout.WriteString("child-ready\n")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sig)
	NotifyStop(sig)
	select {
	case <-sig:
	case <-time.After(30 * time.Second):
		t.Fatal("helper daemon child: no stop signal within 30s")
	}
	os.Stdout.WriteString("child-stopped\n")
}

// waitForLogText polls path until it contains want, failing after timeout.
func waitForLogText(t *testing.T, path, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		b, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(b), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("log %s did not contain %q within %s (err=%v, content=%q)", path, want, timeout, err, string(b))
		}
		time.Sleep(50 * time.Millisecond)
	}
}
