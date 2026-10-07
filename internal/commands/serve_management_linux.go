//go:build linux

package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/inhere/gofer/internal/daemon"
	"github.com/inhere/gofer/internal/procattr"
	"github.com/inhere/gofer/internal/servicemgr"
)

func managedRegisterSpec(opts serveRegisterOptions, spec *servicemgr.Spec) error {
	switch opts.scope {
	case "", "system":
		spec.Backend = servicemgr.BackendSystemdSystem
	case "user":
		spec.Backend = servicemgr.BackendSystemdUser
	default:
		return fmt.Errorf("--scope must be system or user")
	}
	if opts.elevated || opts.adopt {
		return fmt.Errorf("--elevated and --adopt are Windows-only")
	}
	spec.RunAs = opts.runAs
	if spec.Backend == servicemgr.BackendSystemdSystem && spec.RunAs == "" {
		return fmt.Errorf("system scope requires --run-as")
	}
	if spec.Backend == servicemgr.BackendSystemdUser && spec.RunAs != "" {
		return fmt.Errorf("--run-as is only valid with system scope")
	}
	if spec.Backend == servicemgr.BackendSystemdSystem {
		account, err := user.Lookup(spec.RunAs)
		if err != nil {
			return err
		}
		spec.Owner = account.Uid
	} else {
		identity, err := daemon.CurrentProcessIdentity()
		if err != nil {
			return err
		}
		spec.Owner = identity.Owner
	}
	return nil
}

func managedRegister(ctx context.Context, m *servicemgr.Manager, spec servicemgr.Spec, opts serveRegisterOptions) error {
	if err := servicemgr.SystemdRegister(ctx, m, spec); err != nil {
		return err
	}
	if opts.start {
		_, err := servicemgr.SystemdStart(ctx, m)
		return err
	}
	return nil
}

func managedStart(ctx context.Context, m *servicemgr.Manager) error {
	_, err := servicemgr.SystemdStart(ctx, m)
	return err
}
func managedStop(ctx context.Context, m *servicemgr.Manager) error {
	return servicemgr.SystemdStop(ctx, m)
}
func managedRestart(ctx context.Context, m *servicemgr.Manager) error {
	_, err := servicemgr.SystemdRestart(ctx, m)
	return err
}
func managedUninstall(ctx context.Context, m *servicemgr.Manager) error {
	return servicemgr.SystemdUninstall(ctx, m)
}
func managedJournal(ctx context.Context, m *servicemgr.Manager, lines int) ([]byte, error) {
	return servicemgr.SystemdJournal(ctx, m, lines)
}
func managedJournalFollow(ctx context.Context, m *servicemgr.Manager, lines int, out io.Writer) error {
	spec, err := m.LoadSpec()
	if err != nil {
		return err
	}
	if _, err := servicemgr.SystemdStatusOf(ctx, m); err != nil {
		return err
	}
	args := []string{}
	if spec.Backend == servicemgr.BackendSystemdUser {
		args = append(args, "--user")
	}
	args = append(args, "-u", spec.Name+".service", "-n", strconv.Itoa(lines), "-f", "--no-pager")
	cmd := exec.CommandContext(ctx, "journalctl", args...)
	procattr.Background(cmd)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

func managedNativeStatus(ctx context.Context, m *servicemgr.Manager) (nativeServeStatus, error) {
	status, err := servicemgr.SystemdStatusOf(ctx, m)
	return nativeServeStatus{Installed: status.Installed, Active: status.Active, NativeState: status.SubState,
		Server: status.Server, ApplicationLog: status.ApplicationLog}, err
}

func managedNativeExists(ctx context.Context, m *servicemgr.Manager) (bool, error) {
	for _, scope := range [][]string{nil, {"--user"}} {
		args := append(append([]string{}, scope...), "show", m.Name+".service", "--property=LoadState", "--no-pager")
		cmd := exec.CommandContext(ctx, "systemctl", args...)
		procattr.Background(cmd)
		output, err := cmd.CombinedOutput()
		if err != nil {
			if len(scope) != 0 && systemdUserBusUnavailable(string(output)) {
				// A stopped user manager has no transient units. Still inspect its
				// exact unit file before allowing the old pidfile fallback.
				return linuxUnitFileExists(m.Name)
			}
			pid1, readErr := os.ReadFile("/proc/1/comm")
			if readErr != nil || strings.TrimSpace(string(pid1)) == "systemd" {
				return false, fmt.Errorf("check systemd registration: %w: %s", err, strings.TrimSpace(string(output)))
			}
			// In a container without systemd, preserve the existing unregistered
			// pidfile stop only when no exact native unit file claims this name.
			return linuxUnitFileExists(m.Name)
		}
		if !strings.Contains(string(output), "LoadState=not-found") {
			return true, nil
		}
	}
	return false, nil
}

func systemdUserBusUnavailable(output string) bool {
	text := strings.ToLower(output)
	return strings.Contains(text, "failed to connect") && strings.Contains(text, "bus") &&
		(strings.Contains(text, "no such file or directory") || strings.Contains(text, "no medium found"))
}

func linuxUnitFileExists(name string) (bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return false, err
	}
	userConfig := os.Getenv("XDG_CONFIG_HOME")
	if userConfig == "" {
		userConfig = filepath.Join(home, ".config")
	}
	for _, dir := range []string{"/etc/systemd/system", "/usr/lib/systemd/system", "/lib/systemd/system",
		filepath.Join(userConfig, "systemd", "user"), "/etc/systemd/user", "/usr/lib/systemd/user"} {
		_, err := os.Lstat(filepath.Join(dir, name+".service"))
		if err == nil {
			return true, nil
		}
		if !os.IsNotExist(err) {
			return false, err
		}
	}
	return false, nil
}
