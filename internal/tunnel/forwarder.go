package tunnel

import (
	"context"
	"github.com/coder/websocket"
	"log/slog"
	"net"
	"sync"
	"time"
)

// Forwarder accepts local TCP connections and bridges them to a tunnel.
type Forwarder struct {
	Spec       ForwardSpec
	Dial       func(context.Context) (*websocket.Conn, error)
	Log        func(string, ...any)
	active     sync.WaitGroup
	ActualAddr string
	Ready      chan error
}

// Run starts listening and forwards connections until ctx is cancelled.
func (f *Forwarder) Run(ctx context.Context) error {
	l, err := net.Listen("tcp", f.Spec.ListenAddr())
	if f.Ready != nil {
		defer close(f.Ready)
	}
	if err != nil {
		if f.Ready != nil {
			f.Ready <- err
		}
		return err
	}
	f.ActualAddr = l.Addr().String()
	if f.Ready != nil {
		f.Ready <- nil
	}
	defer l.Close()
	go func() { <-ctx.Done(); l.Close() }()
	for {
		c, e := l.Accept()
		if e != nil {
			if ctx.Err() != nil {
				return nil
			}
			continue
		}
		f.active.Add(1)
		go f.handle(ctx, c)
	}
}

// handle bridges one accepted local connection to its own tunnel. Log (when set)
// gets one line when the tunnel is up and one when it ends, both tagged with the
// local peer so concurrent connections stay distinguishable; a dial failure always
// surfaces (Log, or slog when the caller wants no per-connection output).
func (f *Forwarder) handle(ctx context.Context, c net.Conn) {
	defer f.active.Done()
	defer c.Close()
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	peer := c.RemoteAddr().String()
	started := time.Now()
	ws, err := f.Dial(dctx)
	if err != nil {
		if f.Log != nil {
			f.Log("%s dial failed: %v", peer, err)
		} else {
			slog.Error("tunnel dial failed", "peer", peer, "target", f.Spec.Target, "error", err)
		}
		return
	}
	if f.Log != nil {
		f.Log("%s -> %s connected (%d ms)", peer, f.Spec.Target, time.Since(started).Milliseconds())
	}
	defer ws.Close(websocket.StatusNormalClosure, "")
	up, down, err := Bridge(ctx, ws, c)
	if f.Log != nil {
		msg := "%s closed: up=%d down=%d after %s"
		args := []any{peer, up, down, time.Since(started).Round(time.Millisecond)}
		if err != nil {
			msg += " (%v)"
			args = append(args, err)
		}
		f.Log(msg, args...)
	}
}
