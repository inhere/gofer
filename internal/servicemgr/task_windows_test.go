//go:build windows

package servicemgr

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
	"golang.org/x/sys/windows"
)

func init() {
	if os.Getenv("GOFER_T3_FAST_SERVER") == "1" {
		time.Sleep(100 * time.Millisecond)
		os.Exit(3)
	}
}

func TestWindowsTaskXMLPreservesPathsAndPrivileges(t *testing.T) {
	m, spec := fixtureSpec(t)
	spec.Name = m.Name
	spec.Serve.Addr = "127.0.0.1:18081"
	xmlText, err := buildTaskXML(spec, m.SupervisorPath(), m.SpecPath())
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, true, strings.Contains(xmlText, "InteractiveToken"))
	assert.Eq(t, true, strings.Contains(xmlText, "LeastPrivilege"))
	assert.Eq(t, true, strings.Contains(xmlText, "LogonTrigger"))
	assert.Eq(t, true, strings.Contains(xmlText, "RestartOnFailure"))
	assert.Eq(t, false, strings.Contains(xmlText, "pwsh"))
	assert.Require(t, assert.NoErr(t, verifyOwnedTask(xmlText, spec, m.SupervisorPath(), m.SpecPath())))
	extraAction := strings.Replace(xmlText, "</Actions>", "<Exec><Command>C:\\other.exe</Command></Exec></Actions>", 1)
	if err := verifyOwnedTask(extraAction, spec, m.SupervisorPath(), m.SpecPath()); err == nil {
		t.Fatal("extra action was accepted as owned")
	}
	extraTrigger := strings.Replace(xmlText, "</Triggers>", "<LogonTrigger><Enabled>true</Enabled></LogonTrigger></Triggers>", 1)
	if err := verifyOwnedTask(extraTrigger, spec, m.SupervisorPath(), m.SpecPath()); err == nil {
		t.Fatal("extra trigger was accepted as owned")
	}
	extraPrincipal := strings.Replace(xmlText, "</Principals>", `<Principal id="Other"><UserId>S-1-5-18</UserId></Principal></Principals>`, 1)
	if err := verifyOwnedTask(extraPrincipal, spec, m.SupervisorPath(), m.SpecPath()); err == nil {
		t.Fatal("extra principal was accepted as owned")
	}
	changedContext := strings.Replace(xmlText, `Actions Context="Author"`, `Actions Context="Other"`, 1)
	if err := verifyOwnedTask(changedContext, spec, m.SupervisorPath(), m.SpecPath()); err == nil {
		t.Fatal("other action context was accepted as owned")
	}
	spec.Elevated = true
	elevated, err := buildTaskXML(spec, m.SupervisorPath(), m.SpecPath())
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, true, strings.Contains(elevated, "HighestAvailable"))
}

