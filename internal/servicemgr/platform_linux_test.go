//go:build linux

package servicemgr

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func linuxSpec(t *testing.T) (*Manager, Spec) {
	t.Helper()
	root := t.TempDir()
	configDir := filepath.Join(root, "配置 with space")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(configDir, "config.yaml")
	if err := os.WriteFile(configFile, []byte("server:\n  addr: 127.0.0.1:0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(root, "gofer binary")
	if err := os.WriteFile(exe, []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	name := "gofer-t4-test"
	m, err := NewManager(configDir, name)
	if err != nil {
		t.Fatal(err)
	}
	spec := Spec{SchemaVersion: SpecSchema, Backend: BackendSystemdUser, Name: name, Owner: current.Uid,
		Exe: exe, WorkDir: root, ConfigFile: configFile, ConfigDir: configDir,
		RuntimeDir: filepath.Join(configDir, "run"), Serve: ServeOptions{Addr: "127.0.0.1:0", NoWeb: true}}
	return m, spec
}

func TestSystemdUnitContent(t *testing.T) {
	_, spec := linuxSpec(t)
	spec.Exe = filepath.Join(filepath.Dir(spec.Exe), `gofer "binary" %`)
	spec.Exe += `$NAME`
	spec.Serve.WebDir = ""
	unit, err := unitContent(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# Gofer-Managed: " + spec.Owner,
		`ExecStart="`, `\"binary\"`, `%%`, `$NAME`, `"serve" "-c"`,
		`Environment="GOFER_CONFIG_DIR=`, `Environment="GOFER_MANAGED_SPEC=`, "Restart=on-failure", "StartLimitBurst=5", "TimeoutStopSec=180s", "WantedBy=default.target",
	} {
		if !bytes.Contains(unit, []byte(want)) {
			t.Errorf("unit missing %q: %s", want, unit)
		}
	}
	if bytes.Contains(unit, []byte("User=")) {
		t.Fatal("user unit must use current user")
	}
	if bytes.Contains(unit, []byte("token")) {
		t.Fatal("unit contains credential string")
	}
	spec.Backend = BackendSystemdSystem
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	spec.RunAs = current.Username
	unit, err = unitContent(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(unit, []byte("User="+spec.RunAs)) || !bytes.Contains(unit, []byte("WantedBy=multi-user.target")) {
		t.Fatal(string(unit))
	}
	spec.RunAs = ""
	if _, err := unitContent(spec); err == nil {
		t.Fatal("system scope accepted missing run-as")
	}
	spec.Backend = BackendSystemdUser
	spec.RunAs = ""
	spec.Serve.Addr = "127.0.0.1:1\nSecret=bad"
	if _, err := unitContent(spec); err == nil {
		t.Fatal("unit injection accepted")
	}
}

func TestSystemdRegisterAndOwnership(t *testing.T) {
	m, spec := linuxSpec(t)
	unitDir := filepath.Join(t.TempDir(), "units")
	var calls [][]string
	b := systemdBackend{unitDir: unitDir, run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "systemctl" {
			t.Fatalf("unexpected command %s", name)
		}
		calls = append(calls, append([]string{name}, args...))
		if len(args) > 1 && args[1] == "show" {
			if _, err := os.Stat(filepath.Join(unitDir, spec.Name+".service")); errors.Is(err, os.ErrNotExist) {
				return []byte("LoadState=not-found\nFragmentPath=\n"), nil
			}
			if strings.Contains(strings.Join(args, " "), "LoadState,FragmentPath") {
				return []byte("LoadState=loaded\nFragmentPath=" + filepath.Join(unitDir, spec.Name+".service") + "\n"), nil
			}
			return []byte("ActiveState=inactive\nSubState=dead\nMainPID=0\nUnitFileState=enabled\nInvocationID=\n"), nil
		}
		return nil, nil
	}}
	if err := b.register(context.Background(), m, spec); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"systemctl", "--user", "show", spec.Name + ".service", "--property=LoadState,FragmentPath", "--no-pager"},
		{"systemctl", "--user", "daemon-reload"}, {"systemctl", "--user", "enable", spec.Name + ".service"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v", calls)
	}
	if err := b.register(context.Background(), m, spec); err != nil {
		t.Fatal(err)
	}
	if len(calls) != len(want)+4 {
		t.Fatalf("identical registration did more than read status: %#v", calls)
	}
	if _, err := m.LoadSpec(); err != nil {
		t.Fatal(err)
	}
	other, err := NewManager(spec.ConfigDir, "gofer-other")
	if err != nil {
		t.Fatal(err)
	}
	_ = other
	path := filepath.Join(unitDir, spec.Name+".service")
	if err := os.WriteFile(path, []byte("[Unit]\nDescription=someone else\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := b.register(context.Background(), m, spec); err == nil || !strings.Contains(err.Error(), "not owned") {
		t.Fatalf("foreign unit: %v", err)
	}
}

func TestSystemdStopAndUninstallPreserveAssets(t *testing.T) {
	m, spec := linuxSpec(t)
	unitDir := t.TempDir()
	unit, err := unitContent(spec)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(unitDir, spec.Name+".service")
	if err := os.WriteFile(path, unit, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.SaveSpec(spec); err != nil {
		t.Fatal(err)
	}
	var calls []string
	b := systemdBackend{unitDir: unitDir, run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		if len(args) > 1 && args[1] == "show" {
			if strings.Contains(strings.Join(args, " "), "LoadState,FragmentPath") {
				return []byte("LoadState=loaded\nFragmentPath=" + path + "\n"), nil
			}
			return []byte("ActiveState=inactive\nSubState=dead\nMainPID=0\nUnitFileState=enabled\nInvocationID=\n"), nil
		}
		return nil, nil
	}}
	if err := b.stop(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if err := b.uninstall(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unit remains: %v", err)
	}
	if _, err := os.Stat(spec.ConfigFile); err != nil {
		t.Fatal("config removed:", err)
	}
	if _, err := os.Stat(spec.Exe); err != nil {
		t.Fatal("binary removed:", err)
	}
	for _, want := range []string{"--user stop " + spec.Name, "--user disable " + spec.Name, "--user daemon-reload"} {
		if !strings.Contains(strings.Join(calls, "\n"), want) {
			t.Fatalf("missing %q in %v", want, calls)
		}
	}
}

// Run only with an explicit, cross-built test executable in an isolated Linux
// environment. The test owns a random unit name and removes that unit on exit.
func TestSystemdNativeIntegration(t *testing.T) {
	exe := os.Getenv("GOFER_SYSTEMD_TEST_EXE")
	if exe == "" {
		t.Skip("set GOFER_SYSTEMD_TEST_EXE for native systemd validation")
	}
	if !filepath.IsAbs(exe) {
		t.Fatal("test executable must be absolute")
	}
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	for _, backend := range []Backend{BackendSystemdUser, BackendSystemdSystem} {
		t.Run(string(backend), func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "配置 with space")
			if err := os.MkdirAll(root, 0o700); err != nil {
				t.Fatal(err)
			}
			name := "gofer-t4-" + strconv.FormatInt(time.Now().UnixNano(), 36)
			configDir := filepath.Join(root, "assets 目录")
			if err := os.MkdirAll(configDir, 0o700); err != nil {
				t.Fatal(err)
			}
			configFileDir := filepath.Join(root, "cfg 目录")
			if err := os.MkdirAll(configFileDir, 0o700); err != nil {
				t.Fatal(err)
			}
			m, err := NewManager(configDir, name)
			if err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			addr := listener.Addr().String()
			listener.Close()
			configFile := filepath.Join(configFileDir, "config.yaml")
			if err := os.WriteFile(configFile, []byte("server:\n  addr: "+addr+"\nlog:\n  file: '{config_dir}/logs/managed.log'\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			serviceExe := filepath.Join(root, "gofer $literal 程序")
			if err := os.Link(exe, serviceExe); err != nil {
				t.Fatal(err)
			}
			spec := Spec{SchemaVersion: SpecSchema, Backend: backend, Name: name, Owner: current.Uid,
				Exe: serviceExe, WorkDir: root, ConfigFile: configFile, ConfigDir: configDir, RuntimeDir: filepath.Join(configFileDir, "run"),
				Serve: ServeOptions{Addr: addr, NoWeb: true, AllowEmptyToken: true}}
			if backend == BackendSystemdSystem {
				spec.RunAs = current.Username
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := SystemdRegister(ctx, m, spec); err != nil {
				t.Fatal(err)
			}
			installed := true
			t.Cleanup(func() {
				if !installed {
					return
				}
				cleanupCtx, done := context.WithTimeout(context.Background(), 30*time.Second)
				defer done()
				if err := SystemdUninstall(cleanupCtx, m); err != nil {
					t.Errorf("cleanup unit: %v", err)
				}
			})
			status, err := SystemdStatusOf(ctx, m)
			if err != nil {
				t.Fatal(err)
			}
			if !status.Installed || !status.Enabled || status.Active {
				t.Fatalf("after register: %+v", status)
			}
			status, err = SystemdStart(ctx, m)
			if err != nil {
				journal, _ := SystemdJournal(context.Background(), m, 20)
				t.Fatalf("start: %v; status=%+v; journal=%s", err, status, journal)
			}
			if !status.Active || status.MainPID <= 0 || status.Server.Owner != current.Uid {
				t.Fatalf("after start: %+v", status)
			}
			environ, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(status.MainPID), "environ"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(environ, []byte("GOFER_CONFIG_DIR="+configDir+"\x00")) {
				t.Fatal("server did not receive explicit config dir")
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				conn, dialErr := net.DialTimeout("tcp", addr, 100*time.Millisecond)
				if dialErr == nil {
					conn.Close()
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("server did not listen on %s: %v", addr, dialErr)
				}
				time.Sleep(50 * time.Millisecond)
			}
			changed := spec
			changed.Serve.Addr = "127.0.0.1:1"
			if err := SystemdRegister(ctx, m, changed); !errors.Is(err, ErrRunningSpecChange) {
				t.Fatalf("active spec change: %v", err)
			}
			if _, err := SystemdJournal(ctx, m, 20); err != nil {
				t.Errorf("journal source: %v", err)
			}
			if _, err := os.Stat(status.ApplicationLog); err != nil {
				t.Errorf("application log: %v", err)
			}
			if status.ApplicationLog != filepath.Join(configDir, "logs", "managed.log") {
				t.Fatalf("wrong application log: %q", status.ApplicationLog)
			}
			firstInvocation, err := m.LoadState()
			if err != nil || firstInvocation.NativeInvocationID == "" {
				t.Fatalf("missing native invocation: %+v, %v", firstInvocation, err)
			}
			if err := syscall.Kill(status.MainPID, syscall.SIGKILL); err != nil {
				t.Fatal(err)
			}
			var restarted SystemdStatus
			restartDeadline := time.Now().Add(8 * time.Second)
			for time.Now().Before(restartDeadline) {
				restarted, err = SystemdStatusOf(ctx, m)
				if err == nil && restarted.Active && restarted.MainPID != status.MainPID {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			if err != nil || !restarted.Active || restarted.MainPID == status.MainPID {
				t.Fatalf("systemd did not restart after crash: %+v, %v", restarted, err)
			}
			secondInvocation, err := m.LoadState()
			if err != nil || secondInvocation.NativeInvocationID == firstInvocation.NativeInvocationID {
				t.Fatalf("invocation did not change: %+v, %v", secondInvocation, err)
			}
			if err := SystemdStop(ctx, m); err != nil {
				t.Fatal(err)
			}
			status, err = SystemdStatusOf(ctx, m)
			if err != nil {
				t.Fatal(err)
			}
			if status.Active || status.MainPID != 0 {
				t.Fatalf("after stop: %+v", status)
			}
			status, err = SystemdStart(ctx, m)
			if err != nil || !status.Active {
				t.Fatalf("restart via start: %+v, %v", status, err)
			}
			if err := SystemdUninstall(ctx, m); err != nil {
				t.Fatal(err)
			}
			installed = false
			if _, err := os.Stat(configFile); err != nil {
				t.Fatal("config removed:", err)
			}
			if _, err := os.Stat(serviceExe); err != nil {
				t.Fatal("binary removed:", err)
			}
			if _, err := os.Stat(status.ApplicationLog); err != nil {
				t.Fatal("app log removed:", err)
			}
		})
	}
}
