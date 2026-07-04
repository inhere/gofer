// Package worker is the ws-worker client (main plan §4, §6): a `gofer worker`
// process dials the central hub over a single WebSocket, registers, and then
// receives job dispatches which it runs LOCALLY with its own job.Service /
// local runner (review #8: the worker re-validates project/agent/exec with its
// own config). It streams each local job's stdout/stderr back to the hub as log
// frames and pushes the authoritative terminal result.
//
// WP3/C7 scope: Run is a reconnect loop with exponential backoff + full jitter
// that rotates through MULTIPLE hub addresses (server_link.urls) on failure,
// re-registering on each (re)connect (§5.2). Each connection runs a heartbeat
// ping sender + a read-deadline'd recv loop so a half-open hub is detected
// (§5.1). The loop exits only when ctx is cancelled (worker shutdown).
package worker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	mathrand "math/rand"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/inhere/gofer/internal/job"
	ptyrunner "github.com/inhere/gofer/internal/runner/pty"
	"github.com/inhere/gofer/internal/wsproto"
)

// newInstanceID mints a per-process nonce sent in the register frame so the hub can
// distinguish a transient reconnect (same instance) from a worker restart (new
// instance under the same worker_id) — see wsproto.Register.InstanceID / z8ow. It is
// generated ONCE at client construction and reused across reconnects. A crypto/rand
// failure (never in practice) degrades to a fixed string: the hub then treats every
// reconnect as a restart (fails in-flight jobs) — safe, just less precise.
func newInstanceID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "inst-fallback"
	}
	return hex.EncodeToString(b[:])
}

// maxWSReadBytes caps a single inbound message on the worker side (mirrors the
// hub). Var so tests can shrink it.
var maxWSReadBytes int64 = 8 << 20

// builtinLocalRunner is the runner the worker always executes with locally.
const builtinLocalRunner = "local"

// Jobs is the subset of job.Service the worker client needs. job.Service
// satisfies it; an interface keeps the client testable.
type Jobs interface {
	Submit(req job.JobRequest) (job.JobResult, error)
	Get(id string) (job.JobResult, bool)
	Wait(id string) (job.JobResult, bool)
	// Cancel cancels a running local job (P2 cancel frame). Stable no-op for a
	// terminal job, error only for an unknown id (mirrors job.Service.Cancel).
	Cancel(id string) error
	// GetInteractions returns the local job's interactions so the client can bridge
	// new pending ones to the hub as interaction{open} frames (P2).
	GetInteractions(id string) ([]job.Interaction, error)
	// AnswerInteraction delivers the hub's answer to the local job so it resumes
	// (P2 answer frame).
	AnswerInteraction(jobID, interactionID, answer string) (job.Interaction, error)
}

// Client connects one worker to the hub. It is constructed with the resolved hub
// address list + token + the worker's identity and local job service.
type Client struct {
	workerID string
	// instanceID is this process's nonce (minted once in New, reused across
	// reconnects) sent in every register frame so the hub can tell a reconnect from
	// a restart (z8ow). See newInstanceID / wsproto.Register.InstanceID.
	instanceID string
	urls       []string // hub addresses; rotated on connect failure (C7, §5.2)
	token      string
	labels     []string
	projects   []string
	agents     []string
	maxConc    int

	backoff      backoffPolicy
	pingInterval time.Duration
	readDeadline time.Duration

	jobs Jobs

	conn    *websocket.Conn
	writeMu sync.Mutex

	// jobMap maps the hub-side job_id (the wire id) to the worker's LOCAL job id,
	// so an inbound cancel/answer frame (keyed by the hub id) targets the right
	// local job. handleDispatch registers the entry once the local job is submitted
	// and removes it when the dispatch finishes.
	jobMu  sync.Mutex
	jobMap map[string]string

	// sessMu guards the interactive-session rendezvous + pendingCancel state below
	// (D-P2-3 / D-P2-9). The PtyRunner observer callback (OnSessionStart) and the
	// per-dispatch handleDispatch goroutine (waitSession) meet here.
	sessMu sync.Mutex
	// sessReady buffers a started session when OnSessionStart fires BEFORE
	// handleDispatch calls waitSession (localID → sess). sessWaiters parks a waiter
	// chan when waitSession runs FIRST; whichever arrives second delivers the sess.
	// Either ordering is safe (rendezvous).
	sessReady   map[string]*ptyrunner.PtySession
	sessWaiters map[string]chan *ptyrunner.PtySession
	// pendingCancel holds hub job_ids whose cancel frame arrived BEFORE the local
	// job mapping existed (D-P2-9). handleDispatch consumes it after putJobMapping
	// (and on its exit path) so an early cancel is not lost. pendingOrder tracks
	// insertion order for the soft-cap sweep (stale entries for jobs this worker was
	// never dispatched).
	pendingCancel map[string]struct{}
	pendingOrder  []string

	// pollInterval is how often streamLocalJob tails the local log files. Var per
	// instance so tests can speed it up.
	pollInterval time.Duration

	// pumpPtyFn launches the interactive pty pump for one dispatch (T5), returning
	// a pumpDone chan handleDispatch joins before sending the terminal Result. It
	// defaults to the real pumpPty (set in New); a test overrides it to inject a
	// controllable pumpDone (join-ordering assertions) without a real second ws or
	// pty session.
	pumpPtyFn func(ctx context.Context, sessionURL, localID, remoteJobID, ptySessionID, nonce string, sess ptySession) <-chan struct{}

	// onSession, when set, is called after a session ends (for test
	// synchronisation: connect / register / disconnect observation). nil in prod.
	onSession func(event string)
}

