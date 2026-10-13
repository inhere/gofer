//go:build windows || linux

package servicemgr

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
)

func TestRollbackRefusesFileChangesWhenStopFails(t *testing.T) {
	m, spec := fixtureSpec(t)
	id := "fedcba9876543210fedcba9876543210"
	receipt := UpgradeReceipt{SchemaVersion: UpgradeSchema, UpgradeID: id, Name: m.Name,
		Phase: UpgradeSwitching, CandidateSHA256: "hash", CandidateVersion: "candidate", StartedAt: time.Now()}
	oldStop := stopManagedForUpgrade
	defer func() { stopManagedForUpgrade = oldStop }()
	stopManagedForUpgrade = func(context.Context, *Manager) error { return errors.New("injected stop refusal") }
	var switched, restored bool
	err := rollbackStoppedService(context.Background(), m, spec, receipt,
		func() error { switched = true; return nil }, func() error { restored = true; return nil }, errors.New("candidate failed"))
	assert.Err(t, err)
	assert.Eq(t, false, switched)
	assert.Eq(t, false, restored)
	stored, err := m.LoadUpgradeReceipt(id)
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, UpgradeFailed, stored.Phase)
}

func TestHelperSourceUsesCurrentCLIImage(t *testing.T) {
	m, spec := fixtureSpec(t)
	old := os.Getenv("GOFER_T5_HELPER_OLD_BINARY")
	currentCLI := os.Getenv("GOFER_T5_HELPER_NEW_BINARY")
	if old == "" || currentCLI == "" {
		t.Skip("set two Gofer CLI build paths for helper source identity")
	}
	spec.Exe = old
	request := UpgradeRequest{HelperExecutable: currentCLI}
	selected := helperSource(spec, request)
	assert.Eq(t, currentCLI, selected)
	assert.Require(t, assert.NoErr(t, checkHelperExecutable(context.Background(), selected)))
	helper := filepath.Join(m.ConfigDir, "helper"+filepath.Ext(currentCLI))
	assert.Require(t, assert.NoErr(t, copyHelperImage(selected, helper)))
	data, err := os.ReadFile(helper)
	assert.Require(t, assert.NoErr(t, err))
	newData, err := os.ReadFile(currentCLI)
	assert.Require(t, assert.NoErr(t, err))
	oldData, err := os.ReadFile(old)
	assert.Require(t, assert.NoErr(t, err))
	gotHash, newHash, oldHash := sha256.Sum256(data), sha256.Sum256(newData), sha256.Sum256(oldData)
	assert.Eq(t, newHash, gotHash)
	assert.Eq(t, false, oldHash == gotHash)
	assert.Eq(t, old, helperSource(spec, UpgradeRequest{}))
}

func TestCandidateVersionFromGoferBinaryIsPlain(t *testing.T) {
	path := os.Getenv("GOFER_TEST_VERSION_BINARY")
	if path == "" {
		t.Skip("set a built Gofer CLI image for version output verification")
	}
	version, err := checkCandidate(context.Background(), path)
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, "t5-new", version)
}

func TestUpgradeControlAndReceiptPersistAcrossManagerInstances(t *testing.T) {
	m, _ := fixtureSpec(t)
	id := "0123456789abcdef0123456789abcdef"
	control := UpgradeControl{SchemaVersion: UpgradeSchema, UpgradeID: id, Name: m.Name,
		InitiatorPID: os.Getpid(), InitiatorStartID: "test-birth", Deadline: time.Now().Add(time.Minute), Phase: UpgradeDraining}
	assert.Require(t, assert.NoErr(t, m.SaveUpgradeControl(control)))
	receipt := UpgradeReceipt{SchemaVersion: UpgradeSchema, UpgradeID: id, Name: m.Name,
		Phase: UpgradeDraining, CandidateSHA256: "hash", CandidateVersion: "v2", StartedAt: time.Now()}
	assert.Require(t, assert.NoErr(t, m.SaveUpgradeReceipt(receipt)))
	again, err := NewManager(m.ConfigDir, m.Name)
	assert.Require(t, assert.NoErr(t, err))
	readControl, err := again.LoadUpgradeControl()
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, control.UpgradeID, readControl.UpgradeID)
	assert.Eq(t, control.Phase, readControl.Phase)
	assert.Eq(t, true, control.Deadline.Equal(readControl.Deadline))
	readReceipt, err := again.LoadUpgradeReceipt(id)
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, receipt.UpgradeID, readReceipt.UpgradeID)
	assert.Eq(t, receipt.Phase, readReceipt.Phase)
	assert.Eq(t, true, receipt.StartedAt.Equal(readReceipt.StartedAt))
	if err := again.AcceptUpgradeControl(id, "job-unverified"); err == nil {
		t.Fatal("missing live helper was accepted")
	}
	readControl, err = again.LoadUpgradeControl()
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, UpgradeDraining, readControl.Phase)
}

