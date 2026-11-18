package servicemgr

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

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
	// LastExit (gofer-opre) is the last exit of the supervised server that NOBODY asked
	// for (a requested stop or a recorded upgrade drain is not one). A server that dies
	// from a `taskkill /IM gofer.exe` used to leave nothing behind but a line in the
	// supervisor log; `serve status` reads this and says when and with which code.
	LastExit *LastExit `json:"last_exit,omitempty"`
}

// LastExit is one abnormal end of the supervised server process.
type LastExit struct {
	// At is when the supervisor saw the process end.
	At time.Time `json:"at"`
	// ExitCode is what the process returned. Windows reports the raw DWORD, so a crash
	// killed mid-teardown shows up as 4294967295 (0xffffffff).
	ExitCode int `json:"exit_code"`
	// PID / UptimeSec describe the incarnation that ended (how long it had been up).
	PID       int   `json:"pid,omitempty"`
	UptimeSec int64 `json:"uptime_sec,omitempty"`
}

// ExitCodeOf turns the error exec.Cmd.Wait returns into the process exit code: 0 for a
// clean exit, -1 when the process was signalled/debug-stopped and had no code.
func ExitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// noteLastExit records an exit the operator did not ask for on the state (gofer-opre):
// a non-zero code ends up as LastExit so `serve status` can say when and how the server
// died. A requested stop proves the end was intended, and 0 is a clean exit, so neither
// is written (and neither clears what an earlier crash recorded — the note is history
// of the last ABNORMAL end, not of the last end).
func noteLastExit(state *State, pid int, exitCode int, uptime time.Duration, now time.Time) {
	if exitCode == 0 || state.StopRequested {
		return
	}
	state.LastExit = &LastExit{At: now, ExitCode: exitCode, PID: pid, UptimeSec: int64(uptime / time.Second)}
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
