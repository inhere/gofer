//go:build windows || linux

package servicemgr

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
	"github.com/inhere/gofer/internal/daemon"
)

func fixtureSpec(t *testing.T) (*Manager, Spec) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "配置 dir with spaces")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(root, "gofer-test")
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	identity, err := daemon.CurrentProcessIdentity()
	if err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(root, "config file.yaml")
	if err := os.WriteFile(configFile, []byte("server: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtimeDir, err := RuntimeDirFor(root, configFile)
	if err != nil {
		t.Fatal(err)
	}
	return m, Spec{
		SchemaVersion: SpecSchema, Backend: BackendWindowsTask, Name: m.Name,
		Owner: identity.Owner, Exe: exe, WorkDir: root,
		ConfigFile: configFile, ConfigDir: root, RuntimeDir: runtimeDir,
	}
}

func TestSpecRoundTripAndRejectsInvalidReplacement(t *testing.T) {
	m, spec := fixtureSpec(t)
	if err := spec.CheckFiles(); err != nil {
		t.Fatal(err)
	}
	if err := m.SaveSpec(spec); err != nil {
		t.Fatal(err)
	}
	loaded, err := m.LoadSpec()
	if err != nil || loaded != spec {
		t.Fatalf("LoadSpec = %+v, %v; want %+v", loaded, err, spec)
	}
	invalid := spec
	invalid.SchemaVersion++
	if err := m.SaveSpec(invalid); err == nil {
		t.Fatal("unknown schema was saved")
	}
	if loaded, err := m.LoadSpec(); err != nil || loaded != spec {
		t.Fatalf("failed replacement changed spec: %+v, %v", loaded, err)
	}
	spec.RegisteredVersion = "test-v2"
	if err := m.SaveSpec(spec); err != nil {
		t.Fatal(err)
	}
	loaded, err = m.LoadSpec()
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, spec, loaded)
	if err := os.WriteFile(m.SpecPath(), []byte(`{"schema_version":1,"name":"gofer-test"`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.LoadSpec(); err == nil {
		t.Fatal("half-written spec was accepted")
	}
}

func TestServerArgsAllowlist(t *testing.T) {
	_, spec := fixtureSpec(t)
	spec.Serve = ServeOptions{Addr: "127.0.0.1:18001", NoWeb: true, AllowEmptyToken: true}
	args, err := spec.ServerArgs()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"serve", "-c", spec.ConfigFile, "--addr", spec.Serve.Addr, "--no-web", "--allow-empty-token"}
	assert.Eq(t, want, args)
}

func TestSpecRejectsUnknownBackendRelativePathAndMissingConfig(t *testing.T) {
	_, spec := fixtureSpec(t)
	unknown := spec
	unknown.Backend = "other"
	if err := unknown.Validate(); err == nil {
		t.Fatal("unknown backend accepted")
	}
	relative := spec
	relative.RuntimeDir = "run"
	if err := relative.Validate(); err == nil {
		t.Fatal("relative runtime dir accepted")
	}
	missing := spec
	missing.ConfigFile = filepath.Join(spec.ConfigDir, "missing.yaml")
	if err := missing.CheckFiles(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing config not rejected: %v", err)
	}
}

func TestRuntimeDirForExplicitConfigOnly(t *testing.T) {
	m, spec := fixtureSpec(t)
	defaultDir, err := RuntimeDirFor(m.ConfigDir, "")
	if err != nil || defaultDir != filepath.Join(m.ConfigDir, "run") {
		t.Fatalf("default runtime dir = %q, %v", defaultDir, err)
	}
	other := filepath.Join(t.TempDir(), "elsewhere", "custom config.yaml")
	explicitDir, err := RuntimeDirFor(m.ConfigDir, other)
	if err != nil || explicitDir != filepath.Join(filepath.Dir(other), "run") {
		t.Fatalf("explicit runtime dir = %q, %v", explicitDir, err)
	}
	if spec.RuntimeDir != filepath.Join(filepath.Dir(spec.ConfigFile), "run") {
		t.Fatal("fixture did not follow explicit config runtime rule")
	}
}

func TestLockCompetitionAndRelease(t *testing.T) {
	m, _ := fixtureSpec(t)
	first, err := m.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Acquire(); !errors.Is(err, ErrLockBusy) {
		t.Fatalf("second lock = %v, want ErrLockBusy", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("double release: %v", err)
	}
	second, err := m.Acquire()
	if err != nil {
		t.Fatalf("lock not reusable after release: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestLockCompetitionAcrossProcesses(t *testing.T) {
	m, _ := fixtureSpec(t)
	ready := filepath.Join(t.TempDir(), "locked")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "-test.run=^TestLockChild$")
	cmd.Env = append(os.Environ(), "GOFER_LOCK_CHILD=1", "GOFER_LOCK_PATH="+m.LockPath(), "GOFER_LOCK_READY="+ready)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stdin.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not acquire lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := m.Acquire(); !errors.Is(err, ErrLockBusy) {
		t.Fatalf("parent acquired child-held lock: %v", err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	lock, err := m.Acquire()
	if err != nil {
		t.Fatalf("lock remained held after child exit: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestLockChild(t *testing.T) {
	if os.Getenv("GOFER_LOCK_CHILD") != "1" {
		return
	}
	lock, err := AcquireLock(os.Getenv("GOFER_LOCK_PATH"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if err := os.WriteFile(os.Getenv("GOFER_LOCK_READY"), []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func TestInspectServerChecksPIDExeOwnerAndCreation(t *testing.T) {
	m, spec := fixtureSpec(t)
	if err := m.SaveSpec(spec); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(spec.RuntimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := daemon.WritePIDFile(filepath.Join(spec.RuntimeDir, "serve.pid"), os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.InspectServer(); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("missing recorded identity not rejected: %v", err)
	}
	identity, err := daemon.CurrentProcessIdentity()
	if err != nil {
		t.Fatal(err)
	}
	state := State{SchemaVersion: StateSchema, Name: m.Name, Server: identity}
	if err := m.SaveState(state); err != nil {
		t.Fatal(err)
	}
	inspected, err := m.InspectServer()
	if err != nil || inspected.PID != os.Getpid() {
		t.Fatalf("InspectServer = %+v, %v", inspected, err)
	}
	state.Server = daemon.ProcessIdentity{}
	if err := m.SaveState(state); err != nil {
		t.Fatal(err)
	}
	if _, err := m.InspectServer(); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("zero recorded identity not rejected: %v", err)
	}
	state.Server = identity
	state.Server.StartID = "wrong-start"
	if err := m.SaveState(state); err != nil {
		t.Fatal(err)
	}
	if _, err := m.InspectServer(); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("reused PID identity not rejected: %v", err)
	}
	state.Server = identity
	if err := m.SaveState(state); err != nil {
		t.Fatal(err)
	}
	spec.Owner = "wrong-owner"
	if err := saveSpec(m.SpecPath(), spec); err != nil {
		t.Fatal(err)
	}
	if _, err := m.InspectServer(); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("different owner not rejected: %v", err)
	}
	spec.Owner = identity.Owner
	spec.Exe = spec.ConfigFile
	if err := saveSpec(m.SpecPath(), spec); err != nil {
		t.Fatal(err)
	}
	if _, err := m.InspectServer(); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("different executable not rejected: %v", err)
	}
	if strings.Contains(m.SpecPath(), ".tmp") {
		t.Fatal("metadata path unexpectedly temporary")
	}
}

func TestRunningInstanceRefusesSpecChange(t *testing.T) {
	m, spec := fixtureSpec(t)
	if err := m.SaveSpec(spec); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(spec.RuntimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := daemon.WritePIDFile(filepath.Join(spec.RuntimeDir, "serve.pid"), os.Getpid()); err != nil {
		t.Fatal(err)
	}
	changed := spec
	changed.RegisteredVersion = "next"
	if err := m.SaveSpec(changed); !errors.Is(err, ErrRunningSpecChange) {
		t.Fatalf("running config change = %v, want ErrRunningSpecChange", err)
	}
	if current, err := m.LoadSpec(); err != nil || current != spec {
		t.Fatalf("running spec was overwritten: %+v, %v", current, err)
	}
	if err := m.SaveSpec(spec); err != nil {
		t.Fatalf("idempotent same spec should succeed: %v", err)
	}
}