func TestUpgradeControlCorruptionFailsClosed(t *testing.T) {
	m, _ := fixtureSpec(t)
	_, err := m.LoadUpgradeControl()
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing control = %v", err)
	}
	assert.Require(t, assert.NoErr(t, os.MkdirAll(m.serviceDir(), 0o700)))
	assert.Require(t, assert.NoErr(t, os.WriteFile(m.UpgradeControlPath(), []byte("{broken"), 0o600)))
	if _, err := m.LoadUpgradeControl(); err == nil {
		t.Fatal("corrupt control was treated as no gate")
	}
}

// A supervisor copy that cannot be restored (Windows keeps the stopped candidate's
// image open for a moment) is retried, and the service is started again even when it
// stays unrestored: the old program is back, so leaving the service stopped is worse.
func TestRollbackStartsServiceWhenSupervisorRestoreFails(t *testing.T) {
	m, spec := fixtureSpec(t)
	assert.Require(t, assert.NoErr(t, m.SaveSpec(spec)))
	id := "0123456789abcdef0123456789abcdef"
	receipt := UpgradeReceipt{SchemaVersion: UpgradeSchema, UpgradeID: id, Name: m.Name,
		Phase: UpgradeSwitching, CandidateSHA256: "hash", CandidateVersion: "candidate", StartedAt: time.Now()}
	oldStop, oldStart, oldRetries, oldDelay := stopManagedForUpgrade, startManagedForUpgrade, restoreRetries, restoreRetryDelay
	defer func() {
		stopManagedForUpgrade, startManagedForUpgrade, restoreRetries, restoreRetryDelay = oldStop, oldStart, oldRetries, oldDelay
	}()
	stopManagedForUpgrade = func(context.Context, *Manager) error { return nil }
	started := 0
	startManagedForUpgrade = func(context.Context, *Manager) error { started++; return errors.New("injected start result") }
	restoreRetries, restoreRetryDelay = 3, time.Millisecond
	attempts := 0
	err := rollbackStoppedService(context.Background(), m, spec, receipt,
		func() error { return nil },
		func() error { attempts++; return errors.New("Access is denied") },
		errors.New("candidate failed"))
	assert.Err(t, err)
	assert.Eq(t, 3, attempts)
	assert.Eq(t, 1, started)
	stored, err := m.LoadUpgradeReceipt(id)
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, UpgradeFailed, stored.Phase)

	// A restore that succeeds on a retry is a normal rollback.
	attempts = 0
	assert.Require(t, assert.NoErr(t, retryRestore(context.Background(), func() error {
		attempts++
		if attempts < 2 {
			return errors.New("Access is denied")
		}
		return nil
	})))
	assert.Eq(t, 2, attempts)
}

// An upgrade is refused while the old server still runs when the config would not
// load in the new one (a half-written edit).
func TestBeginUpgradeRefusesConfigThatDoesNotLoad(t *testing.T) {
	m, spec := fixtureSpec(t)
	assert.Require(t, assert.NoErr(t, m.SaveSpec(spec)))
	assert.Require(t, assert.NoErr(t, os.WriteFile(spec.ConfigFile, []byte("server: {}\nhttp_proxy\nsteward:\n  enabled: true\n"), 0o600)))
	_, err := BeginUpgrade(context.Background(), m, UpgradeRequest{
		CandidatePath: spec.Exe, Size: 1, SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Deadline: time.Now().Add(time.Minute),
	})
	assert.Err(t, err)
	assert.StrContains(t, err.Error(), "the config does not load")
	_, err = m.LoadUpgradeControl()
	assert.Eq(t, true, errors.Is(err, os.ErrNotExist))
}
