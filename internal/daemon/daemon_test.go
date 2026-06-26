package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
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

// TestPIDAlive: the running test process is alive on unix; pid 0 is never alive.
// On Windows PIDAlive is a documented stub returning false, so the positive
// assertion is unix-only (tests run on linux in this project).
func TestPIDAlive(t *testing.T) {
	if runtime.GOOS != "windows" && !PIDAlive(os.Getpid()) {
		t.Fatalf("PIDAlive(self=%d) should be true on unix", os.Getpid())
	}
	if PIDAlive(0) {
		t.Fatal("PIDAlive(0) should be false")
	}
}
