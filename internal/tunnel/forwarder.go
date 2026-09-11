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
func (f *Forwarder) handle(ctx context.Context, c net.Conn) {
	defer f.active.Done()
	defer c.Close()
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	ws, err := f.Dial(dctx)
	if err != nil {
		if f.Log != nil {
			f.Log("dial failed: %v", err)
		} else {
			slog.Error("tunnel dial failed", "error", err)
		}
		return
	}
	defer ws.Close(websocket.StatusNormalClosure, "")
	up, down, err := Bridge(ctx, ws, c)
	if f.Log != nil {
		f.Log("closed up=%d down=%d err=%v", up, down, err)
	}
}
