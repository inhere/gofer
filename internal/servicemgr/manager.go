package servicemgr

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/inhere/gofer/internal/daemon"
)

var ErrIdentityMismatch = errors.New("service process identity mismatch")
var ErrRunningSpecChange = errors.New("stop the running service before changing its registration")

// Manager locates one instance's metadata. Platform backends extend its
// lifecycle; the shared core does not write OS registrations.
type Manager struct {
	ConfigDir string
	Name      string
}

func NewManager(configDir, name string) (*Manager, error) {
	if !filepath.IsAbs(configDir) || filepath.Clean(configDir) != configDir {
		return nil, fmt.Errorf("config_dir must be a clean absolute path: %q", configDir)
	}
	if !validName(name) {
		return nil, fmt.Errorf("invalid service name %q", name)
	}
	return &Manager{ConfigDir: configDir, Name: name}, nil
}

func (m *Manager) serviceDir() string      { return filepath.Join(m.ConfigDir, "run", "service") }
func (m *Manager) SpecPath() string        { return filepath.Join(m.serviceDir(), m.Name+".json") }
func (m *Manager) StatePath() string       { return filepath.Join(m.serviceDir(), m.Name+".state.json") }
func (m *Manager) LockPath() string        { return filepath.Join(m.serviceDir(), m.Name+".lock") }
func (m *Manager) Acquire() (*Lock, error) { return AcquireLock(m.LockPath()) }

// RuntimeDirFor follows the existing serve -c rule: only an explicit -c puts
// serve.pid beside that file. GOFER_CONFIG affects file selection, not this
// runtime path. Callers supply the selected ConfigDir independently.
func RuntimeDirFor(configDir, explicitConfig string) (string, error) {
	if !filepath.IsAbs(configDir) {
		return "", fmt.Errorf("config_dir must be absolute: %q", configDir)
	}
	if strings.TrimSpace(explicitConfig) != "" {
		path, err := filepath.Abs(strings.TrimSpace(explicitConfig))
		if err != nil {
			return "", err
		}
		return filepath.Join(filepath.Dir(path), "run"), nil
	}
	return filepath.Join(configDir, "run"), nil
}

func (m *Manager) LoadSpec() (Spec, error) {
	spec, err := LoadSpec(m.SpecPath())
	if err != nil {
		return spec, err
	}
	if spec.Name != m.Name || spec.ConfigDir != m.ConfigDir {
		return spec, fmt.Errorf("%w: spec name/config_dir differs from requested instance", ErrIdentityMismatch)
	}
	return spec, nil
}

// SaveSpec is a registration update. Its caller holds Acquire across the full
// native task/unit transaction; this method also rejects changes while either
// recorded process remains live.
func (m *Manager) SaveSpec(spec Spec) error {
	if spec.Name != m.Name || spec.ConfigDir != m.ConfigDir {
		return fmt.Errorf("%w: spec name/config_dir differs from requested instance", ErrIdentityMismatch)
	}
	current, err := m.LoadSpec()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil && current != spec {
		pid, readErr := daemon.ReadPIDFile(filepath.Join(current.RuntimeDir, "serve.pid"))
		if readErr == nil && daemon.PIDAlive(pid) {
			return ErrRunningSpecChange
		}
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return fmt.Errorf("check existing service pidfile: %w", readErr)
		}
		state, stateErr := m.LoadState()
		if stateErr != nil && !errors.Is(stateErr, os.ErrNotExist) {
			return stateErr
		}
		if stateErr == nil && state.Supervisor.PID > 0 && daemon.PIDAlive(state.Supervisor.PID) {
			return ErrRunningSpecChange
		}
	}
	return saveSpec(m.SpecPath(), spec)
}

func (m *Manager) LoadState() (State, error) {
	state, err := LoadState(m.StatePath())
	if err != nil {
		return state, err
	}
	if state.Name != m.Name {
		return state, fmt.Errorf("%w: state name differs from requested instance", ErrIdentityMismatch)
	}
	return state, nil
}

func (m *Manager) SaveState(state State) error {
	if state.Name != m.Name {
		return fmt.Errorf("%w: state name differs from requested instance", ErrIdentityMismatch)
	}
	return saveState(m.StatePath(), state)
}

// InspectServer reads the recorded pid and checks the live process against the
// spec and the required recorded creation identity. A native backend
// must separately verify its task/unit command line and config-file ownership.
func (m *Manager) InspectServer() (daemon.ProcessIdentity, error) {
	spec, err := m.LoadSpec()
	if err != nil {
		return daemon.ProcessIdentity{}, err
	}
	pid, err := daemon.ReadPIDFile(filepath.Join(spec.RuntimeDir, "serve.pid"))
	if err != nil {
		return daemon.ProcessIdentity{}, err
	}
	identity, err := daemon.InspectProcess(pid)
	if err != nil {
		return identity, err
	}
	if !daemon.SameExecutable(identity.Exe, spec.Exe) || identity.Owner != spec.Owner {
		return identity, fmt.Errorf("%w: pid=%d exe=%q owner=%q", ErrIdentityMismatch, pid, identity.Exe, identity.Owner)
	}
	state, err := m.LoadState()
	if errors.Is(err, os.ErrNotExist) {
		return identity, fmt.Errorf("%w: no recorded server creation identity", ErrIdentityMismatch)
	}
	if err != nil {
		return identity, err
	}
	if state.Server.PID == 0 || state.Server.PID != pid || state.Server.StartID != identity.StartID ||
		!daemon.SameExecutable(state.Server.Exe, identity.Exe) || state.Server.Owner != identity.Owner {
		return identity, fmt.Errorf("%w: pid=%d creation identity changed", ErrIdentityMismatch, pid)
	}
	return identity, nil
}
