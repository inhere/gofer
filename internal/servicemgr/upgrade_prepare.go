package servicemgr

import (
	"context"
	"crypto/rand"
	"debug/buildinfo"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/daemon"
	"github.com/inhere/gofer/internal/procattr"
	"github.com/inhere/gofer/internal/util"
)

type UpgradeRequest struct {
	CandidatePath    string
	HelperExecutable string // internal current management CLI image; never a user-facing flag
	Size             int64
	SHA256           string
	SourceJobID      string // untrusted claim until the server bridge checks local runner PID and birth
	Deadline         time.Time
}

func helperSource(spec Spec, request UpgradeRequest) string {
	if request.HelperExecutable != "" {
		return request.HelperExecutable
	}
	return spec.Exe
}

func checkHelperExecutable(ctx context.Context, path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("helper executable path must be absolute")
	}
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return fmt.Errorf("helper Go build info: %w", err)
	}
	if info.Path != "github.com/inhere/gofer/cmd/gofer" {
		return fmt.Errorf("helper is not a Gofer CLI build: %q", info.Path)
	}
	_, err = checkCandidate(ctx, path)
	return err
}

// VerifyUpgradeTarget uses the platform's owned task/unit and current process
// identity. The server drain bridge calls it before accepting a transaction.
func (m *Manager) VerifyUpgradeTarget(ctx context.Context) error {
	spec, err := m.LoadSpec()
	if err != nil {
		return err
	}
	return verifyManagedForUpgrade(ctx, m, spec)
}

func nextUpgradeID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func checkCandidate(ctx context.Context, path string) (string, error) {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("candidate Go build info: %w", err)
	}
	var goos, arch string
	for _, setting := range info.Settings {
		switch setting.Key {
		case "GOOS":
			goos = setting.Value
		case "GOARCH":
			arch = setting.Value
		}
	}
	if goos != runtime.GOOS || arch != runtime.GOARCH {
		return "", fmt.Errorf("candidate platform %s/%s differs from %s/%s", goos, arch, runtime.GOOS, runtime.GOARCH)
	}
	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(checkCtx, path, "--version")
	procattr.Background(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("candidate --version: %w", err)
	}
	version := strings.TrimSpace(string(out))
	version, err = util.ParseVersionOutput(version)
	if err != nil {
		return "", err
	}
	if len(version) > 256 {
		return "", errors.New("candidate version is too long")
	}
	return version, nil
}

func helperImagePath(m *Manager, id string) string {
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	return filepath.Join(m.ConfigDir, "run", "upgrade", id+"-helper"+ext)
}

