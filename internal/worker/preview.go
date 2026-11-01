package worker

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/tunnel"
	"github.com/inhere/gofer/internal/wsproto"
)

// Limits of the preview relay. A worker serves a handful of browsers at most; the cap
// only keeps a misbehaving server from exhausting file descriptors.
const (
	previewMaxConns    = 256
	previewDialTimeout = 5 * time.Second
	previewNonceTTL    = 10 * time.Minute
)

// previewRelays tracks the preview data connections of one worker process: the open ones
// by job (so a finished job or a lost connection cuts them) and the nonces already used
// (a replayed preview_open is refused).
type previewRelays struct {
	mu     sync.Mutex
	byJob  map[string]map[net.Conn]struct{}
	active int
	seen   map[string]time.Time
}

// claim records a nonce as used; false means it was already used.
func (p *previewRelays) claim(nonce string, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.seen == nil {
		p.seen = map[string]time.Time{}
	}
	for k, at := range p.seen {
		if now.Sub(at) > previewNonceTTL {
			delete(p.seen, k)
		}
	}
	if _, dup := p.seen[nonce]; dup {
		return false
	}
	p.seen[nonce] = now
	return true
}

// add registers an open connection for jobID; false means the worker is at its cap.
func (p *previewRelays) add(jobID string, c net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.active >= previewMaxConns {
		return false
	}
	if p.byJob == nil {
		p.byJob = map[string]map[net.Conn]struct{}{}
	}
	if p.byJob[jobID] == nil {
		p.byJob[jobID] = map[net.Conn]struct{}{}
	}
	p.byJob[jobID][c] = struct{}{}
	p.active++
	return true
}

func (p *previewRelays) remove(jobID string, c net.Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if set := p.byJob[jobID]; set != nil {
		if _, ok := set[c]; ok {
			delete(set, c)
			p.active--
			if len(set) == 0 {
				delete(p.byJob, jobID)
			}
		}
	}
}

// closeJob closes every preview connection of jobID.
func (p *previewRelays) closeJob(jobID string) {
	p.mu.Lock()
	set := p.byJob[jobID]
	conns := make([]net.Conn, 0, len(set))
	for c := range set {
		conns = append(conns, c)
	}
	p.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

// closeAll closes every preview connection (the hub connection dropped).
func (p *previewRelays) closeAll() {
	p.mu.Lock()
	var conns []net.Conn
	for _, set := range p.byJob {
		for c := range set {
			conns = append(conns, c)
		}
	}
	p.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

// previewJobRunning reports whether remoteID is a job this process still runs: tracked,
// no terminal Result cached and no terminal local status.
func (cl *Client) previewJobRunning(remoteID string) bool {
	cl.inflMu.Lock()
	defer cl.inflMu.Unlock()
	f := cl.inflight[remoteID]
	return f != nil && f.result == nil && !job.IsTerminal(f.status)
}

// previewRefusal checks a preview_open against this process; it returns the refusal
// code, or "" when the open may proceed. The target is never taken from the frame: the
// only thing dialled is 127.0.0.1 on a valid port.
func (cl *Client) previewRefusal(o wsproto.PreviewOpen) (code, msg string) {
	switch {
	case o.InstanceID != cl.instanceID:
		return wsproto.PreviewCodeInstance, "preview_open is for another worker instance"
	case o.Port < 1 || o.Port > 65535:
		return wsproto.PreviewCodeBadPort, "port must be between 1 and 65535"
	case !cl.previewJobRunning(o.JobID):
		return wsproto.PreviewCodeJobNotRunning, "job is not running on this worker"
	case o.RelayNonce == "" || !cl.previews.claim(o.RelayNonce, time.Now()):
		return wsproto.PreviewCodeNonceReplay, "relay nonce is missing or already used"
	}
	return "", ""
}

// handlePreviewOpen serves one preview_open: validate, dial 127.0.0.1:<port>, call the hub
// back on the preview-connect endpoint with a hello and relay bytes until either side
// closes, the job ends or the hub connection drops. It always answers with a hello (a
// refusal carries its code) so the server does not wait out its rendezvous timeout.
func (cl *Client) handlePreviewOpen(ctx context.Context, sessionURL string, o wsproto.PreviewOpen) {
	code, msg := cl.previewRefusal(o)
	var nc net.Conn
	if code == "" {
		d := net.Dialer{Timeout: previewDialTimeout}
		var err error
		nc, err = d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(o.Port)))
		if err != nil {
			code, msg = wsproto.PreviewCodeDialFailed, "could not connect to the application port"
		} else if !cl.previews.add(o.JobID, nc) {
			_ = nc.Close()
			nc = nil
			code, msg = wsproto.PreviewCodeTooManyConnect, "too many preview connections"
		} else {
			defer cl.previews.remove(o.JobID, nc)
			defer nc.Close()
			// The job may have ended between the check and the registration.
			if !cl.previewJobRunning(o.JobID) {
				code, msg = wsproto.PreviewCodeJobNotRunning, "job is not running on this worker"
			}
		}
	}
	if code != "" {
		slog.Warn("preview.rejected", "event", "preview.rejected", "component", "worker", "worker_id", cl.workerID, "preview_id", o.PreviewID, "job_id", o.JobID, "error_code", code)
	}
	u, err := deriveConnectURL(sessionURL, wsproto.PreviewConnectPath)
	if err != nil {
		return
	}
	h := http.Header{}
	if cl.token != "" {
		h.Set("Authorization", "Bearer "+cl.token)
	}
	ws, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		slog.Error("preview.error", "event", "preview.error", "component", "worker", "worker_id", cl.workerID, "preview_id", o.PreviewID, "error", err)
		return
	}
	defer ws.Close(websocket.StatusNormalClosure, "")
	ws.SetReadLimit(tunnel.ReadLimit)
	hello := wsproto.PreviewHello{PreviewID: o.PreviewID, JobID: o.JobID, ConnID: o.ConnID, RelayNonce: o.RelayNonce, ErrorCode: code, Error: msg}
	if err = wsjson.Write(ctx, ws, hello); err != nil || code != "" {
		return
	}
	started := time.Now()
	r := tunnel.BridgeWithOptions(ctx, ws, nc, tunnel.BridgeOptions{})
	slog.Info("preview.closed", "event", "preview.closed", "component", "worker", "worker_id", cl.workerID, "preview_id", o.PreviewID, "job_id", o.JobID,
		"bytes_down", r.ToWS, "bytes_up", r.FromWS, "duration_ms", time.Since(started).Milliseconds(), "close_reason", r.Reason)
}
