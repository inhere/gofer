//go:build windows

package commands

import (
	"context"
	"fmt"
	"io"

	"github.com/inhere/gofer/internal/daemon"
	"github.com/inhere/gofer/internal/servicemgr"
)

func managedRegisterSpec(opts serveRegisterOptions, spec *servicemgr.Spec) error {
	if opts.scope != "" || opts.runAs != "" {
		return fmt.Errorf("--scope and --run-as are Linux-only")
	}
	spec.Backend = servicemgr.BackendWindowsTask
	spec.Elevated = opts.elevated
	identity, err := daemon.CurrentProcessIdentity()
	if err != nil {
		return err
	}
	spec.Owner = identity.Owner
	return nil
}

func managedRegister(_ context.Context, m *servicemgr.Manager, spec servicemgr.Spec, opts serveRegisterOptions) error {
	return m.WindowsRegister(spec, servicemgr.WindowsRegisterOptions{Adopt: opts.adopt, Start: opts.start})
}
func managedStart(_ context.Context, m *servicemgr.Manager) error { return m.WindowsStart() }
func managedStop(_ context.Context, m *servicemgr.Manager) error  { return m.WindowsStop() }
func managedRestart(ctx context.Context, m *servicemgr.Manager) error {
	if err := managedStop(ctx, m); err != nil {
		return err
	}
	return managedStart(ctx, m)
}
func managedUninstall(_ context.Context, m *servicemgr.Manager) error { return m.WindowsUninstall() }
func managedJournal(context.Context, *servicemgr.Manager, int) ([]byte, error) {
	return nil, fmt.Errorf("journal is Linux-only")
}
func managedJournalFollow(context.Context, *servicemgr.Manager, int, io.Writer) error {
	return fmt.Errorf("journal is Linux-only")
}
func managedNativeStatus(_ context.Context, m *servicemgr.Manager) (nativeServeStatus, error) {
	status, err := m.WindowsStatus()
	return nativeServeStatus{Installed: status.Registered, Active: status.TaskState == 4, NativeState: fmt.Sprint(status.TaskState),
		Server: status.Server, Supervisor: status.Supervisor, IdentityError: status.IdentityError}, err
}
func managedNativeExists(_ context.Context, m *servicemgr.Manager) (bool, error) {
	return m.WindowsTaskExists()
}
