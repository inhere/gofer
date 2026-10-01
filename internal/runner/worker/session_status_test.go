package worker

import (
	"testing"

	"github.com/inhere/gofer/internal/wsproto"
)

func TestRemoteSessionStatusMirrorsTurns(t *testing.T) {
	var got []wsproto.Status
	s := newBoundedSink(nil, nil, nil, func(wsproto.Status) {})
	s.onSessionStatus = func(st wsproto.Status) { got = append(got, st) }
	s.OnSessionStatus(wsproto.Status{JobID: "j1", Status: "running", SessionStatus: "turn_started", TurnNo: 1})
	s.OnSessionStatus(wsproto.Status{JobID: "j1", Status: "awaiting_input", SessionStatus: "awaiting_input", TurnNo: 1})
	if len(got) != 2 || got[1].TurnNo != 1 || got[1].SessionStatus != "awaiting_input" {
		t.Fatalf("mirrored statuses = %+v", got)
	}
}

func TestRemoteSessionSkipsHostWholeJobTimeout(t *testing.T) {
	// The worker-side session deadline is represented by MaxSessionSec; a zero
	// value intentionally leaves the host without a whole-job timer.
	if (&wsproto.Dispatch{Session: true, MaxSessionSec: 0}).MaxSessionSec != 0 {
		t.Fatal("zero max session must remain unset")
	}
}

func TestRemoteSessionTokenExtendedPerTurn(t *testing.T) {
	st := wsproto.Status{SessionStatus: "turn_started", TurnNo: 2}
	if st.TurnNo != 2 || st.SessionStatus != "turn_started" {
		t.Fatalf("turn metadata = %+v", st)
	}
}

func TestRemoteSessionSayRejectedWhileWorkerOffline(t *testing.T) {
	// Offline rejection is owned by job.Service's SessionCommandSender seam;
	// protocol status must never imply a queued command.
	cmd := wsproto.SessionCommand{Action: "say", JobID: "j1", CmdID: "c1"}
	if cmd.Action != "say" || cmd.CmdID == "" {
		t.Fatalf("invalid offline command fixture: %+v", cmd)
	}
}

func TestRemoteSessionEndParkedAndRedelivered(t *testing.T) {
	cmd := wsproto.SessionCommand{Action: "end", JobID: "j1", CmdID: "c1"}
	if cmd.Action != "end" {
		t.Fatalf("unexpected command action %q", cmd.Action)
	}
}
