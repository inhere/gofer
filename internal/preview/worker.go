package preview

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/inhere/gofer/internal/tunnel"
	"github.com/inhere/gofer/internal/wsproto"
)

// localWorker is the worker id recorded on a lease whose job runs on this server. It is a
// reserved runner name, so no real worker can carry it.
const localWorker = "local"

// Refusals specific to a lease on a worker, as values so the HTTP layer can attach a stable
// error code instead of matching message text.
var (
	// ErrWorkerOffline: the job's worker is not connected.
	ErrWorkerOffline = errors.New("preview worker is offline")
	// ErrWorkerOutdated: the worker's protocol predates wsproto.PreviewMinProtocolVersion.
	ErrWorkerOutdated = errors.New("preview worker needs an upgrade")
	// ErrWorkerLost: the worker process the lease was created against is gone.
	ErrWorkerLost = errors.New("preview worker instance changed")
	// ErrWorkerDial: the worker refused or failed to open the connection.
	ErrWorkerDial = errors.New("preview worker could not open the connection")
)

// WorkerHub is what the manager needs from the worker hub: liveness, the negotiated
// protocol and the one control frame. *wshub.Hub satisfies it.
type WorkerHub interface {
	LiveInstance(workerID string) (string, bool)
	WorkerProtocol(workerID string) (int, bool)
	OpenPreview(workerID string, o wsproto.PreviewOpen) error
}

// relayTimeout bounds how long a data connection waits for the worker to call back.
var relayTimeout = 15 * time.Second

// AttachWorkers wires the worker hub so leases can target jobs on workers. Without it a
// worker job is refused with ErrWorkerOffline.
func (m *Manager) AttachWorkers(h WorkerHub) {
	m.mu.Lock()
	m.hub = h
	m.mu.Unlock()
}

// endpoint is where a lease's job runs.
type endpoint struct {
	worker, instance string
}

func (e endpoint) remote() bool { return e.worker != localWorker }

// WorkerEndpoint resolves workerID to a lease endpoint, refusing a worker that is offline
// or whose protocol cannot serve previews (so no port is allocated for it).
func (m *Manager) workerEndpoint(workerID string) (endpoint, error) {
	m.mu.Lock()
	h := m.hub
	m.mu.Unlock()
	if h == nil {
		return endpoint{}, ErrWorkerOffline
	}
	inst, ok := h.LiveInstance(workerID)
	if !ok {
		return endpoint{}, ErrWorkerOffline
	}
	proto, _ := h.WorkerProtocol(workerID)
	if !wsproto.SupportsPreview(proto) {
		return endpoint{}, fmt.Errorf("%w: worker protocol %d, preview needs %d", ErrWorkerOutdated, proto, wsproto.PreviewMinProtocolVersion)
	}
	return endpoint{worker: workerID, instance: inst}, nil
}

// workerTransport is the reverse proxy's upstream for a lease on a worker: every upstream
// connection is a relay to 127.0.0.1:<port> on that worker, tracked by the lease so closing
// the lease cuts it.
func (m *Manager) workerTransport(lv *live, ep endpoint, jobID, previewID string, port int) *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			c, err := m.dialWorker(ctx, ep, jobID, previewID, port)
			if err != nil {
				return nil, err
			}
			return lv.trackUpstream(c)
		},
		DisableCompression:  true,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     30 * time.Second,
	}
}

// dialWorker asks the worker to open one connection to the job's port and returns the byte
// stream once the worker has called back on the preview-connect endpoint.
func (m *Manager) dialWorker(ctx context.Context, ep endpoint, jobID, previewID string, port int) (net.Conn, error) {
	m.mu.Lock()
	h := m.hub
	m.mu.Unlock()
	if h == nil {
		return nil, ErrWorkerOffline
	}
	if inst, ok := h.LiveInstance(ep.worker); !ok || inst != ep.instance {
		return nil, ErrWorkerLost
	}
	p, nonce := m.relay.Begin(tunnel.Binding{WorkerID: ep.worker, InstanceID: ep.instance, CallerID: previewID, Target: jobID}, relayTimeout)
	open := wsproto.PreviewOpen{PreviewID: previewID, JobID: jobID, Port: port, RelayNonce: nonce, ConnID: p.TunnelID(), InstanceID: ep.instance}
	if err := h.OpenPreview(ep.worker, open); err != nil {
		p.Cancel()
		switch {
		case errors.Is(err, tunnel.ErrUnsupported):
			return nil, fmt.Errorf("%w: %v", ErrWorkerOutdated, err)
		case errors.Is(err, tunnel.ErrWorkerOffline):
			return nil, ErrWorkerOffline
		}
		return nil, err
	}
	a, err := p.Wait(ctx, relayTimeout)
	if err != nil {
		return nil, err
	}
	if a.Hello.ErrorCode != "" {
		_ = a.Conn.Close(websocket.StatusNormalClosure, "worker refused")
		p.Finish()
		return nil, fmt.Errorf("%w: %s", ErrWorkerDial, a.Hello.ErrorCode)
	}
	a.Conn.SetReadLimit(tunnel.ReadLimit)
	cctx, cancel := context.WithCancel(context.Background())
	return &relayConn{Conn: websocket.NetConn(cctx, a.Conn, websocket.MessageBinary), ws: a.Conn, p: p, cancel: cancel}, nil
}