func TestLegacyAdoptionRequiresExactTopology(t *testing.T) {
	_, spec := fixtureSpec(t)
	// The legacy script derives its repo from ExeDir's parent. Build a temp
	// layout so this test never depends on or operates the current live task.
	root := filepath.Join(spec.ConfigDir, "old repo")
	spec.Exe = filepath.Join(root, "serve-run", "gofer.exe")
	spec.WorkDir = root
	spec.ConfigFile = filepath.Join(spec.ConfigDir, "config.yaml")
	actual := taskDocument{XMLNS: "http://schemas.microsoft.com/windows/2004/02/mit/task", Version: "1.4"}
	actual.Registration.URI = `\` + spec.Name
	actual.Triggers.Logon.Enabled = true
	actual.Triggers.Logon.UserID = spec.Owner
	actual.Principals.Principal.UserID = spec.Owner
	actual.Principals.Principal.ID = "Author"
	actual.Principals.Principal.LogonType = "InteractiveToken"
	actual.Principals.Principal.RunLevel = "LeastPrivilege"
	actual.Actions.Exec.Command = `C:\Program Files\PowerShell\7\pwsh.exe`
	actual.Actions.Context = "Author"
	actual.Actions.Exec.WorkingDirectory = spec.WorkDir
	actual.Actions.Exec.Arguments = windows.ComposeCommandLine([]string{
		"-WindowStyle", "Hidden", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", filepath.Join(root, "scripts", "win-supervisor.ps1"),
		"-ExeDir", filepath.Dir(spec.Exe), "-WorkDir", root,
		"-ServeArgs", "serve", "-EnvExtra", "GOFER_CONFIG_DIR=" + spec.ConfigDir,
	})
	encoded, err := xml.Marshal(actual)
	assert.Require(t, assert.NoErr(t, err))
	assert.Require(t, assert.NoErr(t, verifyAdoptableLegacyTask(string(encoded), spec)))
	actual.Actions.Exec.Command = `C:\Windows\System32\cmd.exe`
	encoded, err = xml.Marshal(actual)
	assert.Require(t, assert.NoErr(t, err))
	if err := verifyAdoptableLegacyTask(string(encoded), spec); err == nil {
		t.Fatal("non-PowerShell action was adoptable")
	}
	actual.Actions.Exec.Command = `C:\Program Files\PowerShell\7\pwsh.exe`
	actual.Actions.Exec.Arguments = windows.ComposeCommandLine([]string{"-WindowStyle", "Hidden", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", filepath.Join(root, "other", "win-supervisor.ps1"),
		"-ExeDir", filepath.Dir(spec.Exe), "-WorkDir", root, "-ServeArgs", "serve", "-EnvExtra", "GOFER_CONFIG_DIR=" + spec.ConfigDir})
	encoded, err = xml.Marshal(actual)
	assert.Require(t, assert.NoErr(t, err))
	if err := verifyAdoptableLegacyTask(string(encoded), spec); err == nil {
		t.Fatal("untrusted script path was adoptable")
	}
}

func TestWindowsRegisterRestoresSupervisorOnFailures(t *testing.T) {
	if os.Getenv("GOFER_NATIVE_TASK_TEST") != "1" {
		t.Skip("set GOFER_NATIVE_TASK_TEST=1 for isolated native rollback test")
	}
	_, spec := fixtureSpec(t)
	spec.Name = fmt.Sprintf("gofer-t3-rollback-%d", time.Now().UnixNano())
	m, err := NewManager(spec.ConfigDir, spec.Name)
	assert.Require(t, assert.NoErr(t, err))
	spec.Exe = filepath.Join(spec.ConfigDir, "candidate.exe")
	assert.Require(t, assert.NoErr(t, os.WriteFile(spec.Exe, []byte("old-binary"), 0o600)))
	assert.Require(t, assert.NoErr(t, m.WindowsRegister(spec, WindowsRegisterOptions{})))
	t.Cleanup(func() { _ = m.WindowsUninstall() })
	oldTask, found, err := readScheduledTask(spec.Name)
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, true, found)
	assert.Require(t, assert.NoErr(t, os.WriteFile(spec.Exe, []byte("new-binary"), 0o600)))
	changed := spec
	changed.RegisteredVersion = "next"
	oldRegister := windowsNativeRegister
	defer func() { windowsNativeRegister = oldRegister }()
	windowsNativeRegister = func(string, string, string, bool) error { return errors.New("injected COM registration failure") }
	if err := m.WindowsRegister(changed, WindowsRegisterOptions{}); err == nil {
		t.Fatal("injected COM failure was hidden")
	}
	got, err := os.ReadFile(m.SupervisorPath())
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, "old-binary", string(got))
	currentTask, found, err := readScheduledTask(spec.Name)
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, true, found)
	assert.Eq(t, oldTask.XML, currentTask.XML)
	windowsNativeRegister = oldRegister
	oldPersist := windowsPersistSpec
	defer func() { windowsPersistSpec = oldPersist }()
	windowsPersistSpec = func(*Manager, Spec) error { return errors.New("injected metadata failure") }
	if err := m.WindowsRegister(changed, WindowsRegisterOptions{}); err == nil {
		t.Fatal("injected metadata failure was hidden")
	}
	got, err = os.ReadFile(m.SupervisorPath())
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, "old-binary", string(got))
	currentTask, found, err = readScheduledTask(spec.Name)
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, true, found)
	assert.Require(t, assert.NoErr(t, verifyOwnedTask(currentTask.XML, spec, m.SupervisorPath(), m.SpecPath())))
	currentSpec, err := m.LoadSpec()
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, spec, currentSpec)
}

func TestNativeTaskReadMissing(t *testing.T) {
	name := fmt.Sprintf("gofer-t3-missing-%d", time.Now().UnixNano())
	_, found, err := readScheduledTask(name)
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, false, found)
}

func TestSupervisorStopsAfterFastFailureBudget(t *testing.T) {
	m, spec := fixtureSpec(t)
	self, err := os.Executable()
	assert.Require(t, assert.NoErr(t, err))
	spec.Exe = filepath.Join(spec.ConfigDir, "fast-server.exe")
	assert.Require(t, assert.NoErr(t, copySupervisorBinary(self, spec.Exe)))
	assert.Require(t, assert.NoErr(t, copySupervisorBinary(self, m.SupervisorPath())))
	assert.Require(t, assert.NoErr(t, m.SaveSpec(spec)))
	assert.Require(t, assert.NoErr(t, m.SaveState(State{SchemaVersion: StateSchema, Name: m.Name})))
	cmd := exec.Command(m.SupervisorPath(), "-test.run=^TestSupervisorFastFailureChild$")
	cmd.Env = append(os.Environ(), "GOFER_T3_SUPERVISE_SPEC="+m.SpecPath())
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("supervisor helper: %v, output=%s", err, output)
	}
	state, err := m.LoadState()
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, 0, state.Supervisor.PID)
	assert.Eq(t, 0, state.Server.PID)
	log, err := os.ReadFile(m.windowsLogPath())
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, maxFastFailures, strings.Count(string(log), "server exited"))
}

func TestStandaloneSplitDirectories(t *testing.T) {
	m, spec := fixtureSpec(t)
	configFileDir := filepath.Join(t.TempDir(), "cfg with spaces")
	assert.Require(t, assert.NoErr(t, os.MkdirAll(configFileDir, 0o700)))
	spec.ConfigFile = filepath.Join(configFileDir, "config.yaml")
	assert.Require(t, assert.NoErr(t, os.WriteFile(spec.ConfigFile, []byte("server: {}\n"), 0o600)))
	spec.RuntimeDir = filepath.Join(configFileDir, "run")
	if _, err := os.Stat(spec.RuntimeDir); !os.IsNotExist(err) {
		t.Fatalf("runtime directory must start absent: %v", err)
	}
	self, err := os.Executable()
	assert.Require(t, assert.NoErr(t, err))
	spec.Exe = filepath.Join(spec.ConfigDir, "fast-server.exe")
	assert.Require(t, assert.NoErr(t, copySupervisorBinary(self, spec.Exe)))
	assert.Require(t, assert.NoErr(t, copySupervisorBinary(self, m.SupervisorPath())))
	assert.Require(t, assert.NoErr(t, m.SaveSpec(spec)))
	assert.Require(t, assert.NoErr(t, m.SaveState(State{SchemaVersion: StateSchema, Name: m.Name})))
	cmd := exec.Command(m.SupervisorPath(), "-test.run=^TestSupervisorFastFailureChild$")
	cmd.Env = append(os.Environ(), "GOFER_T3_SUPERVISE_SPEC="+m.SpecPath())
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("split-directory supervisor: %v, output=%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(spec.RuntimeDir, "serve.out.log")); err != nil {
		t.Fatalf("supervisor did not create the config-file runtime directory: %v", err)
	}
	log, err := os.ReadFile(m.windowsLogPath())
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, maxFastFailures, strings.Count(string(log), "server exited"))
}

func TestSupervisorFastFailureChild(t *testing.T) {
	specPath := os.Getenv("GOFER_T3_SUPERVISE_SPEC")
	if specPath == "" {
		return
	}
	fastFailure = 500 * time.Millisecond
	restartPause = 10 * time.Millisecond
	assert.Require(t, assert.NoErr(t, os.Setenv("GOFER_T3_FAST_SERVER", "1")))
	err := Supervise(context.Background(), specPath)
	if err == nil || !strings.Contains(err.Error(), "failed 3 times") {
		t.Fatalf("fast failure budget: %v", err)
	}
}

func TestNativeTaskValidateOnly(t *testing.T) {
	m, spec := fixtureSpec(t)
	xmlText, err := buildTaskXML(spec, m.SupervisorPath(), m.SpecPath())
	assert.Require(t, assert.NoErr(t, err))
	assert.Require(t, assert.NoErr(t, validateScheduledTaskXML(spec.Name, xmlText, spec.Owner)))
}

func TestNativeTaskRegisterAndDeleteIsolated(t *testing.T) {
	if os.Getenv("GOFER_NATIVE_TASK_TEST") != "1" {
		t.Skip("set GOFER_NATIVE_TASK_TEST=1 for isolated Task Scheduler write test")
	}
	_, spec := fixtureSpec(t)
	spec.Name = fmt.Sprintf("gofer-t3-%d", time.Now().UnixNano())
	m, err := NewManager(spec.ConfigDir, spec.Name)
	assert.Require(t, assert.NoErr(t, err))
	supervisor := filepath.Join(spec.ConfigDir, "test supervisor.exe")
	xmlText, err := buildTaskXML(spec, supervisor, m.SpecPath())
	assert.Require(t, assert.NoErr(t, err))
	if _, found, err := readScheduledTask(spec.Name); err != nil || found {
		t.Fatalf("test name already present: found=%v err=%v", found, err)
	}
	if err := registerScheduledTask(spec.Name, xmlText, spec.Owner, false); err != nil {
		t.Log(xmlText)
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = deleteScheduledTask(spec.Name) })
	got, found, err := readScheduledTask(spec.Name)
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, true, found)
	assert.Require(t, assert.NoErr(t, verifyOwnedTask(got.XML, spec, supervisor, m.SpecPath())))
	assert.Require(t, assert.NoErr(t, deleteScheduledTask(spec.Name)))
	_, found, err = readScheduledTask(spec.Name)
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, false, found)
}

func TestWindowsLifecycleWithGoferBinary(t *testing.T) {
	if os.Getenv("GOFER_NATIVE_TASK_TEST") != "1" {
		t.Skip("set GOFER_NATIVE_TASK_TEST=1 for isolated native lifecycle")
	}
	goferBinary := os.Getenv("GOFER_T3_GOFER_BINARY")
	if goferBinary == "" {
		t.Skip("set GOFER_T3_GOFER_BINARY to the freshly built isolated test binary")
	}
	_, spec := fixtureSpec(t)
	spec.Name = fmt.Sprintf("gofer-t3-%d", time.Now().UnixNano())
	m, err := NewManager(spec.ConfigDir, spec.Name)
	assert.Require(t, assert.NoErr(t, err))
	spec.Exe = goferBinary
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	assert.Require(t, assert.NoErr(t, err))
	addr := listener.Addr().String()
	assert.Require(t, assert.NoErr(t, listener.Close()))
	spec.Serve = ServeOptions{Addr: addr, NoWeb: true, AllowEmptyToken: true}
	spec.RuntimeDir = filepath.Join(filepath.Dir(spec.ConfigFile), "run")
	t.Cleanup(func() {
		if _, found, _ := readScheduledTask(spec.Name); found {
			_ = m.WindowsUninstall()
		}
	})
	assert.Require(t, assert.NoErr(t, m.WindowsRegister(spec, WindowsRegisterOptions{})))
	registered, err := m.WindowsStatus()
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, true, registered.Registered)
	assert.Require(t, assert.NoErr(t, m.WindowsStart()))
	waitVerifiedServer(t, m)
	assert.Require(t, assert.NoErr(t, m.WindowsRegister(spec, WindowsRegisterOptions{})))
	assertServingInCurrentSession(t, m, addr)
	assert.Require(t, assert.NoErr(t, m.WindowsStop()))
	stopped, err := m.WindowsStatus()
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, true, stopped.StopRequested)
	assert.Eq(t, false, stopped.ServerVerified)
	assert.Require(t, assert.NoErr(t, m.WindowsStart()))
	waitVerifiedServer(t, m)
	assertServingInCurrentSession(t, m, addr)
	assert.Require(t, assert.NoErr(t, m.WindowsUninstall()))
	_, found, err := readScheduledTask(spec.Name)
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, false, found)
	if _, err := os.Stat(spec.ConfigFile); err != nil {
		t.Fatalf("uninstall removed config: %v", err)
	}
}

func assertServingInCurrentSession(t *testing.T, m *Manager, addr string) {
	t.Helper()
	status, err := m.WindowsStatus()
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, true, status.ServerVerified)
	var expected, supervisorSession, serverSession uint32
	assert.Require(t, assert.NoErr(t, windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &expected)))
	assert.Require(t, assert.NoErr(t, windows.ProcessIdToSessionId(uint32(status.Supervisor.PID), &supervisorSession)))
	assert.Require(t, assert.NoErr(t, windows.ProcessIdToSessionId(uint32(status.Server.PID), &serverSession)))
	assert.Eq(t, expected, supervisorSession)
	assert.Eq(t, expected, serverSession)
	assert.Eq(t, true, serverSession != 0)
	client := http.Client{Timeout: time.Second}
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get("http://" + addr + "/health")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("verified server pid=%d did not serve /health on %s", status.Server.PID, addr)
}

func waitVerifiedServer(t *testing.T, m *Manager) {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		status, err := m.WindowsStatus()
		if err == nil && status.ServerVerified && status.Supervisor.PID > 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	status, err := m.WindowsStatus()
	t.Fatalf("server not verified: status=%+v err=%v", status, err)
}
