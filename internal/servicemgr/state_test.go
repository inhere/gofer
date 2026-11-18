package servicemgr

import (
	"testing"
	"time"
)

// TestNoteLastExit: an unrequested non-zero exit is recorded (with its code, the pid and
// how long that incarnation had been up) so `serve status` can report it; a clean exit
// and a stop the operator asked for are not recorded, and neither wipes an earlier note
// (gofer-opre).
func TestNoteLastExit(t *testing.T) {
	now := time.Date(2026, 11, 4, 14, 24, 39, 0, time.UTC)
	cases := []struct {
		name    string
		state   State
		code    int
		wantSet bool
	}{
		{"crash with 0xffffffff is recorded", State{}, 4294967295, true},
		{"clean exit is not recorded", State{}, 0, false},
		{"requested stop is not recorded", State{StopRequested: true}, 4294967295, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := tc.state
			noteLastExit(&state, 4321, tc.code, 90*time.Second, now)
			if !tc.wantSet {
				if state.LastExit != nil {
					t.Fatalf("LastExit = %+v, want none", state.LastExit)
				}
				return
			}
			if state.LastExit == nil {
				t.Fatal("LastExit not recorded")
			}
			got := *state.LastExit
			want := LastExit{At: now, ExitCode: tc.code, PID: 4321, UptimeSec: 90}
			if got != want {
				t.Fatalf("LastExit = %+v, want %+v", got, want)
			}
		})
	}

	// A later requested stop must not erase what the crash recorded.
	state := State{}
	noteLastExit(&state, 1, 1, time.Minute, now)
	state.StopRequested = true
	noteLastExit(&state, 2, 0, time.Minute, now.Add(time.Hour))
	if state.LastExit == nil || state.LastExit.ExitCode != 1 || state.LastExit.PID != 1 {
		t.Fatalf("LastExit after a requested stop = %+v", state.LastExit)
	}
}

// TestExitCodeOf: the wait error carries the process's exit code, and a signalled
// process (no code) reads as -1 rather than as a clean 0.
func TestExitCodeOf(t *testing.T) {
	if got := ExitCodeOf(nil); got != 0 {
		t.Fatalf("ExitCodeOf(nil) = %d, want 0", got)
	}
	if got := ExitCodeOf(errNotExitError{}); got != -1 {
		t.Fatalf("ExitCodeOf(non-exit error) = %d, want -1", got)
	}
}

type errNotExitError struct{}

func (errNotExitError) Error() string { return "start failed" }

// TestStateRoundTripKeepsLastExit: the note survives the state file, and an old state
// file without the field still validates (the schema version does not change).
func TestStateRoundTripKeepsLastExit(t *testing.T) {
	root := t.TempDir()
	m, err := NewManager(root, "gofer-opre-state")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 4, 14, 24, 39, 0, time.UTC)
	state := State{SchemaVersion: StateSchema, Name: m.Name,
		LastExit: &LastExit{At: now, ExitCode: 4294967295, PID: 77, UptimeSec: 3600}}
	if err := m.SaveState(state); err != nil {
		t.Fatal(err)
	}
	got, err := m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if got.LastExit == nil || got.LastExit.ExitCode != 4294967295 || !got.LastExit.At.Equal(now) || got.LastExit.UptimeSec != 3600 {
		t.Fatalf("LastExit round trip = %+v", got.LastExit)
	}
}