// Config is the resolved worker-client wiring (the command resolves env/URLs).
// URLs may list multiple hub addresses (C7 failover); InitialBackoff/MaxBackoff/
// PingInterval/ReadDeadline are 0-defaulted to the package constants. Rng is the
// jitter source (nil = time-seeded; tests inject a deterministic one).
type Config struct {
	WorkerID       string
	URLs           []string
	Token          string
	Labels         []string
	Projects       []string
	Agents         []string
	MaxConc        int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	PingInterval   time.Duration
	ReadDeadline   time.Duration
	Rng            *mathrand.Rand
}

// New builds a worker client. jobs is the worker's local job service (built from
// its own config by the command).
func New(cfg Config, jobs Jobs) *Client {
	ping := cfg.PingInterval
	if ping <= 0 {
		ping = DefaultPingInterval
	}
	read := cfg.ReadDeadline
	if read <= 0 {
		read = DefaultReadDeadline
	}
	if read < 2*ping {
		read = 3 * ping
	}
	cl := &Client{
		workerID:      cfg.WorkerID,
		instanceID:    newInstanceID(),
		urls:          cfg.URLs,
		token:         cfg.Token,
		labels:        cfg.Labels,
		projects:      cfg.Projects,
		agents:        cfg.Agents,
		maxConc:       cfg.MaxConc,
		backoff:       newBackoffPolicy(cfg.InitialBackoff, cfg.MaxBackoff, cfg.Rng),
		pingInterval:  ping,
		readDeadline:  read,
		jobs:          jobs,
		jobMap:        map[string]string{},
		sessReady:     map[string]*ptyrunner.PtySession{},
		sessWaiters:   map[string]chan *ptyrunner.PtySession{},
		pendingCancel: map[string]struct{}{},
		pollInterval:  200 * time.Millisecond,
	}
	cl.pumpPtyFn = cl.pumpPty // real pump by default; tests override for join assertions
	return cl
}

// putJobMapping records the hub job_id → local job id mapping (handleDispatch).
func (cl *Client) putJobMapping(remoteID, localID string) {
	cl.jobMu.Lock()
	cl.jobMap[remoteID] = localID
	cl.jobMu.Unlock()
}

// localJobID resolves the hub job_id to the worker's local job id (empty if the
// dispatch is unknown / already cleaned up).
func (cl *Client) localJobID(remoteID string) string {
	cl.jobMu.Lock()
	defer cl.jobMu.Unlock()
	return cl.jobMap[remoteID]
}

// dropJobMapping removes the mapping once a dispatch finishes.
func (cl *Client) dropJobMapping(remoteID string) {
	cl.jobMu.Lock()
	delete(cl.jobMap, remoteID)
	cl.jobMu.Unlock()
}

// The worker Client is the pty session observer on the worker side (wired by the
// worker command via PtyRunner.SetObserver; the serve side never sets it → nil).
var _ ptyrunner.SessionObserver = (*Client)(nil)

