package servicemgr

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/inhere/gofer/internal/daemon"
)

const StateSchema = 1

// State is local control state, not an OS manager's status source. A backend
// still has to verify its task/unit and the process before reporting success.
type State struct {
	SchemaVersion      int                    `json:"schema_version"`
	Name               string                 `json:"name"`
	StopRequested      bool                   `json:"stop_requested"`
	NativeInvocationID string                 `json:"native_invocation_id,omitempty"`
	Supervisor         daemon.ProcessIdentity `json:"supervisor,omitempty"`
	Server             daemon.ProcessIdentity `json:"server,omitempty"`
}

func (s State) Validate() error {
	if s.SchemaVersion != StateSchema {
		return fmt.Errorf("unsupported service state schema %d", s.SchemaVersion)
	}
	if !validName(s.Name) {
		return fmt.Errorf("invalid service state name %q", s.Name)
	}
	for _, process := range []struct {
		name     string
		identity daemon.ProcessIdentity
	}{{"supervisor", s.Supervisor}, {"server", s.Server}} {
		p := process.identity
		if p.PID < 0 || (p.PID == 0 && (p.Exe != "" || p.Owner != "" || p.StartID != "")) ||
			(p.PID > 0 && (p.Exe == "" || p.Owner == "" || p.StartID == "")) {
			return fmt.Errorf("invalid %s process identity", process.name)
		}
	}
	return nil
}

func LoadState(path string) (State, error) {
	var state State
	data, err := os.ReadFile(path)
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, fmt.Errorf("decode service state: %w", err)
	}
	if err := state.Validate(); err != nil {
		return state, err
	}
	return state, nil
}

func saveState(path string, state State) error {
	if err := state.Validate(); err != nil {
		return err
	}
	return writeJSONAtomic(path, state)
}

func writeJSONAtomic(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".service-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
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
