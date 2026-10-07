//go:build linux

package servicemgr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLinuxIndependentUpgradeSuccessIsolated(t *testing.T) {
	oldBinary, candidate := os.Getenv("GOFER_T5_LINUX_OLD"), os.Getenv("GOFER_T5_LINUX_NEW")
	if oldBinary == "" || candidate == "" {
		t.Skip("set cross-built old and new Gofer binaries for isolated native systemd upgrade")
	}
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	for _, backend := range []Backend{BackendSystemdUser, BackendSystemdSystem} {
		t.Run(string(backend), func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "upgrade with space")
			configDir := filepath.Join(root, "assets")
			if err := os.MkdirAll(configDir, 0o700); err != nil {
				t.Fatal(err)
			}
			name := "gofer-t5-" + strconv.FormatInt(time.Now().UnixNano(), 36)
			m, err := NewManager(configDir, name)
			if err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			addr := listener.Addr().String()
			listener.Close()
			configFile := filepath.Join(root, "config.yaml")
			if err := os.WriteFile(configFile, []byte("server:\n  addr: "+addr+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			exe := filepath.Join(root, "managed-gofer")
			if err := copyHelperImage(oldBinary, exe); err != nil {
				t.Fatal(err)
			}
			spec := Spec{SchemaVersion: SpecSchema, Backend: backend, Name: name, Owner: current.Uid,
				Exe: exe, WorkDir: root, ConfigFile: configFile, ConfigDir: configDir, RuntimeDir: filepath.Join(root, "run"),
				Serve: ServeOptions{Addr: addr, NoWeb: true, AllowEmptyToken: true}}
			if backend == BackendSystemdSystem {
				spec.RunAs = current.Username
			}
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
			defer cancel()
			if err := SystemdRegister(ctx, m, spec); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				cleanCtx, done := context.WithTimeout(context.Background(), 20*time.Second)
				defer done()
				if err := SystemdUninstall(cleanCtx, m); err != nil {
					t.Errorf("cleanup isolated unit: %v", err)
				}
			})
			if _, err := SystemdStart(ctx, m); err != nil {
				t.Fatal(err)
			}
			if err := waitLinuxOwnedHealth(ctx, m, addr); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(candidate)
			if err != nil {
				t.Fatal(err)
			}
			hash := sha256.Sum256(data)
			receipt, err := BeginUpgrade(ctx, m, UpgradeRequest{CandidatePath: candidate, Size: int64(len(data)), SHA256: hex.EncodeToString(hash[:]), Deadline: time.Now().Add(40 * time.Second)})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := WaitUpgradeAccepted(ctx, m, receipt.UpgradeID); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(30 * time.Second)
			for time.Now().Before(deadline) {
				result, err := m.LoadUpgradeReceipt(receipt.UpgradeID)
				if err != nil {
					t.Fatal(err)
				}
				if result.Phase == UpgradeSucceeded {
					if err := waitLinuxOwnedHealth(ctx, m, addr); err != nil {
						t.Fatal(err)
					}
					out, err := exec.Command(exe, "--version").Output()
					if err != nil || !strings.Contains(string(out), "t5-linux-new") {
						t.Fatalf("installed version %q: %v", out, err)
					}
					return
				}
				if result.Phase == UpgradeFailed || result.Phase == UpgradeRolledBack {
					t.Fatalf("upgrade %s: %s", result.Phase, result.Error)
				}
				time.Sleep(100 * time.Millisecond)
			}
			t.Fatal("Linux helper did not persist success")
		})
	}
}

func waitLinuxOwnedHealth(ctx context.Context, m *Manager, addr string) error {
	spec, err := m.LoadSpec()
	if err != nil {
		return err
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if err := waitManagedHealthy(ctx, m, spec, "", 600*time.Millisecond); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("managed Linux server did not own healthy port %s", addr)
}

func TestLinuxIndependentUpgradeRollsBackBadCandidate(t *testing.T) {
	oldBinary, candidate := os.Getenv("GOFER_T5_LINUX_OLD"), os.Getenv("GOFER_T5_LINUX_BAD")
	if oldBinary == "" || candidate == "" {
		t.Skip("set old and failing candidate binaries for isolated native systemd rollback")
	}
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	for _, backend := range []Backend{BackendSystemdUser, BackendSystemdSystem} {
		t.Run(string(backend), func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "rollback with space")
			configDir := filepath.Join(root, "assets")
			if err := os.MkdirAll(configDir, 0o700); err != nil {
				t.Fatal(err)
			}
			name := "gofer-t5-bad-" + strconv.FormatInt(time.Now().UnixNano(), 36)
			m, err := NewManager(configDir, name)
			if err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			addr := listener.Addr().String()
			listener.Close()
			configFile := filepath.Join(root, "config.yaml")
			if err := os.WriteFile(configFile, []byte("server:\n  addr: "+addr+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			exe := filepath.Join(root, "managed-gofer")
			if err := copyHelperImage(oldBinary, exe); err != nil {
				t.Fatal(err)
			}
			spec := Spec{SchemaVersion: SpecSchema, Backend: backend, Name: name, Owner: current.Uid,
				Exe: exe, WorkDir: root, ConfigFile: configFile, ConfigDir: configDir, RuntimeDir: filepath.Join(root, "run"),
				Serve: ServeOptions{Addr: addr, NoWeb: true, AllowEmptyToken: true}}
			if backend == BackendSystemdSystem {
				spec.RunAs = current.Username
			}
			ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
			defer cancel()
			if err := SystemdRegister(ctx, m, spec); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				cleanCtx, done := context.WithTimeout(context.Background(), 20*time.Second)
				defer done()
				if err := SystemdUninstall(cleanCtx, m); err != nil {
					t.Errorf("cleanup isolated unit: %v", err)
				}
			})
			if _, err := SystemdStart(ctx, m); err != nil {
				t.Fatal(err)
			}
			if err := waitLinuxOwnedHealth(ctx, m, addr); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(candidate)
			if err != nil {
				t.Fatal(err)
			}
			hash := sha256.Sum256(data)
			receipt, err := BeginUpgrade(ctx, m, UpgradeRequest{CandidatePath: candidate, Size: int64(len(data)), SHA256: hex.EncodeToString(hash[:]), Deadline: time.Now().Add(48 * time.Second)})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := WaitUpgradeAccepted(ctx, m, receipt.UpgradeID); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(35 * time.Second)
			for time.Now().Before(deadline) {
				result, err := m.LoadUpgradeReceipt(receipt.UpgradeID)
				if err != nil {
					t.Fatal(err)
				}
				if result.Phase == UpgradeRolledBack {
					if err := waitLinuxOwnedHealth(ctx, m, addr); err != nil {
						t.Fatal(err)
					}
					out, err := exec.Command(exe, "--version").Output()
					if err != nil || !strings.Contains(string(out), "t5-linux-old") {
						t.Fatalf("rollback version %q: %v", out, err)
					}
					return
				}
				if result.Phase == UpgradeFailed || result.Phase == UpgradeSucceeded {
					version, versionErr := exec.Command(exe, "--version").CombinedOutput()
					currentSpec, specErr := m.LoadSpec()
					status, statusErr := SystemdStatusOf(ctx, m)
					t.Fatalf("bad candidate ended %s: %s; installed=%q version_err=%v; spec_version=%q spec_err=%v; status=%+v status_err=%v",
						result.Phase, result.Error, version, versionErr, currentSpec.RegisteredVersion, specErr, status, statusErr)
				}
				time.Sleep(100 * time.Millisecond)
			}
			t.Fatal("Linux helper did not persist rollback")
		})
	}
}