// OnSessionStart implements ptyrunner.SessionObserver: the PtyRunner calls it
// (synchronously, once) right after a pty session starts, handing us SOLE-reader
// ownership. It MUST NOT block (SessionObserver contract) — it only delivers the
// session to a parked waiter or buffers it for a waiter that has not arrived yet
// (the pump itself is started later by handleDispatch, T5). localID is the
// worker's LOCAL job id (= the id waitSession keys on).
func (cl *Client) OnSessionStart(localID string, sess *ptyrunner.PtySession) {
	cl.sessMu.Lock()
	if ch := cl.sessWaiters[localID]; ch != nil {
		// waitSession arrived first: hand off directly (ch is buffered, never blocks).
		delete(cl.sessWaiters, localID)
		cl.sessMu.Unlock()
		ch <- sess
		return
	}
	// observer arrived first: buffer for the waiter that will call waitSession next.
	cl.sessReady[localID] = sess
	cl.sessMu.Unlock()
}

// waitSession blocks until the interactive session for localID starts (returning
// it) or the wait is abandoned (returning nil). It resolves immediately if the
// observer already buffered the session; otherwise it parks a waiter and wakes on
// one of three signals: the session starting, the local job reaching a terminal
// state (jobs.Wait — e.g. a pre-pty submit failure or an early cancel), or ctx
// cancellation (the dispatch is being torn down). On a nil return the waiter is
// cleaned up so a late OnSessionStart does not leak.
func (cl *Client) waitSession(ctx context.Context, localID string) *ptyrunner.PtySession {
	cl.sessMu.Lock()
	if s := cl.sessReady[localID]; s != nil {
		delete(cl.sessReady, localID)
		cl.sessMu.Unlock()
		return s
	}
	ch := make(chan *ptyrunner.PtySession, 1)
	cl.sessWaiters[localID] = ch
	cl.sessMu.Unlock()

	// jobs.Wait returns once the local job is terminal; closing term wakes us so a
	// job that ended before its pty ever started does not hang the dispatch.
	term := make(chan struct{})
	go func() { cl.jobs.Wait(localID); close(term) }()

	select {
	case s := <-ch:
		return s
	case <-term:
		cl.clearWaiter(localID)
		return nil
	case <-ctx.Done():
		cl.clearWaiter(localID)
		return nil
	}
}

// clearWaiter removes a parked waiter (and any late-buffered session) for localID
// when waitSession gave up. Both deletes are no-ops if OnSessionStart already
// consumed the waiter, so this is safe to call unconditionally on the nil path.
func (cl *Client) clearWaiter(localID string) {
	cl.sessMu.Lock()
	delete(cl.sessWaiters, localID)
	delete(cl.sessReady, localID)
	cl.sessMu.Unlock()
}

// pendingCancelCap soft-bounds the pendingCancel map: a cancel for a job this
// worker was never dispatched (nothing ever consumes it) would otherwise linger.
// The sweep in recordPendingCancel evicts the oldest ids past this cap.
const pendingCancelCap = 256

// recordPendingCancel notes that a cancel frame for hub job_id remoteID arrived
// before the local mapping existed (recvLoop cancel branch, D-P2-9). It is later
// consumed by takePendingCancel. A soft-cap sweep evicts the oldest ids so a
// stream of cancels for never-dispatched jobs cannot grow the map unboundedly.
func (cl *Client) recordPendingCancel(remoteID string) {
	cl.sessMu.Lock()
	if _, dup := cl.pendingCancel[remoteID]; !dup {
		cl.pendingCancel[remoteID] = struct{}{}
		cl.pendingOrder = append(cl.pendingOrder, remoteID)
	}
	// Evict oldest still-present ids while over the cap (ids already taken are
	// skipped by the delete no-op, so the loop pops until enough live ids are gone).
	for len(cl.pendingCancel) > pendingCancelCap && len(cl.pendingOrder) > 0 {
		oldest := cl.pendingOrder[0]
		cl.pendingOrder = cl.pendingOrder[1:]
		delete(cl.pendingCancel, oldest)
	}
	// Compact pendingOrder when it accumulates taken (stale) ids without ever
	// tripping the cap sweep, keeping only ids still live and preserving order.
	if len(cl.pendingOrder) > 2*pendingCancelCap {
		kept := cl.pendingOrder[:0]
		for _, id := range cl.pendingOrder {
			if _, ok := cl.pendingCancel[id]; ok {
				kept = append(kept, id)
			}
		}
		cl.pendingOrder = kept
	}
	cl.sessMu.Unlock()
}

