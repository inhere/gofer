//go:build !windows && !linux

package servicemgr

import (
	"context"
	"errors"

	"github.com/inhere/gofer/internal/daemon"
)

// errUpgradeUnsupported: managed `serve upgrade` drives a native service
// (Windows SCM task / systemd unit); other platforms have neither, so every
// platform hook refuses instead of half-running an upgrade.
var errUpgradeUnsupported = errors.New("managed service upgrade is supported on Windows and Linux")

func verifyManagedForUpgrade(context.Context, *Manager, Spec) error { return errUpgradeUnsupported }

func launchIndependentHelper(context.Context, *Manager, Spec, string, string) error {
	return errUpgradeUnsupported
}

func inspectIndependentHelper(*Manager, Spec, string, daemon.ProcessIdentity) (bool, error) {
	return false, errUpgradeUnsupported
}

func platformStopForUpgrade(context.Context, *Manager) error     { return errUpgradeUnsupported }
func platformStartForUpgrade(context.Context, *Manager) error    { return errUpgradeUnsupported }
func refreshSupervisorForUpgrade(*Manager, Spec) error           { return errUpgradeUnsupported }
func prepareRollbackStart(context.Context, *Manager, Spec) error { return errUpgradeUnsupported }
func ownsListeningPort(int, string, uint16) (bool, error)        { return false, errUpgradeUnsupported }
func backupSupervisorForUpgrade(*Manager, Spec) (func() error, func(), error) {
	return nil, nil, errUpgradeUnsupported
}
