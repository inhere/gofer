//go:build windows

package servicemgr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
)

// The test directly acknowledges a no-job transaction after the helper takes
// over. The serve/job drain bridge has separate tests and real job acceptance.
func TestWindowsIndependentUpgradeSuccessIsolated(t *testing.T) {
	if os.Getenv("GOFER_T5_NATIVE_TEST") != "1" {
		t.Skip("isolated native upgrade opt-in")
	}
	oldBinary, candidate := os.Getenv("GOFER_T5_OLD_BINARY"), os.Getenv("GOFER_T5_CANDIDATE_BINARY")
	if oldBinary == "" || candidate == "" {
		t.Fatal("two freshly built binaries are required")
	}
	_, spec := fixtureSpec(t)
	spec.Name = fmt.Sprintf("gofer-t5-%d", time.Now().UnixNano())
	m, err := NewManager(spec.ConfigDir, spec.Name)
	assert.Require(t, assert.NoErr(t, err))
	spec.Exe = filepath.Join(spec.ConfigDir, "managed.exe")
	assert.Require(t, assert.NoErr(t, copySupervisorBinary(oldBinary, spec.Exe)))
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	assert.Require(t, assert.NoErr(t, err))
	spec.Serve = ServeOptions{Addr: listener.Addr().String(), NoWeb: true, AllowEmptyToken: true}
	assert.Require(t, assert.NoErr(t, listener.Close()))
	t.Cleanup(func() {
		if _, found, _ := readScheduledTask(spec.Name); found {
			_ = m.WindowsUninstall()
		}
	})
	assert.Require(t, assert.NoErr(t, m.WindowsRegister(spec, WindowsRegisterOptions{Start: true})))
	waitVerifiedServer(t, m)
	assertServingInCurrentSession(t, m, spec.Serve.Addr)
	data, err := os.ReadFile(candidate)
	assert.Require(t, assert.NoErr(t, err))
	hash := sha256.Sum256(data)
	receipt, err := BeginUpgrade(context.Background(), m, UpgradeRequest{
		CandidatePath: candidate, Size: int64(len(data)), SHA256: hex.EncodeToString(hash[:]), Deadline: time.Now().Add(35 * time.Second),
	})
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, true, receipt.Helper.PID > 0)
	if os.Getenv("GOFER_T5_BRIDGE_TEST") != "1" {
		assert.Require(t, assert.NoErr(t, m.AcceptUpgradeControl(receipt.UpgradeID, "")))
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		current, err := m.LoadUpgradeReceipt(receipt.UpgradeID)
		assert.Require(t, assert.NoErr(t, err))
		if current.Phase == UpgradeSucceeded {
			assertServingInCurrentSession(t, m, spec.Serve.Addr)
			stored, err := m.LoadSpec()
			assert.Require(t, assert.NoErr(t, err))
			assert.Eq(t, "t5-new", stored.RegisteredVersion)
			return
		}
		if current.Phase == UpgradeFailed || current.Phase == UpgradeRolledBack {
			t.Fatalf("upgrade ended %s: %s", current.Phase, current.Error)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("independent helper did not persist terminal result")
}

func TestWindowsIndependentUpgradeRollsBackBadCandidate(t *testing.T) {
	if os.Getenv("GOFER_T5_NATIVE_TEST") != "1" {
		t.Skip("isolated native upgrade opt-in")
	}
	oldBinary, candidate := os.Getenv("GOFER_T5_OLD_BINARY"), os.Getenv("GOFER_T5_BAD_BINARY")
	if oldBinary == "" || candidate == "" {
		t.Fatal("old and failing candidate binaries are required")
	}
	_, spec := fixtureSpec(t)
	spec.Name = fmt.Sprintf("gofer-t5-bad-%d", time.Now().UnixNano())
	m, err := NewManager(spec.ConfigDir, spec.Name)
	assert.Require(t, assert.NoErr(t, err))
	spec.Exe = filepath.Join(spec.ConfigDir, "managed.exe")
	assert.Require(t, assert.NoErr(t, copySupervisorBinary(oldBinary, spec.Exe)))
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	assert.Require(t, assert.NoErr(t, err))
	spec.Serve = ServeOptions{Addr: listener.Addr().String(), NoWeb: true, AllowEmptyToken: true}
	assert.Require(t, assert.NoErr(t, listener.Close()))
	t.Cleanup(func() {
		if _, found, _ := readScheduledTask(spec.Name); found {
			_ = m.WindowsUninstall()
		}
	})
	assert.Require(t, assert.NoErr(t, m.WindowsRegister(spec, WindowsRegisterOptions{Start: true})))
	waitVerifiedServer(t, m)
	data, err := os.ReadFile(candidate)
	assert.Require(t, assert.NoErr(t, err))
	hash := sha256.Sum256(data)
	receipt, err := BeginUpgrade(context.Background(), m, UpgradeRequest{
		CandidatePath: candidate, Size: int64(len(data)), SHA256: hex.EncodeToString(hash[:]), Deadline: time.Now().Add(45 * time.Second),
	})
	assert.Require(t, assert.NoErr(t, err))
	assert.Require(t, assert.NoErr(t, m.AcceptUpgradeControl(receipt.UpgradeID, "")))
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		current, err := m.LoadUpgradeReceipt(receipt.UpgradeID)
		assert.Require(t, assert.NoErr(t, err))
		if current.Phase == UpgradeRolledBack {
			assertServingInCurrentSession(t, m, spec.Serve.Addr)
			output, err := exec.Command(spec.Exe, "--version").Output()
			assert.Require(t, assert.NoErr(t, err))
			assert.Eq(t, true, strings.Contains(string(output), "t5-old"))
			return
		}
		if current.Phase == UpgradeFailed || current.Phase == UpgradeSucceeded {
			t.Fatalf("bad candidate ended %s: %s", current.Phase, current.Error)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("bad candidate did not produce rollback terminal result")
}