// takePendingCancel reports whether a cancel for remoteID was recorded before the
// mapping existed, consuming the record (D-P2-9). handleDispatch calls it after
// putJobMapping (to cancel the freshly-submitted local job) and on its exit path
// (to clear any stale record for a dispatch that never mapped).
func (cl *Client) takePendingCancel(remoteID string) bool {
	cl.sessMu.Lock()
	_, ok := cl.pendingCancel[remoteID]
	delete(cl.pendingCancel, remoteID)
	cl.sessMu.Unlock()
	return ok
}

// Run is the worker's reconnect supervisor (C7, §5.2): it repeatedly dials a hub
// address (rotating through cl.urls on failure), registers and runs one
// connection session until the connection drops, then backs off (exponential +
// full jitter, reset after a successful registration) and retries. It returns
// only when ctx is cancelled (worker shutdown / signal) — a transient hub
// outage never permanently disconnects the worker.
func (cl *Client) Run(ctx context.Context) error {
	if len(cl.urls) == 0 {
		return errors.New("worker: no hub urls configured")
	}
	idx := 0
	attempt := 0
	for {
		if ctx.Err() != nil {
			return nil // worker shutdown
		}
		url := cl.urls[idx]
		registered, err := cl.runSession(ctx, url)
		if registered {
			// A session that got registered resets the backoff so the next reconnect
			// (after a clean/transient drop) starts fast again (§4.2).
			attempt = 0
		}
		if ctx.Err() != nil {
			return nil // shut down during the session
		}
		// Connect/register failed or the session dropped: rotate to the next address
		// and back off before retrying.
		idx = (idx + 1) % len(cl.urls)
		wait := cl.backoff.next(attempt)
		attempt++
		if err != nil {
			cl.notify("retry:" + err.Error())
			// Surface WHY a connect/session failed + when we retry — the operator's
			// main signal that a worker is not reaching the hub (bad token/binding,
			// wrong url, hub down). The err already carries the cause (dial /
			// register rejection / disconnect).
			slog.Warn("worker reconnecting to hub",
				"worker_id", cl.workerID, "retry_in", wait.String(), "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
	}
}

// runSession dials url, registers and runs ONE connection's recv loop. It returns
// registered=true once the registered{accepted:true} ack was received (so the
// supervisor can reset the backoff), and the error that ended the session (dial
// error, register rejection, or recv-loop disconnect). The connection is always
// closed before returning.
func (cl *Client) runSession(ctx context.Context, url string) (registered bool, err error) {
	header := http.Header{}
	if cl.token != "" {
		header.Set("Authorization", "Bearer "+cl.token)
	}
	conn, _, derr := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header})
	if derr != nil {
		return false, fmt.Errorf("dial hub %s: %w", url, derr)
	}
	conn.SetReadLimit(maxWSReadBytes)
	cl.conn = conn
	// going-away (1001) on a clean shutdown; the deferred close also covers the
	// drop/error paths so the fd is always released (no leak, §5.6).
	defer conn.Close(websocket.StatusGoingAway, "worker session end")

	// register → registered (bare ctx; the read deadline governs the steady-state
	// recv loop, not the handshake).
	if err := cl.writeFrame(ctx, wsproto.TypeRegister, "", wsproto.Register{
		WorkerID:      cl.workerID,
		InstanceID:    cl.instanceID,
		PtyCapable:    ptyrunner.Available(),
		OS:            runtime.GOOS,
		Labels:        cl.labels,
		Projects:      cl.projects,
		Agents:        cl.agents,
		MaxConcurrent: cl.maxConc,
	}); err != nil {
		return false, fmt.Errorf("send register: %w", err)
	}
	env, err := cl.readEnvelope(ctx)
	if err != nil {
		return false, fmt.Errorf("read registered: %w", err)
	}
	reg, _ := wsproto.As[wsproto.Registered](env)
	if !reg.Accepted {
		// A binding/token mismatch will not self-heal, but the supervisor still
		// retries (the config may be fixed) — just backed off (§5.2).
		slog.Warn("worker registration rejected by hub",
			"worker_id", cl.workerID, "url", url, "reason", reg.Reason)
		return false, fmt.Errorf("register rejected: %s", reg.Reason)
	}
	cl.notify("registered")
	slog.Info("worker registered with hub",
		"worker_id", cl.workerID, "url", url, "labels", cl.labels, "max_concurrent", cl.maxConc)

	// Per-session heartbeat: start the ping sender, stop it when the recv loop ends.
	done := make(chan struct{})
	defer close(done)
	cl.startHeartbeat(ctx, done)

	err = cl.recvLoop(ctx, url)
	cl.notify("disconnected")
	slog.Info("worker disconnected from hub", "worker_id", cl.workerID, "err", err)
	return true, err
}

