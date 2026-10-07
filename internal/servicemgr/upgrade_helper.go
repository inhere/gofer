package servicemgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/daemon"
	"github.com/inhere/gofer/internal/util"
)

var stopManagedForUpgrade = platformStopForUpgrade

// RunUpgradeHelper is the private process entry. It must see a precommitted
// receipt and gate, publish its own identity, then wait for the server bridge's
// durable accepted transition. No old process is stopped before that point.
func RunUpgradeHelper(ctx context.Context, receiptPath string) error {
	if !filepath.IsAbs(receiptPath) {
		return errors.New("upgrade receipt path must be absolute")
	}
	id := strings.TrimSuffix(filepath.Base(receiptPath), ".json")
	configDir := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Clean(receiptPath))))
	// Name is read from the receipt only after checking its path and schema.
	data, err := os.ReadFile(receiptPath)
	if err != nil {
		return err
	}
	var header struct {
		Name      string `json:"name"`
		UpgradeID string `json:"upgrade_id"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return err
	}
	if header.UpgradeID != id {
		return ErrIdentityMismatch
	}
	m, err := NewManager(configDir, header.Name)
	if err != nil {
		return err
	}
	expectedPath, err := m.UpgradeReceiptPath(id)
	if err != nil || expectedPath != filepath.Clean(receiptPath) {
		return ErrIdentityMismatch
	}
	stored, err := m.LoadUpgradeReceipt(id)
	if err != nil {
		return err
	}
	spec, err := m.LoadSpec()
	if err != nil {
		return err
	}
	self, err := daemon.CurrentProcessIdentity()
	if err != nil {
		return err
	}
	if !daemon.SameExecutable(self.Exe, helperImagePath(m, id)) || self.Owner != spec.Owner {
		return ErrIdentityMismatch
	}
	lock, err := m.Acquire()
	if err != nil {
		return err
	}
	control, err := m.LoadUpgradeControl()
	if err != nil || control.UpgradeID != id || control.Phase != UpgradeDraining {
		lock.Release()
		return fmt.Errorf("upgrade control not in draining phase: %v", err)
	}
	stored.Helper = self
	if err := m.SaveUpgradeReceipt(stored); err != nil {
		lock.Release()
		return err
	}
	lock.Release()
	for {
		if ctx.Err() != nil {
			return finishUpgrade(m, stored, UpgradeFailed, ctx.Err())
		}
		control, err = m.LoadUpgradeControl()
		if err != nil {
			return finishUpgrade(m, stored, UpgradeFailed, err)
		}
		if control.UpgradeID != id {
			return finishUpgrade(m, stored, UpgradeFailed, ErrIdentityMismatch)
		}
		if time.Now().After(control.Deadline) {
			return finishUpgrade(m, stored, UpgradeFailed, errors.New("upgrade drain deadline expired"))
		}
		if control.Phase == UpgradeAccepted {
			break
		}
		if control.Phase != UpgradeDraining {
			return finishUpgrade(m, stored, UpgradeFailed, fmt.Errorf("unexpected upgrade gate phase %q", control.Phase))
		}
		select {
		case <-ctx.Done():
			return finishUpgrade(m, stored, UpgradeFailed, ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
	if control.SourceJobID != "" {
		for {
			initiator, inspectErr := daemon.InspectProcess(control.InitiatorPID)
			if inspectErr != nil || initiator.StartID != control.InitiatorStartID {
				break
			}
			if time.Now().After(control.Deadline) {
				return finishUpgrade(m, stored, UpgradeFailed, errors.New("source job did not exit after acceptance"))
			}
			select {
			case <-ctx.Done():
				return finishUpgrade(m, stored, UpgradeFailed, ctx.Err())
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	return executeAcceptedUpgrade(ctx, m, spec, stored)
}

func executeAcceptedUpgrade(ctx context.Context, m *Manager, spec Spec, receipt UpgradeReceipt) error {
	lock, err := m.Acquire()
	if err != nil {
		return finishUpgrade(m, receipt, UpgradeFailed, err)
	}
	control, err := m.LoadUpgradeControl()
	if err != nil || control.UpgradeID != receipt.UpgradeID || control.Phase != UpgradeAccepted {
		lock.Release()
		return finishUpgrade(m, receipt, UpgradeFailed, fmt.Errorf("upgrade acceptance changed: %v", err))
	}
	if live, err := inspectIndependentHelper(m, spec, receipt.UpgradeID, receipt.Helper); err != nil || !live {
		lock.Release()
		return finishUpgrade(m, receipt, UpgradeFailed, fmt.Errorf("helper isolation not verified: %v", err))
	}
	if err := verifyManagedForUpgrade(ctx, m, spec); err != nil {
		lock.Release()
		return finishUpgrade(m, receipt, UpgradeFailed, err)
	}
	if err := util.VerifyUpgradeFile(receipt.CandidatePath, fileSize(receipt.CandidatePath), receipt.CandidateSHA256); err != nil {
		lock.Release()
		return finishUpgrade(m, receipt, UpgradeFailed, err)
	}
	control.Phase = UpgradeSwitching
	receipt.SourceJobID = control.SourceJobID
	receipt.Phase = UpgradeSwitching
	acceptedAt := time.Now()
	receipt.AcceptedAt = &acceptedAt
	if err := m.SaveUpgradeControl(control); err != nil {
		lock.Release()
		return finishUpgrade(m, receipt, UpgradeFailed, err)
	}
	if err := m.SaveUpgradeReceipt(receipt); err != nil {
		lock.Release()
		return finishUpgrade(m, receipt, UpgradeFailed, err)
	}
	lock.Release()
	if err := stopManagedForUpgrade(ctx, m); err != nil {
		return finishUpgrade(m, receipt, UpgradeFailed, fmt.Errorf("stop old service: %w", err))
	}
	restoreSupervisor, cleanupSupervisor, err := backupSupervisorForUpgrade(m, spec)
	if err != nil {
		return rollbackStoppedService(ctx, m, spec, receipt, nil, nil, err)
	}
	defer cleanupSupervisor()
	rollbackBinary, err := util.SwitchBinary(spec.Exe, receipt.CandidatePath)
	if err != nil {
		return rollbackStoppedService(ctx, m, spec, receipt, nil, restoreSupervisor, err)
	}
	if err := refreshSupervisorForUpgrade(m, spec); err != nil {
		return rollbackStoppedService(ctx, m, spec, receipt, rollbackBinary, restoreSupervisor, err)
	}
	nextSpec := spec
	nextSpec.RegisteredVersion = receipt.CandidateVersion
	if err := m.SaveSpec(nextSpec); err != nil {
		return rollbackStoppedService(ctx, m, spec, receipt, rollbackBinary, restoreSupervisor, err)
	}
	if err := platformStartForUpgrade(ctx, m); err != nil {
		return rollbackStoppedService(ctx, m, spec, receipt, rollbackBinary, restoreSupervisor, err)
	}
	if err := waitManagedHealthy(ctx, m, spec, receipt.CandidateSHA256, 20*time.Second); err != nil {
		return rollbackStoppedService(ctx, m, spec, receipt, rollbackBinary, restoreSupervisor, err)
	}
	return finishUpgrade(m, receipt, UpgradeSucceeded, nil)
}

func rollbackStoppedService(ctx context.Context, m *Manager, spec Spec, receipt UpgradeReceipt, rollbackBinary, restoreSupervisor func() error, cause error) error {
	// A candidate that started must be stopped through its verified OS owner.
	stopErr := stopManagedForUpgrade(ctx, m)
	if stopErr != nil {
		// A still-live candidate may be using both program files. Preserve all
		// backups for operator diagnosis instead of racing its executable image.
		return finishUpgrade(m, receipt, UpgradeFailed, errors.Join(cause, stopErr))
	}
	var binaryErr, supervisorErr error
	if rollbackBinary != nil {
		binaryErr = rollbackBinary()
	}
	if restoreSupervisor != nil {
		supervisorErr = restoreSupervisor()
	}
	specErr := m.SaveSpec(spec)
	combined := errors.Join(cause, stopErr, binaryErr, supervisorErr, specErr)
	if binaryErr != nil || supervisorErr != nil || stopErr != nil || specErr != nil {
		return finishUpgrade(m, receipt, UpgradeFailed, combined)
	}
	startErr := prepareRollbackStart(ctx, m, spec)
	if startErr == nil { startErr = platformStartForUpgrade(ctx, m) }
	if startErr == nil {
		startErr = waitManagedHealthy(ctx, m, spec, "", 20*time.Second)
	}
	combined = errors.Join(combined, startErr)
	if startErr != nil {
		return finishUpgrade(m, receipt, UpgradeFailed, combined)
	}
	return finishUpgrade(m, receipt, UpgradeRolledBack, combined)
}

func finishUpgrade(m *Manager, receipt UpgradeReceipt, phase UpgradePhase, cause error) error {
	lock, err := m.Acquire()
	if err != nil {
		return errors.Join(cause, err)
	}
	defer lock.Release()
	now := time.Now()
	receipt.Phase, receipt.FinishedAt = phase, &now
	if cause != nil {
		receipt.Error = cause.Error()
	}
	writeErr := m.SaveUpgradeReceipt(receipt)
	control, controlErr := m.LoadUpgradeControl()
	if controlErr == nil && control.UpgradeID == receipt.UpgradeID {
		controlErr = os.Remove(m.UpgradeControlPath())
	}
	if errors.Is(controlErr, os.ErrNotExist) {
		controlErr = nil
	}
	return errors.Join(cause, writeErr, controlErr)
}

func fileSize(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return -1
	}
	return st.Size()
}

func waitManagedHealthy(ctx context.Context, m *Manager, spec Spec, sha string, limit time.Duration) error {
	addr := spec.Serve.Addr
	if addr == "" {
		cfg, _, err := config.Load(spec.ConfigFile)
		if err != nil {
			return err
		}
		addr = cfg.Server.Addr
	}
	if addr == "" {
		return errors.New("server address unavailable for upgrade health check")
	}
	bindHost, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid managed server address %q: %w", addr, err)
	}
	portNumber, err := strconv.Atoi(portText)
	if err != nil || portNumber <= 0 || portNumber > 65535 {
		return fmt.Errorf("invalid managed server port %q", portText)
	}
	if strings.HasPrefix(addr, "0.0.0.0:") {
		addr = "127.0.0.1:" + strings.TrimPrefix(addr, "0.0.0.0:")
	}
	if strings.HasPrefix(addr, "[::]:") {
		addr = "127.0.0.1:" + strings.TrimPrefix(addr, "[::]:")
	}
	client := http.Client{Timeout: time.Second}
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		identity, err := m.InspectServer()
		if err == nil && identity.PID > 0 {
			ownsPort, ownerErr := ownsListeningPort(identity.PID, bindHost, uint16(portNumber))
			if ownerErr != nil {
				return ownerErr
			}
			if !ownsPort {
				time.Sleep(200 * time.Millisecond)
				continue
			}
			if sha != "" {
				if err := util.VerifyUpgradeFile(spec.Exe, fileSize(spec.Exe), sha); err != nil {
					return err
				}
			}
			response, reqErr := client.Get("http://" + addr + "/health")
			if reqErr == nil {
				response.Body.Close()
				if response.StatusCode == http.StatusOK {
					// Guard against an unrelated listener observed while our process is
					// still starting: require the same verified process to remain alive.
					time.Sleep(200 * time.Millisecond)
					if again, checkErr := m.InspectServer(); checkErr == nil && again == identity {
						if stillOwns, ownerErr := ownsListeningPort(identity.PID, bindHost, uint16(portNumber)); ownerErr == nil && stillOwns {
							return nil
						}
					}
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("managed server did not become verified and healthy within %s", limit)
}
