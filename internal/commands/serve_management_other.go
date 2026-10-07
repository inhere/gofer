//go:build !windows && !linux

package commands

import (
	"context"
	"errors"
	"io"

	"github.com/inhere/gofer/internal/servicemgr"
)

var errManagedUnsupported = errors.New("native serve management is supported on Windows and Linux")

func managedRegisterSpec(serveRegisterOptions, *servicemgr.Spec) error { return errManagedUnsupported }
func managedRegister(context.Context, *servicemgr.Manager, servicemgr.Spec, serveRegisterOptions) error {
	return errManagedUnsupported
}
func managedStart(context.Context, *servicemgr.Manager) error     { return errManagedUnsupported }
func managedStop(context.Context, *servicemgr.Manager) error      { return errManagedUnsupported }
func managedRestart(context.Context, *servicemgr.Manager) error   { return errManagedUnsupported }
func managedUninstall(context.Context, *servicemgr.Manager) error { return errManagedUnsupported }
func managedJournal(context.Context, *servicemgr.Manager, int) ([]byte, error) {
	return nil, errManagedUnsupported
}
func managedJournalFollow(context.Context, *servicemgr.Manager, int, io.Writer) error {
	return errManagedUnsupported
}
func managedNativeStatus(context.Context, *servicemgr.Manager) (nativeServeStatus, error) {
	return nativeServeStatus{}, errManagedUnsupported
}
func managedNativeExists(context.Context, *servicemgr.Manager) (bool, error) {
	return false, errManagedUnsupported
}
