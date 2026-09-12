package tunnel

import (
	"context"
	"github.com/coder/websocket"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"
)

const (
	// defaultUDPIdleTimeout reaps a UDP session after this much silence. UDP has no
	// close, so without it every source address that ever sent a datagram would hold
	// a tunnel on the worker forever.
	defaultUDPIdleTimeout = 60 * time.Second
	// defaultUDPMaxSessions caps concurrent UDP sessions so one client spraying from
	// random source ports cannot exhaust the worker's tunnel budget.
	defaultUDPMaxSessions = 32
)

// Forwarder accepts local connections and bridges them to a tunnel.
type Forwarder struct {
	Spec           ForwardSpec
	Dial           func(context.Context) (*websocket.Conn, error)
	Log            func(string, ...any)
	active         sync.WaitGroup
	ActualAddr     string
	Ready          chan error
	UDPIdleTimeout time.Duration
	UDPMaxSessions int
}

// Run starts listening and forwards connections until ctx is cancelled.
func (f *Forwarder) Run(ctx context.Context) error {
	if f.Spec.Network == "udp" {
		return f.runUDP(ctx)
	}
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

// udpSession is one local source address talking through its own tunnel. Datagrams
// from that source go up its websocket and replies come back to that source only,
// which is what keeps two clients on the same forwarded port from seeing each
// other's traffic.
type udpSession struct {
	ws     *websocket.Conn
	cancel context.CancelFunc

	mu   sync.Mutex
	last time.Time
	up   int64
	down int64
}

func (s *udpSession) touch() {
	s.mu.Lock()
	s.last = time.Now()
	s.mu.Unlock()
}

func (s *udpSession) idleSince(now time.Time) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return now.Sub(s.last)
}

func (s *udpSession) counts() (int64, int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.up, s.down
}

// runUDP forwards datagrams through per-source-address tunnels.
//
// The whole point of the tunnel is that this machine cannot reach the target, so a
// datagram is never sent to Spec.Target from here: it goes up the session's
// websocket, the worker dials the device on its own network, and the reply comes
// back down the same websocket to the source that sent it.
func (f *Forwarder) runUDP(ctx context.Context) error {
	pc, err := net.ListenPacket("udp", f.Spec.ListenAddr())
	if f.Ready != nil {
		defer close(f.Ready)
	}
	if err != nil {
		if f.Ready != nil {
			f.Ready <- err
		}
		return err
	}
	f.ActualAddr = pc.LocalAddr().String()
	if f.Ready != nil {
		f.Ready <- nil
	}
	defer pc.Close()

	idle := f.UDPIdleTimeout
	if idle <= 0 {
		idle = defaultUDPIdleTimeout
	}
	maxSessions := f.UDPMaxSessions
	if maxSessions <= 0 {
		maxSessions = defaultUDPMaxSessions
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { <-ctx.Done(); pc.Close() }()

	var mu sync.Mutex
	sessions := map[string]*udpSession{}
	drop := func(key string) {
		mu.Lock()
		s := sessions[key]
		delete(sessions, key)
		mu.Unlock()
		if s == nil {
			return
		}
		s.cancel()
		s.ws.Close(websocket.StatusNormalClosure, "")
		if f.Log != nil {
			up, down := s.counts()
			f.Log("%s closed: up=%d down=%d", key, up, down)
		}
	}
	defer func() {
		mu.Lock()
		keys := make([]string, 0, len(sessions))
		for k := range sessions {
			keys = append(keys, k)
		}
		mu.Unlock()
		for _, k := range keys {
			drop(k)
		}
	}()
	go f.reapUDP(ctx, idle, &mu, sessions, drop)

	buf := make([]byte, ReadLimit)
	for {
		n, src, e := pc.ReadFrom(buf)
		if e != nil {
			if ctx.Err() != nil {
				return nil
			}
			continue
		}
		key := src.String()
		mu.Lock()
		s := sessions[key]
		full := s == nil && len(sessions) >= maxSessions
		mu.Unlock()
		if full {
			if f.Log != nil {
				f.Log("%s dropped: %d udp sessions already open", key, maxSessions)
			}
			continue
		}
		if s == nil {
			s = f.openUDPSession(ctx, pc, src, key, drop)
			if s == nil {
				continue
			}
			mu.Lock()
			// A datagram from the same source may have raced us here; keep the
			// session that got registered first so both sides agree on one tunnel.
			if existing := sessions[key]; existing != nil {
				mu.Unlock()
				s.cancel()
				s.ws.Close(websocket.StatusNormalClosure, "")
				s = existing
			} else {
				sessions[key] = s
				mu.Unlock()
			}
		}
		if err := s.ws.Write(ctx, websocket.MessageBinary, buf[:n]); err != nil {
			drop(key)
			continue
		}
		s.mu.Lock()
		s.up += int64(n)
		s.last = time.Now()
		s.mu.Unlock()
	}
}

// openUDPSession dials one tunnel for src and starts pumping replies back to it.
// It returns nil when the tunnel could not be opened, which is logged like a failed
// TCP dial rather than retried: the next datagram tries again.
func (f *Forwarder) openUDPSession(ctx context.Context, pc net.PacketConn, src net.Addr, key string, drop func(string)) *udpSession {
	dctx, dcancel := context.WithTimeout(ctx, 20*time.Second)
	started := time.Now()
	ws, err := f.Dial(dctx)
	dcancel()
	if err != nil {
		if f.Log != nil {
			f.Log("%s dial failed: %v", key, err)
		} else {
			slog.Error("tunnel dial failed", "peer", key, "target", f.Spec.Target, "network", "udp", "error", err)
		}
		return nil
	}
	sctx, scancel := context.WithCancel(ctx)
	s := &udpSession{ws: ws, cancel: scancel, last: time.Now()}
	if f.Log != nil {
		f.Log("%s -> %s connected (%d ms)", key, f.Spec.Target, time.Since(started).Milliseconds())
	}
	go func() {
		defer drop(key)
		for {
			typ, r, e := ws.Reader(sctx)
			if e != nil {
				return
			}
			if typ != websocket.MessageBinary {
				io.Copy(io.Discard, r)
				continue
			}
			b, e := io.ReadAll(io.LimitReader(r, ReadLimit+1))
			if e != nil || len(b) > ReadLimit {
				return
			}
			if _, e := pc.WriteTo(b, src); e != nil {
				return
			}
			s.mu.Lock()
			s.down += int64(len(b))
			s.last = time.Now()
			s.mu.Unlock()
		}
	}()
	return s
}

// reapUDP closes sessions that have been silent for idle. It ticks at half the idle
// window so a session lives at most ~1.5x idle, which is close enough for freeing a
// device-side socket and cheap to reason about.
func (f *Forwarder) reapUDP(ctx context.Context, idle time.Duration, mu *sync.Mutex, sessions map[string]*udpSession, drop func(string)) {
	tick := idle / 2
	if tick < 10*time.Millisecond {
		tick = 10 * time.Millisecond
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			var stale []string
			mu.Lock()
			for k, s := range sessions {
				if s.idleSince(now) >= idle {
					stale = append(stale, k)
				}
			}
			mu.Unlock()
			for _, k := range stale {
				if f.Log != nil {
					f.Log("%s idle for %s, closing udp session", k, idle)
				}
				drop(k)
			}
		}
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
