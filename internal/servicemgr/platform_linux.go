//go:build linux

package servicemgr

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/daemon"
	"github.com/inhere/gofer/internal/procattr"
)

// SystemdStatus combines the native unit state and verified process identity.
type SystemdStatus struct {
	Installed      bool
	Enabled        bool
	Active         bool
	SubState       string
	MainPID        int
	Server         daemon.ProcessIdentity
	UnitPath       string
	ApplicationLog string
}

type systemdRunner func(context.Context, string, ...string) ([]byte, error)

type systemdBackend struct {
	run     systemdRunner
	unitDir string // tests may substitute an isolated unit directory
}

func nativeSystemd() systemdBackend { return systemdBackend{run: runSystemdCommand} }

func runSystemdCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	procattr.Background(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func (b systemdBackend) unitPath(spec Spec) (string, error) {
	if spec.Backend != BackendSystemdSystem && spec.Backend != BackendSystemdUser {
		return "", fmt.Errorf("not a systemd backend: %s", spec.Backend)
	}
	if err := spec.Validate(); err != nil {
		return "", err
	}
	if b.unitDir != "" {
		return filepath.Join(b.unitDir, spec.Name+".service"), nil
	}
	if spec.Backend == BackendSystemdSystem {
		return filepath.Join("/etc/systemd/system", spec.Name+".service"), nil
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	if !filepath.IsAbs(base) {
		return "", errors.New("XDG_CONFIG_HOME must be absolute")
	}
	return filepath.Join(base, "systemd", "user", spec.Name+".service"), nil
}

func systemdScope(spec Spec) []string {
	if spec.Backend == BackendSystemdUser {
		return []string{"--user"}
	}
	return nil
}

func (b systemdBackend) ctl(ctx context.Context, spec Spec, args ...string) ([]byte, error) {
	argv := append(systemdScope(spec), args...)
	return b.run(ctx, "systemctl", argv...)
}

func unitString(value string) (string, error) {
	if strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("control character in systemd unit value")
	}
	return strings.ReplaceAll(value, "%", "%%"), nil
}

func unitArg(value string) (string, error) {
	v, err := unitString(value)
	if err != nil {
		return "", err
	}
	// systemd's ExecStart grammar is not a shell. Quote each entire argument.
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	v = strings.ReplaceAll(v, `$`, `$$`)
	return `"` + v + `"`, nil
}

func unitEnvironment(value string) (string, error) {
	v, err := unitString(value)
	if err != nil {
		return "", err
	}
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	return `"` + v + `"`, nil
}

func unitContent(spec Spec) ([]byte, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if spec.Backend != BackendSystemdSystem && spec.Backend != BackendSystemdUser {
		return nil, errors.New("not a systemd service")
	}
	// daemon.InspectProcess reports numeric UIDs on Linux. Keep the durable
	// owner in that same form, even when User= names the account textually.
	if _, err := user.LookupId(spec.Owner); err != nil {
		return nil, fmt.Errorf("service owner uid: %w", err)
	}
	if spec.Backend == BackendSystemdSystem {
		runAs, err := user.Lookup(spec.RunAs)
		if err != nil {
			return nil, fmt.Errorf("run_as: %w", err)
		}
		if runAs.Uid != spec.Owner {
			return nil, errors.New("system service owner uid and run_as must match")
		}
	}
	if spec.Backend == BackendSystemdUser {
		current, err := user.Current()
		if err != nil {
			return nil, err
		}
		if current.Uid != spec.Owner {
			return nil, errors.New("user service owner must be current user")
		}
	}
	args, err := spec.ServerArgs()
	if err != nil {
		return nil, err
	}
	command := make([]string, 0, len(args)+1)
	for i, arg := range append([]string{spec.Exe}, args...) {
		quoted, err := unitArg(arg)
		if err != nil {
			return nil, err
		}
		// systemd never applies environment substitution to argv[0]; $$ there
		// would be interpreted as two literal dollars in the executable path.
		if i == 0 {
			quoted = strings.ReplaceAll(quoted, "$$", "$")
		}
		command = append(command, quoted)
	}
	workDir, err := unitString(spec.WorkDir)
	if err != nil {
		return nil, err
	}
	// WorkingDirectory is one setting value; spaces need no shell quoting.
	env, err := unitEnvironment("GOFER_CONFIG_DIR=" + spec.ConfigDir)
	if err != nil {
		return nil, err
	}
	if strings.ContainsAny(spec.Owner, "\r\n") {
		return nil, errors.New("invalid service owner")
	}
	var out strings.Builder
	fmt.Fprintf(&out, "# Gofer-Managed: %s\n", spec.Owner)
	out.WriteString("[Unit]\nDescription=Gofer managed server\nAfter=network.target\nStartLimitIntervalSec=60\nStartLimitBurst=5\n\n[Service]\nType=simple\n")
	if spec.Backend == BackendSystemdSystem {
		fmt.Fprintf(&out, "User=%s\n", spec.RunAs)
	}
	fmt.Fprintf(&out, "WorkingDirectory=%s\nEnvironment=%s\nExecStart=%s\n", workDir, env, strings.Join(command, " "))
	out.WriteString("Restart=on-failure\nRestartSec=3s\nTimeoutStopSec=180s\nKillMode=control-group\n")
	out.WriteString("\n[Install]\nWantedBy=")
	if spec.Backend == BackendSystemdUser {
		out.WriteString("default.target\n")
	} else {
		out.WriteString("multi-user.target\n")
	}
	return []byte(out.String()), nil
}

func ownsUnit(data []byte, owner string) bool {
	return bytes.HasPrefix(data, []byte("# Gofer-Managed: "+owner+"\n"))
}

func (b systemdBackend) readOwned(spec Spec) (string, []byte, error) {
	path, err := b.unitPath(spec)
	if err != nil {
		return "", nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return path, nil, err
	}
	if !ownsUnit(data, spec.Owner) {
		return path, nil, fmt.Errorf("unit %s is not owned by this Gofer instance", path)
	}
	return path, data, nil
}

// readRegistered binds the durable spec, file content and systemd's loaded
// fragment. A same-UID marker alone is insufficient to control a native unit.
func (b systemdBackend) readRegistered(ctx context.Context, spec Spec) (string, []byte, error) {
	path, data, err := b.readOwned(spec)
	if err != nil {
		return path, nil, err
	}
	expected, err := unitContent(spec)
	if err != nil {
		return path, nil, err
	}
	if !bytes.Equal(data, expected) {
		return path, nil, fmt.Errorf("%w: unit differs from registered spec", ErrIdentityMismatch)
	}
	out, err := b.ctl(ctx, spec, "show", spec.Name+".service", "--property=LoadState,FragmentPath", "--no-pager")
	if err != nil {
		return path, nil, err
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok {
			values[k] = v
		}
	}
	if values["LoadState"] != "loaded" || filepath.Clean(values["FragmentPath"]) != path {
		return path, nil, fmt.Errorf("%w: systemd loaded fragment %q, expected %q", ErrIdentityMismatch, values["FragmentPath"], path)
	}
	return path, data, nil
}

func writeUnit(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".gofer-unit-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// SystemdRegister installs and enables an owned unit. It never starts it.
func SystemdRegister(ctx context.Context, m *Manager, spec Spec) error {
	return nativeSystemd().register(ctx, m, spec)
}

func (b systemdBackend) register(ctx context.Context, m *Manager, spec Spec) error {
	if err := spec.CheckFiles(); err != nil {
		return err
	}
	unit, err := unitContent(spec)
	if err != nil {
		return err
	}
	if spec.Name != m.Name || spec.ConfigDir != m.ConfigDir {
		return ErrIdentityMismatch
	}
	lock, err := m.Acquire()
	if err != nil {
		return err
	}
	defer lock.Release()
	path, oldUnit, readErr := b.readOwned(spec)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	if readErr != nil {
		out, err := b.ctl(ctx, spec, "show", spec.Name+".service", "--property=LoadState,FragmentPath", "--no-pager")
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(line, "LoadState=") && strings.TrimPrefix(line, "LoadState=") != "not-found" {
				return fmt.Errorf("systemd unit %s already exists outside Gofer ownership", spec.Name)
			}
		}
	}
	oldSpec, specErr := m.LoadSpec()
	if specErr != nil && !errors.Is(specErr, os.ErrNotExist) {
		return specErr
	}
	if readErr == nil && specErr != nil {
		return errors.New("owned systemd unit has no matching Gofer spec")
	}
	if specErr == nil && (oldSpec.Owner != spec.Owner || oldSpec.Backend != spec.Backend) {
		return ErrIdentityMismatch
	}
	if readErr != nil && specErr == nil {
		return errors.New("Gofer spec exists but systemd unit is missing")
	}
	if readErr == nil {
		if _, _, err := b.readRegistered(ctx, oldSpec); err != nil {
			return err
		}
	}
	if readErr == nil && bytes.Equal(oldUnit, unit) && oldSpec == spec {
		status, err := b.status(ctx, m, spec)
		if err != nil {
			return err
		}
		if !status.Enabled {
			_, err = b.ctl(ctx, spec, "enable", spec.Name+".service")
		}
		return err
	}
	oldEnabled := false
	if readErr == nil {
		status, err := b.status(ctx, m, oldSpec)
		if err != nil {
			return err
		}
		if status.Active {
			return ErrRunningSpecChange
		}
		oldEnabled = status.Enabled
	}
	if err := writeUnit(path, unit); err != nil {
		return err
	}
	rollback := func(cause error) error {
		_, disableErr := b.ctl(ctx, spec, "disable", spec.Name+".service")
		if readErr == nil {
			_ = writeUnit(path, oldUnit)
		} else {
			_ = os.Remove(path)
		}
		if specErr == nil {
			_ = m.SaveSpec(oldSpec)
		} else {
			_ = os.Remove(m.SpecPath())
		}
		_, reloadErr := b.ctl(ctx, spec, "daemon-reload")
		if oldEnabled {
			_, enableErr := b.ctl(ctx, oldSpec, "enable", oldSpec.Name+".service")
			return errors.Join(cause, disableErr, reloadErr, enableErr)
		}
		return errors.Join(cause, disableErr, reloadErr)
	}
	if err := m.SaveSpec(spec); err != nil {
		return rollback(err)
	}
	if _, err := b.ctl(ctx, spec, "daemon-reload"); err != nil {
		return rollback(err)
	}
	if _, err := b.ctl(ctx, spec, "enable", spec.Name+".service"); err != nil {
		return rollback(err)
	}
	return nil
}

// SystemdStart starts a previously registered unit and verifies its process.
func SystemdStart(ctx context.Context, m *Manager) (SystemdStatus, error) {
	return nativeSystemd().start(ctx, m)
}

func (b systemdBackend) start(ctx context.Context, m *Manager) (SystemdStatus, error) {
	if _, set := ctx.Deadline(); !set {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
	}
	spec, err := m.LoadSpec()
	if err != nil {
		return SystemdStatus{}, err
	}
	if _, _, err := b.readRegistered(ctx, spec); err != nil {
		return SystemdStatus{}, err
	}
	if _, err := b.ctl(ctx, spec, "start", spec.Name+".service"); err != nil {
		return SystemdStatus{}, err
	}
	var status SystemdStatus
	for {
		status, err = b.status(ctx, m, spec)
		if err == nil && status.Active && status.MainPID > 0 {
			return status, nil
		}
		select {
		case <-ctx.Done():
			return status, errors.Join(fmt.Errorf("systemd start did not reach verified active state: %w", ctx.Err()), err)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// SystemdStop stops through systemctl so Restart=on-failure cannot revive serve.
func SystemdStop(ctx context.Context, m *Manager) error { return nativeSystemd().stop(ctx, m) }

func (b systemdBackend) stop(ctx context.Context, m *Manager) error {
	spec, err := m.LoadSpec()
	if err != nil {
		return err
	}
	if _, _, err := b.readRegistered(ctx, spec); err != nil {
		return err
	}
	if _, err := b.status(ctx, m, spec); err != nil {
		return err
	}
	if _, err := b.ctl(ctx, spec, "stop", spec.Name+".service"); err != nil {
		return err
	}
	status, err := b.status(ctx, m, spec)
	if err != nil {
		return err
	}
	if status.Active {
		return errors.New("systemd unit remains active after stop")
	}
	return nil
}

func SystemdRestart(ctx context.Context, m *Manager) (SystemdStatus, error) {
	if err := SystemdStop(ctx, m); err != nil {
		return SystemdStatus{}, err
	}
	return SystemdStart(ctx, m)
}

func SystemdStatusOf(ctx context.Context, m *Manager) (SystemdStatus, error) {
	spec, err := m.LoadSpec()
	if err != nil {
		return SystemdStatus{}, err
	}
	return nativeSystemd().status(ctx, m, spec)
}

func (b systemdBackend) status(ctx context.Context, m *Manager, spec Spec) (SystemdStatus, error) {
	path, _, err := b.readRegistered(ctx, spec)
	if err != nil {
		return SystemdStatus{}, err
	}
	logPath, err := systemdApplicationLog(spec)
	if err != nil {
		return SystemdStatus{}, err
	}
	status := SystemdStatus{Installed: true, UnitPath: path, ApplicationLog: logPath}
	out, err := b.ctl(ctx, spec, "show", spec.Name+".service", "--property=ActiveState,SubState,MainPID,UnitFileState,InvocationID", "--no-pager")
	if err != nil {
		return status, err
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok {
			values[k] = v
		}
	}
	status.Enabled = values["UnitFileState"] == "enabled"
	status.Active = values["ActiveState"] == "active"
	status.SubState = values["SubState"]
	if values["InvocationID"] == "" && status.Active {
		return status, errors.New("active systemd unit has no InvocationID")
	}
	status.MainPID, err = strconv.Atoi(values["MainPID"])
	if err != nil {
		return status, fmt.Errorf("invalid systemd MainPID: %w", err)
	}
	if status.Active {
		if status.MainPID <= 0 {
			return status, errors.New("active unit has no MainPID")
		}
		identity, err := daemon.InspectProcess(status.MainPID)
		if err != nil {
			return status, err
		}
		if !daemon.SameExecutable(identity.Exe, spec.Exe) || identity.Owner != spec.Owner {
			return status, fmt.Errorf("%w: systemd MainPID=%d", ErrIdentityMismatch, status.MainPID)
		}
		cmdline, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(status.MainPID), "cmdline"))
		if err != nil {
			return status, err
		}
		args, err := spec.ServerArgs()
		if err != nil {
			return status, err
		}
		actual := strings.Split(strings.TrimRight(string(cmdline), "\x00"), "\x00")
		if len(actual) != len(args)+1 || actual[0] != spec.Exe {
			return status, ErrIdentityMismatch
		}
		for i, arg := range args {
			if actual[i+1] != arg {
				return status, ErrIdentityMismatch
			}
		}
		status.Server = identity
	}
	// systemd may have restarted a service while this client was away. Recheck
	// its native PID and process birth before recording the reconciled identity.
	confirm, err := b.ctl(ctx, spec, "show", spec.Name+".service", "--property=ActiveState,MainPID,InvocationID", "--no-pager")
	if err != nil {
		return status, err
	}
	if !bytes.Contains(confirm, []byte("ActiveState="+values["ActiveState"]+"\n")) ||
		!bytes.Contains(confirm, []byte("MainPID="+values["MainPID"]+"\n")) ||
		!bytes.Contains(confirm, []byte("InvocationID="+values["InvocationID"]+"\n")) {
		return status, errors.New("systemd unit changed during identity inspection")
	}
	if status.Active {
		stable, err := daemon.InspectProcess(status.MainPID)
		if err != nil {
			return status, err
		}
		if stable.StartID != status.Server.StartID {
			return status, ErrIdentityMismatch
		}
	}
	state, err := m.LoadState()
	if errors.Is(err, os.ErrNotExist) {
		state = State{SchemaVersion: StateSchema, Name: m.Name}
	} else if err != nil {
		return status, err
	}
	if status.Active && state.NativeInvocationID != "" && state.NativeInvocationID == values["InvocationID"] && state.Server.PID != 0 && state.Server != status.Server {
		return status, fmt.Errorf("%w: process changed within systemd invocation", ErrIdentityMismatch)
	}
	if status.Active {
		state.NativeInvocationID = values["InvocationID"]
		state.Server = status.Server
	}
	if err := m.SaveState(state); err != nil {
		return status, err
	}
	return status, nil
}

func systemdApplicationLog(spec Spec) (string, error) {
	data, err := os.ReadFile(spec.ConfigFile)
	if err != nil {
		return "", err
	}
	var cfg struct {
		Log struct {
			File string `yaml:"file"`
			Dir  string `yaml:"dir"`
		} `yaml:"log"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return "", err
	}
	if cfg.Log.File != "" {
		path, err := config.ResolveLocalPathAt("log.file", cfg.Log.File, spec.ConfigDir)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(spec.WorkDir, path)
		}
		return path, nil
	}
	if cfg.Log.Dir != "" {
		dir, err := config.ResolveLocalPathAt("log.dir", cfg.Log.Dir, spec.ConfigDir)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(spec.WorkDir, dir)
		}
		return filepath.Join(dir, "serve.log"), nil
	}
	return filepath.Join(spec.RuntimeDir, "serve.log"), nil
}

// SystemdJournal reads the native journal without routing through a shell.
func SystemdJournal(ctx context.Context, m *Manager, lines int) ([]byte, error) {
	spec, err := m.LoadSpec()
	if err != nil {
		return nil, err
	}
	if _, _, err := nativeSystemd().readRegistered(ctx, spec); err != nil {
		return nil, err
	}
	if lines <= 0 || lines > 1000 {
		return nil, errors.New("journal lines must be 1..1000")
	}
	args := append(systemdScope(spec), "-u", spec.Name+".service", "-n", strconv.Itoa(lines), "--no-pager")
	return runSystemdCommand(ctx, "journalctl", args...)
}

// SystemdUninstall only removes an owned unit. Config, logs and binary remain.
func SystemdUninstall(ctx context.Context, m *Manager) error {
	return nativeSystemd().uninstall(ctx, m)
}

func (b systemdBackend) uninstall(ctx context.Context, m *Manager) error {
	spec, err := m.LoadSpec()
	if err != nil {
		return err
	}
	lock, err := m.Acquire()
	if err != nil {
		return err
	}
	defer lock.Release()
	path, unit, err := b.readRegistered(ctx, spec)
	if err != nil {
		return err
	}
	if _, err := b.status(ctx, m, spec); err != nil {
		return err
	}
	if _, err := b.ctl(ctx, spec, "stop", spec.Name+".service"); err != nil {
		return err
	}
	status, err := b.status(ctx, m, spec)
	if err != nil {
		return err
	}
	if status.Active {
		return errors.New("systemd unit remains active after stop")
	}
	if _, err := b.ctl(ctx, spec, "disable", spec.Name+".service"); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	if _, err := b.ctl(ctx, spec, "daemon-reload"); err != nil {
		return errors.Join(err, b.restoreUnit(ctx, spec, path, unit, status.Enabled))
	}
	if err := os.Remove(m.SpecPath()); err != nil {
		return errors.Join(err, b.restoreUnit(ctx, spec, path, unit, status.Enabled))
	}
	return nil
}

func (b systemdBackend) restoreUnit(ctx context.Context, spec Spec, path string, unit []byte, enabled bool) error {
	if err := writeUnit(path, unit); err != nil {
		return err
	}
	_, reloadErr := b.ctl(ctx, spec, "daemon-reload")
	var enableErr error
	if enabled {
		_, enableErr = b.ctl(ctx, spec, "enable", spec.Name+".service")
	}
	return errors.Join(reloadErr, enableErr)
}
