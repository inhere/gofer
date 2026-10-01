package wshub

import (
	"testing"

	"github.com/inhere/gofer/internal/wsproto"
)

func TestRemoteSessionStatusMirrorsTurns(t *testing.T) {
	wc := &workerConn{}
	first := wsproto.Status{JobID: "j1", Status: "awaiting_input", TurnNo: 2}
	if wc.sessionStatusSeen(first) {
		t.Fatal("first session status was deduplicated")
	}
	if !wc.sessionStatusSeen(first) {
		t.Fatal("repeated turn status was not deduplicated")
	}
	if wc.sessionStatusSeen(wsproto.Status{JobID: "j1", Status: "awaiting_input", TurnNo: 3}) {
		t.Fatal("a new turn status was deduplicated")
	}
}

func TestRemoteSessionEndParkedAndRedelivered(t *testing.T) {
	h := New(map[string]string{"w1": "caller"})
	h.parkSessionCommand("w1", wsproto.SessionCommand{JobID: "j1", CmdID: "end-1", Action: "end"})
	h.recMu.Lock()
	queued := append([]wsproto.SessionCommand(nil), h.parkedSessionCommands["w1"]...)
	h.recMu.Unlock()
	if len(queued) != 1 || queued[0].JobID != "j1" || queued[0].Action != "end" {
		t.Fatalf("parked session commands = %+v", queued)
	}
}