// BeginUpgrade prepares and launches a helper; it returns only after that
// independent process has recorded its own live identity. The server bridge
// still has to drain and AcceptUpgradeControl before any stop or switch.
func BeginUpgrade(ctx context.Context, m *Manager, request UpgradeRequest) (UpgradeReceipt, error) {
	var receipt UpgradeReceipt
	if request.CandidatePath == "" || !filepath.IsAbs(request.CandidatePath) || len(request.SHA256) != 64 || request.Size <= 0 || request.Deadline.Before(time.Now()) {
		return receipt, errors.New("candidate absolute path, size, SHA-256 and future deadline are required")
	}
	spec, err := m.LoadSpec()
	if err != nil {
		return receipt, err
	}
	if err := spec.CheckFiles(); err != nil {
		return receipt, err
	}
	helperExecutable := helperSource(spec, request)
	if err := checkHelperExecutable(ctx, helperExecutable); err != nil {
		return receipt, err
	}
	if err := verifyManagedForUpgrade(ctx, m, spec); err != nil {
		return receipt, err
	}
	if err := util.VerifyUpgradeFile(request.CandidatePath, request.Size, request.SHA256); err != nil {
		return receipt, err
	}
	version, err := checkCandidate(ctx, request.CandidatePath)
	if err != nil {
		return receipt, err
	}
	id, err := nextUpgradeID()
	if err != nil {
		return receipt, err
	}
	staged, err := util.StageUpgradeFile(request.CandidatePath, spec.Exe, id, request.Size, request.SHA256)
	if err != nil {
		return receipt, err
	}
	prepared := false
	defer func() {
		if !prepared {
			_ = os.Remove(staged)
		}
	}()
	lock, err := m.Acquire()
	if err != nil {
		return receipt, err
	}
	if _, err := m.LoadUpgradeControl(); err == nil {
		lock.Release()
		return receipt, errors.New("another upgrade control already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		lock.Release()
		return receipt, err
	}
	initiator, err := daemon.CurrentProcessIdentity()
	if err != nil {
		lock.Release()
		return receipt, err
	}
	control := UpgradeControl{SchemaVersion: UpgradeSchema, UpgradeID: id, Name: m.Name, SourceJobID: request.SourceJobID,
		InitiatorPID: initiator.PID, InitiatorStartID: initiator.StartID, Deadline: request.Deadline, Phase: UpgradeDraining}
	receipt = UpgradeReceipt{SchemaVersion: UpgradeSchema, UpgradeID: id, Name: m.Name, Phase: UpgradeDraining, CandidateSHA256: request.SHA256,
		CandidateVersion: version, PreviousVersion: spec.RegisteredVersion, CandidatePath: staged, StartedAt: time.Now()}
	if err := m.SaveUpgradeReceipt(receipt); err != nil {
		lock.Release()
		return receipt, err
	}
	if err := m.SaveUpgradeControl(control); err != nil {
		lock.Release()
		return receipt, err
	}
	lock.Release()
	prepared = true
	if err := copyHelperImage(helperExecutable, helperImagePath(m, id)); err != nil {
		_ = failPreparedUpgrade(m, receipt, err)
		return receipt, err
	}
	path, _ := m.UpgradeReceiptPath(id)
	if err := launchIndependentHelper(ctx, m, spec, helperImagePath(m, id), path); err != nil {
		_ = failPreparedUpgrade(m, receipt, err)
		return receipt, err
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		current, err := m.LoadUpgradeReceipt(id)
		if err != nil {
			return receipt, failPreparedUpgrade(m, receipt, err)
		}
		if current.Helper.PID > 0 {
			live, err := inspectIndependentHelper(m, spec, id, current.Helper)
			if err == nil && live {
				return current, nil
			}
			return receipt, failPreparedUpgrade(m, receipt, fmt.Errorf("helper takeover identity invalid: %w", err))
		}
		select {
		case <-ctx.Done():
			return receipt, failPreparedUpgrade(m, receipt, ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	return receipt, failPreparedUpgrade(m, receipt, errors.New("independent upgrade helper did not take over within 8s"))
}

// WaitUpgradeAccepted waits only for the bridge's durable drain/identity ACK.
// A job caller must return this ID and exit; its helper waits for that exact
// initiator process to exit before stopping the old server.
func WaitUpgradeAccepted(ctx context.Context, m *Manager, id string) (UpgradeReceipt, error) {
	for {
		receipt, err := m.LoadUpgradeReceipt(id)
		if err != nil {
			return receipt, err
		}
		control, controlErr := m.LoadUpgradeControl()
		if controlErr == nil && control.UpgradeID == id && (control.Phase == UpgradeAccepted || control.Phase == UpgradeSwitching) {
			return receipt, nil
		}
		if receipt.AcceptedAt != nil || receipt.Phase == UpgradeSwitching || receipt.Phase == UpgradeSucceeded || receipt.Phase == UpgradeRolledBack {
			return receipt, nil
		}
		if receipt.Phase == UpgradeFailed {
			return receipt, fmt.Errorf("upgrade failed before acceptance: %s", receipt.Error)
		}
		if controlErr != nil && !errors.Is(controlErr, os.ErrNotExist) {
			return receipt, controlErr
		}
		if controlErr == nil && control.UpgradeID != id {
			return receipt, ErrIdentityMismatch
		}
		select {
		case <-ctx.Done():
			return receipt, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func copyHelperImage(source, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(target)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(target)
		return err
	}
	return out.Close()
}

func failPreparedUpgrade(m *Manager, receipt UpgradeReceipt, cause error) error {
	lock, err := m.Acquire()
	if err != nil {
		return errors.Join(cause, err)
	}
	defer lock.Release()
	now := time.Now()
	receipt.Phase, receipt.Error, receipt.FinishedAt = UpgradeFailed, cause.Error(), &now
	writeErr := m.SaveUpgradeReceipt(receipt)
	control, readErr := m.LoadUpgradeControl()
	if readErr == nil && control.UpgradeID == receipt.UpgradeID {
		readErr = os.Remove(m.UpgradeControlPath())
	}
	if errors.Is(readErr, os.ErrNotExist) {
		readErr = nil
	}
	return errors.Join(cause, writeErr, readErr)
}