// recvLoop is the single read goroutine for one connection: it reads frames with
// a per-read deadline (half-open hub detection, §5.1) and dispatches each. A
// dispatch is handled in its own goroutine so the worker runs multiple jobs
// concurrently; control frames (cancel/answer/ping) are handled inline. It
// returns the error that ended the connection (disconnect / read-deadline / ctx).
// url is THIS session's hub address; it is threaded to handleDispatch so an
// interactive dispatch derives its pty-connect URL from the same hub it arrived
// on (D-P2-7 per-dispatch URL).
func (cl *Client) recvLoop(ctx context.Context, url string) error {
	for {
		rctx, cancel := context.WithTimeout(ctx, cl.readDeadline)
		env, err := cl.readEnvelope(rctx)
		cancel()
		if err != nil {
			return err // disconnect / read-deadline / ctx done
		}
		switch env.Type {
		case wsproto.TypeDispatch:
			d, derr := wsproto.As[wsproto.Dispatch](env)
			if derr != nil {
				continue
			}
			go cl.handleDispatch(ctx, url, d)
		case wsproto.TypeCancel:
			// P2: cancel the matching local job. job.Service.Cancel is a stable no-op
			// for a terminal/unknown local job, so an unmapped/late cancel is safe.
			cf, derr := wsproto.As[wsproto.Cancel](env)
			if derr != nil {
				continue
			}
			if localID := cl.localJobID(cf.JobID); localID != "" {
				_ = cl.jobs.Cancel(localID)
			} else {
				// D-P2-9: the cancel raced ahead of putJobMapping (or targets a
				// not-yet-dispatched job). Record it so handleDispatch cancels the
				// local job as soon as the mapping is established.
				cl.recordPendingCancel(cf.JobID)
			}
		case wsproto.TypeAnswer:
			// P2: deliver the hub answer to the local job so it resumes. The
			// interaction id is the LOCAL id (the worker generated it on the open
			// frame), so it maps 1:1.
			af, derr := wsproto.As[wsproto.Answer](env)
			if derr != nil {
				continue
			}
			if localID := cl.localJobID(af.JobID); localID != "" {
				_, _ = cl.jobs.AnswerInteraction(localID, af.InteractionID, af.Answer)
			}
		case wsproto.TypePing:
			// P3: the hub pings us; reply pong{ts} (symmetric, §5.1). Reading the
			// frame already proves the connection is alive (refreshes our own read
			// deadline on the next iteration).
			pf, _ := wsproto.As[wsproto.Ping](env)
			_ = cl.writeFrame(ctx, wsproto.TypePong, "", wsproto.Pong{TS: pf.TS})
		case wsproto.TypePong:
			// P3: reply to our own ping; reading it is enough (read deadline reset).
		}
	}
}

// notify invokes the optional onSession hook (test synchronisation; no-op in prod).
func (cl *Client) notify(event string) {
	if cl.onSession != nil {
		cl.onSession(event)
	}
}

// writeFrame marshals a typed payload into an envelope and writes it under
// writeMu (coder/websocket requires a single concurrent writer).
func (cl *Client) writeFrame(ctx context.Context, t wsproto.FrameType, jobID string, payload any) error {
	cl.writeMu.Lock()
	defer cl.writeMu.Unlock()
	return wsjson.Write(ctx, cl.conn, wsproto.Envelope{Type: t, JobID: jobID, Payload: mustRaw(payload)})
}

func (cl *Client) readEnvelope(ctx context.Context) (wsproto.Envelope, error) {
	var env wsproto.Envelope
	if err := wsjson.Read(ctx, cl.conn, &env); err != nil {
		return wsproto.Envelope{}, err
	}
	return env, nil
}