// relayConn is the server end of one preview data connection. Writes are cut into
// messages no larger than the peer's read limit; Close ends the rendezvous so the
// worker-side handler returns.
type relayConn struct {
	net.Conn
	ws     *websocket.Conn
	p      *tunnel.Pending
	cancel context.CancelFunc
	once   sync.Once
}

func (c *relayConn) Write(b []byte) (int, error) {
	n := 0
	for len(b) > 0 {
		k := len(b)
		if k > tunnel.MaxChunk {
			k = tunnel.MaxChunk
		}
		w, err := c.Conn.Write(b[:k])
		n += w
		if err != nil {
			return n, err
		}
		b = b[k:]
	}
	return n, nil
}

func (c *relayConn) Close() error {
	var err error
	c.once.Do(func() {
		err = c.ws.Close(websocket.StatusNormalClosure, "closed")
		c.cancel()
		c.p.Finish()
	})
	return err
}

// AcceptWorker serves one preview-connect WebSocket the HTTP layer has already
// authenticated as workerID: it reads the hello, checks the one-time nonce against the
// rendezvous (worker, instance, lease, job, connection) and hands the connection to the
// waiting proxy dial, then blocks until that connection is finished.
func (m *Manager) AcceptWorker(ctx context.Context, workerID string, conn *websocket.Conn) {
	defer conn.Close(websocket.StatusInternalError, "closed")
	conn.SetReadLimit(tunnel.ReadLimit)
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var h wsproto.PreviewHello
	if err := wsjson.Read(rctx, conn, &h); err != nil {
		_ = conn.Close(websocket.StatusProtocolError, "expected hello")
		return
	}
	b, ok := m.relay.Consume(h.RelayNonce, time.Now())
	if !ok {
		_ = conn.Close(4401, "invalid nonce")
		return
	}
	m.mu.Lock()
	hub := m.hub
	m.mu.Unlock()
	inst, live := "", false
	if hub != nil {
		inst, live = hub.LiveInstance(b.WorkerID)
	}
	switch {
	case b.WorkerID != workerID:
		_ = conn.Close(4409, "worker mismatch")
		return
	case !live || inst != b.InstanceID:
		_ = conn.Close(4409, "instance mismatch")
		return
	case h.ConnID != b.TunnelID || h.PreviewID != b.CallerID || h.JobID != b.Target:
		_ = conn.Close(4404, "preview mismatch")
		return
	}
	done, ok := m.relay.Deliver(h.ConnID, tunnel.Arrival{Conn: conn, Hello: tunnel.Hello{TunnelID: h.ConnID, RelayNonce: h.RelayNonce, ErrorCode: h.ErrorCode, Error: h.Error}})
	if !ok {
		_ = conn.Close(4404, "gone")
		return
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// NotePresence reacts to a worker coming online or going offline: when it went offline,
// or came back as a different process, every lease bound to the old process is closed with
// reason worker_lost and never rebound to the new one.
func (m *Manager) NotePresence(workerID string, online bool) {
	m.mu.Lock()
	h := m.hub
	m.mu.Unlock()
	inst := ""
	if online && h != nil {
		inst, _ = h.LiveInstance(workerID)
	}
	recs, err := m.store.ListActivePreviews()
	if err != nil {
		return
	}
	for _, p := range recs {
		if p.WorkerID == workerID && (!online || p.WorkerInstanceID != inst) {
			_ = m.Close(p.ID, "worker_lost")
		}
	}
}

// workerAlive reports whether a lease's worker process is still the one it was created
// against (always true for a local lease).
func (m *Manager) workerAlive(worker, instance string) bool {
	if worker == localWorker {
		return true
	}
	m.mu.Lock()
	h := m.hub
	m.mu.Unlock()
	if h == nil {
		return false
	}
	inst, ok := h.LiveInstance(worker)
	return ok && inst == instance
}
