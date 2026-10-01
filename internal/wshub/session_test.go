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
