package wshub

import (
	"context"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/inhere/gofer/internal/wsproto"
)

// A worker heartbeat that carries messenger/dirs state updates the snapshot; a
// bare ping (an older worker) leaves it untouched and never clears it.
func TestHeartbeatStateReachesSnapshot(t *testing.T) {
	hub := New(map[string]string{"w1": "w1"})
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, reg := dialAndRegister(t, ctx, wsURL, "w1")
	defer conn.Close(websocket.StatusNormalClosure, "")
	if !reg.Accepted {
		t.Fatal("register rejected")
	}
	waitWorkerOnline(t, hub, "w1")
	if snap, _ := hub.WorkerSnapshot("w1"); snap.Messenger != nil || snap.Dirs != nil {
		t.Fatalf("fresh snapshot already has state: %+v", snap)
	}
	ping := wsproto.Ping{TS: 1,
		Messenger: &wsproto.MessengerSnapshot{Status: "busy", StderrTail: "boom"},
		Dirs:      &wsproto.WorkDirs{Workspace: &wsproto.WorkDir{Path: "/ws", Exists: true}, Roots: []wsproto.WorkRoot{{From: "/a", To: "/b", Exists: false}}}}
	if err := wsjson.Write(ctx, conn, wsproto.Envelope{Type: wsproto.TypePing, Payload: mustRaw(ping)}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { s, _ := hub.WorkerSnapshot("w1"); return s.Messenger != nil })
	snap, _ := hub.WorkerSnapshot("w1")
	if snap.Messenger.Status != "busy" || snap.Dirs.Roots[0].To != "/b" || snap.Dirs.Workspace.Path != "/ws" {
		t.Fatalf("snapshot state = %+v / %+v", snap.Messenger, snap.Dirs)
	}
	// A bare ping must not wipe what was reported.
	if err := wsjson.Write(ctx, conn, wsproto.Envelope{Type: wsproto.TypePing, Payload: mustRaw(wsproto.Ping{TS: 2})}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if s, _ := hub.WorkerSnapshot("w1"); s.Messenger == nil || s.Dirs == nil {
		t.Fatal("a bare ping cleared the reported state")
	}
}
