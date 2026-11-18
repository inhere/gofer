package wshub

import (
	"context"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/inhere/gofer/internal/wsproto"
)

// TestFinishedJobResumeAckCarriesLogOffsets: a job the worker reports as already finished
// (its Result is still owed) is listed in a v33 worker's resume ack with the hub's durable
// stdout/stderr offsets, so the worker re-sends the log tail written while disconnected
// before the Result. A pre-v33 worker's ack lists nothing for it, as before.
//
// Falsification: skip plan.terminal in resumeEntries -> the v33 ack below is empty.
func TestFinishedJobResumeAckCarriesLogOffsets(t *testing.T) {
	for _, tc := range []struct {
		name  string
		proto int
		want  bool
	}{
		{"v33 lists the finished job", wsproto.TerminalLogResumeMinProtocolVersion, true},
		{"v32 does not", wsproto.TerminalLogResumeMinProtocolVersion - 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hub := recoverHub(5 * time.Second)
			_, wsURL := hubServer(t, hub, "w1")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			dispatchAndDrop(t, ctx, hub, wsURL, "inst-1", "j1", 11, 4)

			conn, ack := dialRegisterRaw(t, ctx, wsURL, wsproto.Register{
				WorkerID: "w1", InstanceID: "inst-1", ProtocolVersion: tc.proto,
				Inflight: []wsproto.InflightJob{{JobID: "j1", Status: "done", StdoutOff: 7, StderrOff: 4, Seq: 3}},
			})
			defer conn.Close(websocket.StatusNormalClosure, "done")

			if !tc.want {
				if len(ack.Resume) != 0 {
					t.Fatalf("ack.Resume = %+v, want none for a pre-v33 worker", ack.Resume)
				}
				return
			}
			if len(ack.Resume) != 1 || ack.Resume[0].JobID != "j1" {
				t.Fatalf("ack.Resume = %+v, want exactly [j1]", ack.Resume)
			}
			if r := ack.Resume[0]; r.StdoutOff != 11 || r.StderrOff != 4 {
				t.Fatalf("resume offsets = (%d,%d), want the SERVER-side (11,4)", r.StdoutOff, r.StderrOff)
			}
		})
	}
}
