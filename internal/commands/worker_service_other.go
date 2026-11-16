//go:build !windows && !linux

package commands

import (
	"context"
	"errors"

	"github.com/inhere/gofer/internal/servicemgr"
)

// errWorkerServiceUnsupported: macOS has no per-user supervisor gofer can own yet,
// so the answer is daemon mode, which the launcher/managed login item can start.
var errWorkerServiceUnsupported = errors.New("worker service management is not supported on this platform; use `gofer worker -d` instead")

func workerServiceRefuseRoot() error { return nil }

func workerServiceSpecDefaults(*servicemgr.Spec) error { return errWorkerServiceUnsupported }

func workerServiceRegisterNative(context.Context, *servicemgr.Manager, servicemgr.Spec, bool) error {
	return errWorkerServiceUnsupported
}

func workerServiceStartNative(context.Context, *servicemgr.Manager) error {
	return errWorkerServiceUnsupported
}

func workerServiceStopNative(context.Context, *servicemgr.Manager) error {
	return errWorkerServiceUnsupported
}

func workerServiceUninstallNative(context.Context, *servicemgr.Manager) error {
	return errWorkerServiceUnsupported
}

func workerServiceStatusNative(context.Context, *servicemgr.Manager) (workerServiceState, error) {
	return workerServiceState{}, errWorkerServiceUnsupported
}
