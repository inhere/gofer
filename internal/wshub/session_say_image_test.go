package wshub

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/inhere/gofer/internal/wsproto"
)

func TestSendSessionSayImagesNeedsV25(t *testing.T) {
	images := []wsproto.SessionImage{{Mime: "image/png", Data: []byte{1, 2, 3}}}

	t.Run("older worker is refused and gets no frame", func(t *testing.T) {
		cancel, conn, hub := registerTunnelWorker(t, wsproto.ImageSayMinProtocolVersion-1, 0)
		defer cancel()
		defer conn.Close(websocket.StatusNormalClosure, "")
		err := hub.SendSessionSay("w1", "j1", "c1", "see", images)
		if !errors.Is(err, ErrWorkerTooOld) {
			t.Fatalf("SendSessionSay on v24 = %v, want ErrWorkerTooOld", err)
		}
		rctx, cancelRead := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancelRead()
		if _, err := readEnvelope(rctx, conn); err == nil {
			t.Fatal("old worker received a session_cmd frame")
		}
	})

	t.Run("v25 worker receives the blocks", func(t *testing.T) {
		cancel, conn, hub := registerTunnelWorker(t, wsproto.ImageSayMinProtocolVersion, 0)
		defer cancel()
		defer conn.Close(websocket.StatusNormalClosure, "")
		if err := hub.SendSessionSay("w1", "j1", "c1", "see", images); err != nil {
			t.Fatal(err)
		}
		rctx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancelRead()
		env, err := readEnvelope(rctx, conn)
		if err != nil {
			t.Fatal(err)
		}
		cmd, err := wsproto.As[wsproto.SessionCommand](env)
		if err != nil {
			t.Fatal(err)
		}
		if cmd.Action != "say" || cmd.Prompt != "see" || len(cmd.Images) != 1 || cmd.Images[0].Mime != "image/png" || string(cmd.Images[0].Data) != "\x01\x02\x03" {
			t.Fatalf("frame = %+v", cmd)
		}
	})

	t.Run("offline worker", func(t *testing.T) {
		if err := New(nil).SendSessionSay("nope", "j1", "c1", "see", images); !errors.Is(err, ErrWorkerOffline) {
			t.Fatalf("got %v, want ErrWorkerOffline", err)
		}
	})
}
