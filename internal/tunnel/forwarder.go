package tunnel

import (
	"context"
	"github.com/coder/websocket"
	"io"
	"log/slog"
	"net"
	"os"
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

// DialResult is what Dial hands back: the tunnel websocket plus the id the server
// assigned to it (the X-Gofer-Tunnel-Id header; empty against a server that
// predates it). The id is what lets a forwarder log line be matched with the
// server and worker lines for the same tunnel.
type DialResult struct {
	Conn     *websocket.Conn
	TunnelID string
}

// Forwarder accepts local connections and bridges them to a tunnel.
//
// Two observation surfaces coexist: Log (human one-liners, legacy) and OnEvent
// (structured events, see the event list below). Either may be nil. A dial
// failure always surfaces through slog when neither is set.
//
// Events, with their attrs (all events also carry session_id; tunnel_id is set
// once the dial succeeded):
//
//	forward.started   local, target, network
//	session.opened    local, target, network
//	tunnel.opened     tunnel_id, dial_ms
//	tunnel.first_up   tunnel_id, first_byte_ms   (first local->tunnel bytes)
//	tunnel.first_down tunnel_id, first_byte_ms   (first tunnel->local bytes)
//	session.closed    tunnel_id, close_reason, bytes_up, bytes_down, duration_ms[, error]
//	                  (UDP adds packets_up, packets_down)
//	session.rejected  close_reason (dropped_max_sessions), max_sessions
//	tunnel.datagram   tunnel_id, dir (up|down), len, gap_ms — only with GOFER_TUNNEL_TRACE=1
//
// first_byte_ms and duration_ms count from the moment the dial started, so a
// slow rendezvous and a slow device answer are told apart via dial_ms.
type Forwarder struct {
	Spec           ForwardSpec
	Dial           func(context.Context) (DialResult, error)
	Log            func(string, ...any)
	OnEvent        func(string, ...any)
	active         sync.WaitGroup
	ActualAddr     string
	Ready          chan error
	UDPIdleTimeout time.Duration
	UDPMaxSessions int
}

// Forwarder-side close reasons that have no Bridge counterpart. The Bridge
// reasons (ReasonClientClosed, ReasonWorkerClosed, ...) are reused verbatim for
// TCP so the three log ends speak the same vocabulary.
const (
	// ReasonDialFailed: the tunnel could never be opened (no session existed).
	ReasonDialFailed = "dial_failed"
	// ReasonDroppedMaxSessions: a new UDP source was refused because UDPMaxSessions
	// sessions were already open.
	ReasonDroppedMaxSessions = "dropped_max_sessions"
	// ReasonIdle: a UDP session was reaped after UDPIdleTimeout of silence.
	ReasonIdle = "idle"
	// ReasonWriteFailed: a datagram could not be written up the tunnel.
	ReasonWriteFailed = "write_failed"
)

// localSeatReason flips Bridge's worker-seat close reasons for the forwarder,
// where the plain conn is the local client rather than the device: the local
// client hanging up is client_closed, the far end of the tunnel is worker_closed.
func localSeatReason(r string) string {
	switch r {
	case ReasonWorkerClosed:
		return ReasonClientClosed
	case ReasonClientClosed:
		return ReasonWorkerClosed
	}
	return r
}

func (f *Forwarder) emit(event string, attrs ...any) {
	if f.OnEvent != nil {
		f.OnEvent(event, attrs...)
	}
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
	f.emit("forward.started", "local", f.ActualAddr, "target", f.Spec.Target, "network", "tcp")
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
	ws       *websocket.Conn
	cancel   context.CancelFunc
	tunnelID string
	started  time.Time // when the dial began; first_byte_ms / duration_ms origin

	firstUp, firstDown sync.Once

	mu       sync.Mutex
	last     time.Time
	up       int64
	down     int64
	pktUp    int64 // datagrams sent up the tunnel
	pktDown  int64 // datagrams delivered back to the local source
	lastUp   time.Time
	lastDown time.Time
	reason   string // close reason recorded by whoever decided to drop the session
}

// traceDatagrams enables the per-datagram tunnel.datagram event (direction,
// length, gap since the previous datagram of that session). It is read once
// so the off path costs one branch per datagram and no formatting.
var traceDatagrams = os.Getenv("GOFER_TUNNEL_TRACE") == "1"

// traceDatagram emits one tunnel.datagram record for a session when tracing is
// on. gap_ms is the time since the previous datagram in the same direction,
// which is the per-packet turnaround a stop-and-wait protocol pays; prev is
// the zero time for the first datagram (gap_ms then reads 0).
func (f *Forwarder) traceDatagram(key, tunnelID, dir string, n int, prev, now time.Time) {
	gap := int64(0)
	if !prev.IsZero() {
		gap = now.Sub(prev).Milliseconds()
	}
	f.emit("tunnel.datagram", "session_id", key, "tunnel_id", tunnelID, "dir", dir, "len", n, "gap_ms", gap)
}

// fail records why the session is being dropped; the first caller wins so a
// reap racing a write failure keeps the cause that actually happened first.
func (s *udpSession) fail(reason string) {
	s.mu.Lock()
	if s.reason == "" {
		s.reason = reason
	}
	s.mu.Unlock()
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
	f.emit("forward.started", "local", f.ActualAddr, "target", f.Spec.Target, "network", "udp")
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
	// drop removes and closes one session, emitting session.closed with the
	// reason recorded on it (ctx_done when nobody recorded one, i.e. shutdown).
	drop := func(key string, reason string) {
		mu.Lock()
		s := sessions[key]
		delete(sessions, key)
		mu.Unlock()
		if s == nil {
			return
		}
		s.fail(reason)
		s.cancel()
		s.ws.Close(websocket.StatusNormalClosure, "")
		s.mu.Lock()
		up, down, pktUp, pktDown, why := s.up, s.down, s.pktUp, s.pktDown, s.reason
		s.mu.Unlock()
		f.emit("session.closed", "session_id", key, "tunnel_id", s.tunnelID, "close_reason", why,
			"bytes_up", up, "bytes_down", down, "packets_up", pktUp, "packets_down", pktDown,
			"duration_ms", time.Since(s.started).Milliseconds())
		if f.Log != nil {
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
			drop(k, ReasonContextDone)
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
			f.emit("session.rejected", "session_id", key, "close_reason", ReasonDroppedMaxSessions, "max_sessions", maxSessions)
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
		// Record the up direction as soon as the client's datagram is in hand, i.e.
		// before it is forwarded: nothing can come back down before something went
		// up, so the documented first_up -> first_down order stays deterministic
		// instead of racing the reply goroutine for the log (the same rule as
		// Splice and the datagram bridge).
		s.firstUp.Do(func() {
			f.emit("tunnel.first_up", "session_id", key, "tunnel_id", s.tunnelID, "first_byte_ms", time.Since(s.started).Milliseconds())
		})
		if err := s.ws.Write(ctx, websocket.MessageBinary, buf[:n]); err != nil {
			drop(key, ReasonWriteFailed)
			continue
		}
		now := time.Now()
		s.mu.Lock()
		s.up += int64(n)
		s.pktUp++
		prev := s.lastUp
		s.lastUp, s.last = now, now
		s.mu.Unlock()
		if traceDatagrams {
			f.traceDatagram(key, s.tunnelID, "up", n, prev, now)
		}
	}
}

// openUDPSession dials one tunnel for src and starts pumping replies back to it.
// It returns nil when the tunnel could not be opened, which is logged like a failed
// TCP dial rather than retried: the next datagram tries again.
func (f *Forwarder) openUDPSession(ctx context.Context, pc net.PacketConn, src net.Addr, key string, drop func(string, string)) *udpSession {
	dctx, dcancel := context.WithTimeout(ctx, 20*time.Second)
	started := time.Now()
	f.emit("session.opened", "session_id", key, "local", f.ActualAddr, "target", f.Spec.Target, "network", "udp")
	d, err := f.Dial(dctx)
	dcancel()
	if err != nil {
		f.emit("session.closed", "session_id", key, "close_reason", ReasonDialFailed, "duration_ms", time.Since(started).Milliseconds(), "error", err)
		if f.Log != nil {
			f.Log("%s dial failed: %v", key, err)
		} else {
			slog.Error("tunnel dial failed", "peer", key, "target", f.Spec.Target, "network", "udp", "error", err)
		}
		return nil
	}
	sctx, scancel := context.WithCancel(ctx)
	s := &udpSession{ws: d.Conn, cancel: scancel, tunnelID: d.TunnelID, started: started, last: time.Now()}
	f.emit("tunnel.opened", "session_id", key, "tunnel_id", s.tunnelID, "dial_ms", time.Since(started).Milliseconds())
	if f.Log != nil {
		f.Log("%s -> %s connected (%d ms)", key, f.Spec.Target, time.Since(started).Milliseconds())
	}
	go func() {
		// The worker (or server) hanging up is the only way this loop ends on
		// its own; a reap or write failure cancels sctx after recording its
		// reason, and drop keeps whichever reason was recorded first.
		defer drop(key, ReasonWorkerClosed)
		for {
			typ, r, e := s.ws.Reader(sctx)
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
			s.firstDown.Do(func() {
				f.emit("tunnel.first_down", "session_id", key, "tunnel_id", s.tunnelID, "first_byte_ms", time.Since(started).Milliseconds())
			})
			now := time.Now()
			s.mu.Lock()
			s.down += int64(len(b))
			s.pktDown++
			prev := s.lastDown
			s.lastDown, s.last = now, now
			s.mu.Unlock()
			if traceDatagrams {
				f.traceDatagram(key, s.tunnelID, "down", len(b), prev, now)
			}
		}
	}()
	return s
}

// reapUDP closes sessions that have been silent for idle. It ticks at half the idle
// window so a session lives at most ~1.5x idle, which is close enough for freeing a
// device-side socket and cheap to reason about.
func (f *Forwarder) reapUDP(ctx context.Context, idle time.Duration, mu *sync.Mutex, sessions map[string]*udpSession, drop func(string, string)) {
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
				drop(k, ReasonIdle)
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
	f.emit("session.opened", "session_id", peer, "local", f.ActualAddr, "target", f.Spec.Target, "network", "tcp")
	d, err := f.Dial(dctx)
	if err != nil {
		f.emit("session.closed", "session_id", peer, "close_reason", ReasonDialFailed, "duration_ms", time.Since(started).Milliseconds(), "error", err)
		if f.Log != nil {
			f.Log("%s dial failed: %v", peer, err)
		} else {
			slog.Error("tunnel dial failed", "peer", peer, "target", f.Spec.Target, "error", err)
		}
		return
	}
	ws, tunnelID := d.Conn, d.TunnelID
	f.emit("tunnel.opened", "session_id", peer, "tunnel_id", tunnelID, "dial_ms", time.Since(started).Milliseconds())
	if f.Log != nil {
		f.Log("%s -> %s connected (%d ms)", peer, f.Spec.Target, time.Since(started).Milliseconds())
	}
	defer ws.Close(websocket.StatusNormalClosure, "")
	// Bridge names its directions from the worker's seat (Up = websocket ->
	// conn). Here conn is the local client, so Bridge's "up" is the reply coming
	// back down the tunnel and its "down" is the client's data going up.
	res := BridgeWithOptions(ctx, ws, c, BridgeOptions{OnFirstUp: func() {
		f.emit("tunnel.first_down", "session_id", peer, "tunnel_id", tunnelID, "first_byte_ms", time.Since(started).Milliseconds())
	}, OnFirstDown: func() {
		f.emit("tunnel.first_up", "session_id", peer, "tunnel_id", tunnelID, "first_byte_ms", time.Since(started).Milliseconds())
	}})
	up, down, err := res.ToWS, res.FromWS, res.Err
	closed := []any{"session_id", peer, "tunnel_id", tunnelID, "close_reason", localSeatReason(res.Reason), "bytes_up", up, "bytes_down", down, "duration_ms", time.Since(started).Milliseconds()}
	if err != nil {
		closed = append(closed, "error", err)
	}
	f.emit("session.closed", closed...)
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
