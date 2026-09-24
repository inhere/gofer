package wsproto

import (
	"encoding/json"
	"testing"
)

// TestDispatchRoundTripsExclusiveDirAndStallTimeout: the JOB-11 / AUTO-05 execution
// policy the HUB resolved rides Dispatch additively — the worker that actually runs the
// job takes the directory lock (and arms the stall watchdog) with the numbers the hub
// decided, instead of re-deriving them from its own config. dir_wait_max_sec (F12) is
// the third member of that set: the lock's WAIT cap, which only the machine holding the
// lock can enforce.
func TestDispatchRoundTripsExclusiveDirAndStallTimeout(t *testing.T) {
	excl, stall, dirWait := true, 900, 60
	d := Dispatch{
		JobID: "j1", ProjectKey: "p", Agent: "agent", Runner: "local",
		ExclusiveDir: &excl, StallTimeoutSec: &stall, DirWaitMaxSec: &dirWait,
	}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal dispatch: %v", err)
	}
	var back Dispatch
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal dispatch: %v", err)
	}
	if back.ExclusiveDir == nil || !*back.ExclusiveDir {
		t.Fatalf("exclusive_dir round trip = %v, want an explicit true", back.ExclusiveDir)
	}
	if back.StallTimeoutSec == nil || *back.StallTimeoutSec != 900 {
		t.Fatalf("stall_timeout_sec round trip = %v, want 900", back.StallTimeoutSec)
	}
	if back.DirWaitMaxSec == nil || *back.DirWaitMaxSec != 60 {
		t.Fatalf("dir_wait_max_sec round trip = %v, want 60", back.DirWaitMaxSec)
	}

	// "Resolved to OFF" must survive as an explicit zero: a hub that decided this job
	// gets no stall watchdog must not have the worker re-derive its own window.
	off := 0
	no := Dispatch{JobID: "j2", StallTimeoutSec: &off, ExclusiveDir: new(bool)}
	b, err = json.Marshal(no)
	if err != nil {
		t.Fatalf("marshal dispatch: %v", err)
	}
	var backOff Dispatch
	if err := json.Unmarshal(b, &backOff); err != nil {
		t.Fatalf("unmarshal dispatch: %v", err)
	}
	if backOff.StallTimeoutSec == nil || *backOff.StallTimeoutSec != 0 {
		t.Fatalf("an explicit stall_timeout_sec=0 must survive, got %v", backOff.StallTimeoutSec)
	}
	if backOff.ExclusiveDir == nil || *backOff.ExclusiveDir {
		t.Fatalf("an explicit exclusive_dir=false must survive, got %v", backOff.ExclusiveDir)
	}

	// Additive: a dispatch from a hub that predates both keys still decodes, and a plain
	// dispatch carries neither key on the wire (a pre-P1 frame stays byte-identical).
	var old Dispatch
	if err := json.Unmarshal([]byte(`{"job_id":"j3","project_key":"p","agent":"exec","runner":"local"}`), &old); err != nil {
		t.Fatalf("a dispatch without the gate fields must still decode: %v", err)
	}
	if old.ExclusiveDir != nil || old.StallTimeoutSec != nil || old.DirWaitMaxSec != nil {
		t.Fatalf("absent fields decoded to %v/%v/%v, want nil (unresolved)", old.ExclusiveDir, old.StallTimeoutSec, old.DirWaitMaxSec)
	}
	plain, err := json.Marshal(Dispatch{JobID: "j4"})
	if err != nil {
		t.Fatalf("marshal plain dispatch: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(plain, &raw); err != nil {
		t.Fatalf("unmarshal plain dispatch: %v", err)
	}
	for _, k := range []string{"exclusive_dir", "stall_timeout_sec", "dir_wait_max_sec"} {
		if _, ok := raw[k]; ok {
			t.Fatalf("a plain dispatch must not carry %q: %s", k, plain)
		}
	}
}
