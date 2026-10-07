package serve

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/inhere/gofer/internal/daemon"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/servicemgr"
)

const upgradeDrainPoll = 100 * time.Millisecond

// startUpgradeDrainBridge is the server-owned half of the local control file.
// It accepts a transaction only after native identity, admission quiescence,
// verified source exclusion, and durable in-flight census all succeed.
func startUpgradeDrainBridge(jobs *job.Service, meta *jobstore.Store, manager *servicemgr.Manager, stop <-chan struct{}) error {
	initial, readErr := manager.LoadUpgradeControl()
	activeID, accepted, err := initializeUpgradeAdmission(jobs, initial, readErr, time.Now())
	if err != nil {
		return err
	}
	go func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			select {
			case <-stop:
				cancel()
			case <-ctx.Done():
			}
		}()
		ticker := time.NewTicker(upgradeDrainPoll)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			control, err := manager.LoadUpgradeControl()
			if errors.Is(err, os.ErrNotExist) {
				activeID, accepted = reconcileMissingUpgradeControl(jobs, manager, activeID, accepted)
				continue
			}
			if err != nil {
				if activeID == "" {
					_ = jobs.CloseUpgradeAdmission(ctx, "invalid-upgrade-control")
					activeID = jobs.UpgradeAdmissionOwner()
				}
				slog.Error("upgrade control unreadable; admission closed", "error", err)
				continue
			}
			if activeID != "" && activeID != control.UpgradeID {
				if accepted {
					continue
				}
				_ = jobs.OpenUpgradeAdmission(activeID)
				activeID = ""
			}
			if control.Phase != servicemgr.UpgradeDraining {
				accepted = true
				if activeID == "" {
					_ = jobs.CloseUpgradeAdmission(ctx, control.UpgradeID)
					activeID = jobs.UpgradeAdmissionOwner()
				}
				continue
			}
			if !time.Now().Before(control.Deadline) {
				if activeID == control.UpgradeID {
					_ = jobs.OpenUpgradeAdmission(activeID)
					activeID = ""
				}
				continue
			}
			if err := manager.VerifyUpgradeTarget(ctx); err != nil {
				slog.Warn("upgrade target identity not verified", "upgrade_id", control.UpgradeID, "error", err)
				continue
			}
			server, err := manager.InspectServer()
			if err != nil {
				continue
			}
			self, err := daemon.CurrentProcessIdentity()
			if err != nil || self != server {
				continue
			}
			waitCtx, done := context.WithDeadline(ctx, control.Deadline)
			err = jobs.CloseUpgradeAdmission(waitCtx, control.UpgradeID)
			done()
			activeID = jobs.UpgradeAdmissionOwner()
			if err != nil {
				continue
			}
			verifiedSource := ""
			if control.SourceJobID != "" {
				if err := jobs.ValidateUpgradeSource(control.SourceJobID, control.InitiatorPID); err != nil {
					continue
				}
				verifiedSource = control.SourceJobID
			}
			idle, err := jobs.UpgradeIdleSessionIDs()
			if err != nil {
				continue
			}
			excluded := append(idle, verifiedSource)
			count, err := meta.CountUpgradeInFlightExcluding(excluded)
			if err != nil || count != 0 {
				continue
			}
			if acceptErr := manager.AcceptUpgradeControl(control.UpgradeID, verifiedSource); acceptErr != nil {
				slog.Warn("upgrade acceptance failed", "upgrade_id", control.UpgradeID, "error", acceptErr)
			} else {
				accepted = true
			}
		}
	}()
	return nil
}

func reconcileMissingUpgradeControl(jobs *job.Service, manager *servicemgr.Manager, activeID string, accepted bool) (string, bool) {
	if activeID == "" {
		return "", false
	}
	if accepted {
		receipt, err := manager.LoadUpgradeReceipt(activeID)
		if err != nil || (receipt.Phase != servicemgr.UpgradeFailed && receipt.Phase != servicemgr.UpgradeRolledBack && receipt.Phase != servicemgr.UpgradeSucceeded) {
			return activeID, true // accepted work cannot reopen on missing control alone
		}
	}
	if err := jobs.OpenUpgradeAdmission(activeID); err != nil {
		return activeID, accepted
	}
	return "", false
}

// initializeUpgradeAdmission runs synchronously before any serve producer loop.
// A control left by a previous process cannot get one startup tick of admission.
func initializeUpgradeAdmission(jobs *job.Service, control servicemgr.UpgradeControl, readErr error, now time.Time) (string, bool, error) {
	if errors.Is(readErr, os.ErrNotExist) {
		return "", false, nil
	}
	if readErr != nil {
		err := jobs.CloseUpgradeAdmission(context.Background(), "invalid-upgrade-control")
		return jobs.UpgradeAdmissionOwner(), false, err
	}
	if control.Phase == servicemgr.UpgradeDraining && !now.Before(control.Deadline) {
		return "", false, nil
	}
	err := jobs.CloseUpgradeAdmission(context.Background(), control.UpgradeID)
	return jobs.UpgradeAdmissionOwner(), control.Phase != servicemgr.UpgradeDraining, err
}
