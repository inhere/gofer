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
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	mathrand "math/rand"
	"net"
	"net/http"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/daemon"
	"github.com/inhere/gofer/internal/job"
	ptyrunner "github.com/inhere/gofer/internal/runner/pty"
	"github.com/inhere/gofer/internal/store"
	"github.com/inhere/gofer/internal/wsproto"
	"github.com/inhere/gofer/internal/xfer"
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

// readHostname returns this machine's hostname for the register frame's node
// info; a failure just yields "" (the field is display-only, omitempty).
func readHostname() string {
	hn, err := os.Hostname()
	if err != nil {
		return ""
	}
	return hn
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
	// SetEventObserver installs the callback that receives the whitelisted job events
	// this service records for a DISPATCHED job (SUP-01 G); nil clears it. The
	// service decides which types are mirrorable (job.mirroredEventTypes) — the
	// client only transports them.
	SetEventObserver(job.JobEventObserver)
	// SetXferBridge installs the job file seam (XFER-01 X2): the service uses it to
	// place a dispatched job's staged uploads in this machine's cwd and to filter what
	// its collect globs report. The client supplies one (see New).
	SetXferBridge(job.XferBridge)
	// Config returns the worker's CURRENT runtime config. The client needs it for the
	// work that must be resolved on THIS machine rather than by the hub: a file
	// transfer's project root (XFER-01 re-validates the dispatch's project/path against
	// the worker's own config, exactly like a locally submitted job's --cwd). It is a
	// live accessor, not a snapshot, so a reloaded config is honoured.
	Config() *config.Config
}

// Client connects one worker to the hub. It is constructed with the resolved hub
// address list + token + the worker's identity and local job service.
type Client struct {
	workerID string
	// instanceID is this process's nonce (minted once in New, reused across
	// reconnects) sent in every register frame so the hub can tell a reconnect from
	// a restart (z8ow). See newInstanceID / wsproto.Register.InstanceID.
	instanceID   string
	urls         []string // hub addresses; rotated on connect failure (C7, §5.2)
	token        string
	tunnelPolicy atomic.Pointer[tunnelPolicy]
	tunnelMu     sync.Mutex
	tunnelActive int

	// caps is the config-derived capability snapshot this worker advertises
	// (labels / projects / agents / typed agent caps / max_concurrent). It is what
	// every register frame reports, and a successful reload REPLACES it wholesale
	// (storeCaps), so a reconnect after a reload registers with the CURRENT config
	// rather than the one the process booted with. capsMu guards it: the reload
	// executor writes it while the reconnect loop reads it on register.
	capsMu sync.RWMutex
	caps   wsproto.Caps

	// goferVersion is buildinfo.Info.DisplayVersion(), threaded from the worker
	// command; startedAt is this process's start time (unix sec). Both are node
	// info reported on register for observability.
	goferVersion string
	startedAt    int64
	// hostname identifies the machine this worker runs on (os.Hostname(), read once
	// at construction). Reported on register as node info: the hub's remote addr can
	// be a NAT/bridge address, so the self-reported hostname is what actually tells
	// an operator which box a worker is.
	hostname string

	// reloadFn re-reads + applies the worker's config and re-derives caps; it is
	// injected by the command (see ReloadFunc). reloadCh feeds the SINGLE reload
	// executor goroutine (reloadLoop), which is what keeps concurrent reload
	// requests strictly ordered.
	reloadFn ReloadFunc
	reloadCh chan reloadReq

	// policyMode is true when this worker sources its projects from server-pushed
	// Policy (worker.yaml has `roots`, T5-A modePolicy). LEGACY/EMPTY workers set it
	// false: they never apply a pushed Policy, only reply an Applied{legacy_local_projects}
	// so the hub clears its pending Rev (verification 1).
	policyMode bool
	// st is the single source of truth for the policy session (gen/lastRev/pending/
	// lastPolicy, T5-C). applyMu is the B1 common-commit lock: it makes the executor's
	// fence+apply+commit mutually exclusive with beginSession's gen turnover. Lock order
	// is ALWAYS applyMu → st.mu. policyWake (cap 1) is the B2 merged wake feeding the
	// reload executor's third source.
	st         sessionState
	applyMu    sync.Mutex
	policyWake chan struct{}
	// afterTakePendingHook is a test-only seam fired between taking a pending policy
	// and acquiring applyMu, to model a beginSession racing the apply (F-B1). nil in prod.
	afterTakePendingHook func()
	// beforeParkHook is a test-only seam fired just before the executor parks in its
	// select, to model an offer landing in the pre-park lost-wakeup window. nil in prod.
	beforeParkHook func()

	// Last-known-good policy cache (T5-F). cachePath is the on-disk file
	// (<config-dir>/run/worker-<id>.policy.json); empty disables caching (LEGACY / tests).
	// applySeq is the monotonic apply token; a cache write is dropped when its seq is no
	// longer the latest (guards a late retry from overwriting a newer Rev). cacheMu guards
	// the file I/O + the retry pending value and NEVER takes st.mu/applyMu/updateMu (G-H1).
	cachePath         string
	applySeq          atomic.Uint64
	cacheMu           sync.Mutex
	cacheRetryCh      chan struct{}
	cacheRetryPending *pendingCacheWrite
	// writeCacheFn does the actual atomic file write (default: WritePolicyCacheFile bound
	// to cachePath/workerID). A test overrides it to inject a rename failure (verification 9).
	writeCacheFn func(p *wsproto.Policy, seq uint64) error

	backoff      backoffPolicy
	pingInterval time.Duration
	readDeadline time.Duration

	jobs Jobs

	conn    *websocket.Conn
	writeMu sync.Mutex
	// dispatchWG tracks per-dispatch goroutines spawned by recvLoop. Production
	// does not wait on it during reconnect, but tests can use WaitIdle after
	// cancelling Run to make teardown deterministic.
	dispatchWG sync.WaitGroup

	// jobMap maps the hub-side job_id (the wire id) to the worker's LOCAL job id,
	// so an inbound cancel/answer frame (keyed by the hub id) targets the right
	// local job. handleDispatch registers the entry once the local job is submitted
	// and removes it when the dispatch finishes. localMap is the REVERSE direction
	// (local id → hub id), which the SUP-01 G event mirror needs: a job's events are
	// raised with its LOCAL id and must be addressed to the hub's.
	jobMu    sync.Mutex
	jobMap   map[string]string
	localMap map[string]string

	// jobEvents is the BOUNDED queue of mirrored job events (SUP-01 G) waiting to be
	// written to the hub. A full queue drops the event and counts it
	// (jobEventDropped) — a mirror is informational, and the JOB must never block on
	// a slow socket. jobEventLoop drains it; both live for the process's lifetime
	// (started by Run).
	jobEvents       chan wsproto.JobEvent
	jobEventDropped atomic.Int64

	// inflMu guards inflight: the RECOV-01 recovery table of the jobs this PROCESS
	// still owns on behalf of the hub (remote job_id → inflightJob). It is visible to
	// both sides of a reconnect — the per-dispatch streaming goroutines update it,
	// the register path (runSession) reads it — and it is a LEAF lock: it is never
	// held across a frame write or a call into the job service, so a stalled log
	// tailer can never delay a reconnect's register frame.
	inflMu   sync.Mutex
	inflight map[string]*inflightJob

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

	// beforeMapFn, when set, is called by handleDispatch once the local job exists and
	// immediately BEFORE the hub→local mapping is registered (nil in production). It
	// exists so a test can hold that window open and prove a cancel frame landing in it
	// still cancels the local job (F3, bd h-aii-tcpm).
	beforeMapFn func(remoteJobID, localID string)

	// xferSem bounds CONCURRENT file transfers (XFER-01): a transfer is a stream
	// through this process, so the cap keeps a burst from taking the worker's disk
	// and sockets away from its jobs. Sized once in New.
	xferSem chan struct{}
	// xferTimeout is this worker's single-transfer deadline, resolved from its own
	// config by worker.Serve (worker.xfer_timeout_sec). 0 = the default (10m).
	xferTimeout time.Duration

	// xferLimits are this worker's file-transfer caps (its own server.xfer), resolved
	// by the caller: the job file seam filters what it reports with them, and the
	// deadline for a fetch comes from xferTimeout.
	xferLimits xfer.Limits

	// baseMu guards hubBaseURL: the HTTP base of the hub the CURRENT session is
	// connected to, derived from that session's ws URL (one origin, no second address
	// to configure or to get wrong). It is where the job file seam (XFER-01 X2)
	// fetches a staged upload from; "" while no session is up.
	baseMu     sync.RWMutex
	hubBaseURL string
}

// Config is the resolved worker-client wiring (the command resolves env/URLs).
// URLs may list multiple hub addresses (C7 failover); InitialBackoff/MaxBackoff/
// PingInterval/ReadDeadline are 0-defaulted to the package constants. Rng is the
// jitter source (nil = time-seeded; tests inject a deterministic one).
type Config struct {
	WorkerID string
	URLs     []string
	Token    string
	Labels   []string
	Projects []string
	Agents   []string
	// AgentCaps is the typed agent capability report (built from the worker config's
	// agents map by the command); Agents stays the bare key list.
	AgentCaps []wsproto.AgentBrief
	// GoferVersion is the worker binary's display version (buildinfo).
	GoferVersion string
	MaxConc      int
	// XferLimits are this worker's file-transfer caps (its own config's server.xfer),
	// used by the job file seam (XFER-01 X2): the collect step filters what it reports
	// with them, and OwnsArtifacts stays false — the hub owns the job row, so it pulls
	// a collected file back itself.
	XferLimits xfer.Limits
	// Reload re-reads and applies the worker's config, returning the capabilities of
	// the config it applied (see ReloadFunc). Injected by the command; nil disables
	// config reload (a reload request is then answered with an error, never silently
	// accepted).
	Reload ReloadFunc
	// PolicyMode is true when the worker sources projects from server Policy (roots
	// configured, T5-A). LEGACY/EMPTY workers leave it false.
	PolicyMode bool
	// CachePath is the last-known-good policy cache file (POLICY mode only); empty
	// disables the cache. InitialPolicy seeds the in-memory LKG at construction (a
	// cold start that recovered a cached policy passes it here so a first SIGHUP
	// re-projects it, verification 9).
	CachePath      string
	InitialPolicy  *wsproto.Policy
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	PingInterval   time.Duration
	ReadDeadline   time.Duration
	Rng            *mathrand.Rand
	Tunnel         config.WorkerTunnelConfig
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
		workerID:   cfg.WorkerID,
		instanceID: newInstanceID(),
		urls:       cfg.URLs,
		token:      cfg.Token,
		caps: wsproto.Caps{
			Labels:    cfg.Labels,
			Projects:  cfg.Projects,
			Agents:    cfg.Agents,
			AgentCaps: cfg.AgentCaps,
			MaxConc:   cfg.MaxConc,
		},
		goferVersion:  cfg.GoferVersion,
		startedAt:     time.Now().Unix(),
		hostname:      readHostname(),
		reloadFn:      cfg.Reload,
		reloadCh:      make(chan reloadReq, reloadQueueCap),
		policyMode:    cfg.PolicyMode,
		policyWake:    make(chan struct{}, 1),
		cachePath:     cfg.CachePath,
		cacheRetryCh:  make(chan struct{}, 1),
		backoff:       newBackoffPolicy(cfg.InitialBackoff, cfg.MaxBackoff, cfg.Rng),
		pingInterval:  ping,
		readDeadline:  read,
		jobs:          jobs,
		jobMap:        map[string]string{},
		localMap:      map[string]string{},
		jobEvents:     make(chan wsproto.JobEvent, jobEventQueueCap),
		inflight:      map[string]*inflightJob{},
		sessReady:     map[string]*ptyrunner.PtySession{},
		xferSem:       make(chan struct{}, xferMaxConcurrent),
		sessWaiters:   map[string]chan *ptyrunner.PtySession{},
		pendingCancel: map[string]struct{}{},
		pollInterval:  200 * time.Millisecond,
	}
	cl.applyTunnel(outTunnel(cfg.Tunnel))
	// Seed the in-memory last-known-good so a SIGHUP before the first server Policy
	// re-projects the recovered cache rather than no-op'ing to empty (verification 9).
	if cfg.InitialPolicy != nil {
		p := *cfg.InitialPolicy
		cl.st.lastPolicy = &p
	}
	// Default cache writer binds the atomic file helper to this worker's path/id; a
	// test overrides writeCacheFn to inject a write failure.
	wid, path := cfg.WorkerID, cfg.CachePath
	cl.writeCacheFn = func(p *wsproto.Policy, seq uint64) error {
		return WritePolicyCacheFile(path, wid, p, seq)
	}
	cl.pumpPtyFn = cl.pumpPty // real pump by default; tests override for join assertions
	// SUP-01 G: mirror the whitelisted events the local job service raises for a
	// DISPATCHED job back to the hub that asked for it. Installing the observer here
	// (once per client) keeps the job service's event path free of hub knowledge. A
	// nil service (a client built only to exercise the connection, as some tests do)
	// simply has nothing to mirror.
	if jobs != nil {
		jobs.SetEventObserver(cl.observeJobEvent)
		// XFER-01 X2: the job file seam for jobs this worker executes. Same place and
		// same reason as the observer above — the job service must not know about the
		// hub, and the wiring belongs to the one thing that owns both. The caps are the
		// worker's OWN server.xfer, resolved by the caller from its own config.
		jobs.SetXferBridge(JobXferBridge(cl, cfg.XferLimits))
	}
	return cl
}

// jobEventQueueCap bounds the mirrored-event queue (SUP-01 G). Events are rare, so
// the cap only ever matters when the hub connection is stalled; overflowing drops and
// counts rather than delaying the job.
const jobEventQueueCap = 64

// observeJobEvent is the job service's event observer (SUP-01 G): it turns a
// whitelisted local event into a wire frame and ENQUEUES it (never blocks the job).
// A local job this worker runs on its own account has no hub id, so it is skipped —
// there is nobody to mirror it to.
func (cl *Client) observeJobEvent(localID, eventType string, detail map[string]any) {
	remoteID := cl.remoteJobID(localID)
	if remoteID == "" {
		return
	}
	ev := wsproto.JobEvent{JobID: remoteID, Type: eventType, TS: time.Now().Unix()}
	if len(detail) > 0 {
		if b, err := json.Marshal(detail); err == nil {
			ev.Detail = b
		}
		if id, ok := detail["interaction_id"].(string); ok {
			ev.InteractionID = id
		}
	}
	select {
	case cl.jobEvents <- ev:
	default:
		// The queue is full: drop and account for it. A mirror is informational (the
		// event is already durably recorded on this side) and must never push back on
		// the job that raised it.
		if n := cl.jobEventDropped.Add(1); n == 1 || n%100 == 0 {
			slog.Warn("worker.job_event_dropped", "event", "worker.job_event_dropped", "component", "worker",
				"worker_id", cl.workerID, "job_id", remoteID, "type", eventType, "dropped", n)
		}
	}
}

// jobEventLoop drains the mirrored-event queue onto the hub connection until the
// worker shuts down (SUP-01 G). Writes are best-effort: a frame that cannot go out
// (disconnected, or the run ctx ended) is dropped with a debug line — the worker's
// local record is authoritative, and the next reconnect has nothing to replay (the
// hub only ever ADDS these to the host job's event log).
func (cl *Client) jobEventLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-cl.jobEvents:
			if err := cl.writeFrame(ctx, wsproto.TypeJobEvent, ev.JobID, ev); err != nil {
				slog.Debug("worker.job_event_not_sent", "worker_id", cl.workerID,
					"job_id", ev.JobID, "type", ev.Type, "err", err)
			}
		}
	}
}

// putJobMapping records the hub job_id → local job id mapping (handleDispatch).
func (cl *Client) putJobMapping(remoteID, localID string) {
	cl.jobMu.Lock()
	cl.jobMap[remoteID] = localID
	cl.localMap[localID] = remoteID
	cl.jobMu.Unlock()
}

// localJobID resolves the hub job_id to the worker's local job id (empty if the
// dispatch is unknown / already cleaned up).
func (cl *Client) localJobID(remoteID string) string {
	cl.jobMu.Lock()
	defer cl.jobMu.Unlock()
	return cl.jobMap[remoteID]
}

// remoteJobID resolves the worker's local job id back to the hub job_id (empty for a
// job this worker runs on its own account, e.g. a locally submitted one).
func (cl *Client) remoteJobID(localID string) string {
	cl.jobMu.Lock()
	defer cl.jobMu.Unlock()
	return cl.localMap[localID]
}

// dropJobMapping removes the mapping once a dispatch finishes.
func (cl *Client) dropJobMapping(remoteID string) {
	cl.jobMu.Lock()
	delete(cl.localMap, cl.jobMap[remoteID])
	delete(cl.jobMap, remoteID)
	cl.jobMu.Unlock()
}

// workerResultTTL bounds how long a terminal Result that could not be delivered is
// kept for replay (RECOV-01). The worker cannot read the SERVER's config, so this
// MIRRORS the server default (config.DefaultJobRecoverWindowSec = 120s) rather than
// the server's actual setting, at 2x: once the hub's own window has elapsed it has
// already failed the job, so a later replay would only write into a finished job.
// The 2x margin covers the worker detecting the outage later than the hub does (a
// half-open socket only dies on our read deadline).
const workerResultTTL = 2 * config.DefaultJobRecoverWindowSec * time.Second

// inflightJob is the worker's recovery record for ONE hub job (RECOV-01): the local
// counterpart of the hub's `recovering` set. It answers the three questions the hub
// asks about a job on every reconnect — is it still here, how much of its log has
// already reached the hub, and is there a terminal Result that never made it — so a
// job survives a connection blip untouched: the job itself lives in the
// process-scoped job.Service and does not care about the socket.
//
// Every field is guarded by Client.inflMu.
type inflightJob struct {
	// localID is the worker's LOCAL job id: it locates the job's log directory and
	// is the id the job service is asked about. Empty until Submit returns (and for a
	// dispatch that never produced a local job at all).
	localID string
	// stdoutOff / stderrOff are the offsets of the local log files this worker has
	// SUCCESSFULLY written to the wire. A failed writeFrame leaves them exactly where
	// they were (the unsent bytes are retried verbatim on the next tick), and a
	// resume ack moves them BACKWARDS to the hub's durable byte counts.
	stdoutOff int64
	stderrOff int64
	// seq is the highest log-frame seq this worker has successfully sent.
	seq int64
	// status is the worker-side local status last observed for this job. It is what
	// the register frame's `inflight` snapshot reports, and the hub decides on it
	// whether to resume the job (non-terminal) or to wait for a replayed Result
	// (terminal).
	status string
	// result, when non-nil, is a terminal Result the worker could not deliver because
	// the connection was down when the job finished. resultAt bounds how long it is
	// kept for replay (workerResultTTL).
	result   *wsproto.Result
	resultAt time.Time
}

// wireStatus is the status reported for this job in the register frame: the local
// job's last observed status, or — once a Result is cached — that Result's terminal
// status, which is what the hub must see to WAIT for the replayed Result instead of
// resuming a job that is already over.
func (f *inflightJob) wireStatus() string {
	if f.result != nil {
		return f.result.Status
	}
	if f.status != "" {
		return f.status
	}
	return job.StatusRunning
}

// inflightCreate records a dispatch in the recovery table. It runs at the very start
// of handleDispatch — before any path that can send a Result — so the entry can hold
// a Result whose write fails, and so the register frame never reports a job as gone
// while its dispatch goroutine is still unwinding.
func (cl *Client) inflightCreate(remoteID string) {
	cl.inflMu.Lock()
	if _, dup := cl.inflight[remoteID]; !dup {
		cl.inflight[remoteID] = &inflightJob{status: job.StatusRunning}
	}
	cl.inflMu.Unlock()
}

// inflightSetLocal binds the local job id once the dispatch has submitted its job.
func (cl *Client) inflightSetLocal(remoteID, localID string) {
	cl.inflMu.Lock()
	if f := cl.inflight[remoteID]; f != nil {
		f.localID = localID
	}
	cl.inflMu.Unlock()
}

// inflightSetStatus records the local job's status. The log tailer observes it on
// every poll, so the recovery table follows the local job WITHOUT inflMu ever being
// held across a call into the job service.
func (cl *Client) inflightSetStatus(remoteID, status string) {
	cl.inflMu.Lock()
	if f := cl.inflight[remoteID]; f != nil {
		f.status = status
	}
	cl.inflMu.Unlock()
}

// inflightOffset returns the local log offset the tailer for stream must read from.
// An unknown job has nothing sent yet (0).
func (cl *Client) inflightOffset(remoteID, stream string) int64 {
	cl.inflMu.Lock()
	defer cl.inflMu.Unlock()
	f := cl.inflight[remoteID]
	if f == nil {
		return 0
	}
	if stream == string(store.StreamStderr) {
		return f.stderrOff
	}
	return f.stdoutOff
}

// inflightSeq returns the highest log seq successfully sent for this job.
func (cl *Client) inflightSeq(remoteID string) int64 {
	cl.inflMu.Lock()
	defer cl.inflMu.Unlock()
	if f := cl.inflight[remoteID]; f != nil {
		return f.seq
	}
	return 0
}

// inflightCommit records a log frame that actually reached the wire: the stream's
// offset becomes off and the job's seq becomes seq. It is called ONLY after a
// successful writeFrame — that single rule is what makes a blip lossless, since a
// failed write is re-sent rather than skipped.
func (cl *Client) inflightCommit(remoteID, stream string, off, seq int64) {
	cl.inflMu.Lock()
	if f := cl.inflight[remoteID]; f != nil {
		if stream == string(store.StreamStderr) {
			f.stderrOff = off
		} else {
			f.stdoutOff = off
		}
		f.seq = seq
	}
	cl.inflMu.Unlock()
}

// inflightRewind moves a job's log offsets BACK to the hub's durable byte counts
// (the resume ack). It reports whether the job is tracked at all: an entry this
// process does not have (a stale ack) has nothing to rewind.
func (cl *Client) inflightRewind(remoteID string, stdoutOff, stderrOff int64) bool {
	cl.inflMu.Lock()
	defer cl.inflMu.Unlock()
	f := cl.inflight[remoteID]
	if f == nil {
		return false
	}
	f.stdoutOff, f.stderrOff = stdoutOff, stderrOff
	return true
}

// inflightCacheResult stores a terminal Result that could not be delivered, for
// replay after the next successful register. The entry is created if missing: this
// process owes the hub a Result either way.
func (cl *Client) inflightCacheResult(remoteID string, res wsproto.Result) {
	cl.inflMu.Lock()
	f := cl.inflight[remoteID]
	if f == nil {
		f = &inflightJob{}
		cl.inflight[remoteID] = f
	}
	r := res
	f.result, f.resultAt, f.status = &r, time.Now(), res.Status
	cl.inflMu.Unlock()
}

// inflightResult returns the cached, not-yet-delivered terminal Result. An entry
// whose Result is older than workerResultTTL is dropped instead — the hub gave up on
// that job long ago, so there is nothing left to replay into.
func (cl *Client) inflightResult(remoteID string) (wsproto.Result, bool) {
	cl.inflMu.Lock()
	defer cl.inflMu.Unlock()
	f := cl.inflight[remoteID]
	if f == nil || f.result == nil {
		return wsproto.Result{}, false
	}
	if time.Since(f.resultAt) > workerResultTTL {
		delete(cl.inflight, remoteID)
		return wsproto.Result{}, false
	}
	return *f.result, true
}

// inflightDrop forgets a job: its terminal Result is on the wire (or the dispatch is
// gone), so this process no longer owes the hub anything for it.
func (cl *Client) inflightDrop(remoteID string) {
	cl.inflMu.Lock()
	delete(cl.inflight, remoteID)
	cl.inflMu.Unlock()
}

// inflightIDs lists the job ids this process currently tracks.
func (cl *Client) inflightIDs() []string {
	cl.inflMu.Lock()
	defer cl.inflMu.Unlock()
	ids := make([]string, 0, len(cl.inflight))
	for id := range cl.inflight {
		ids = append(ids, id)
	}
	return ids
}

// inflightSnapshot renders the register frame's `inflight` list (RECOV-01): what
// this process still holds and how much of each log it has successfully sent. It is
// ALWAYS non-nil — nil tells the hub "pre-RECOV-01 worker, it cannot prove
// anything" (the hub then waits out its whole window), while an EMPTY slice tells it
// "a RECOV-01 worker that tracks nothing", so the jobs the hub is holding for us are
// failed at once instead of after a pointless wait. Expired Results are swept here,
// which is also what bounds the cache in a worker that never reconnects.
func (cl *Client) inflightSnapshot() []wsproto.InflightJob {
	now := time.Now()
	cl.inflMu.Lock()
	defer cl.inflMu.Unlock()
	out := make([]wsproto.InflightJob, 0, len(cl.inflight))
	for id, f := range cl.inflight {
		if f.result != nil && now.Sub(f.resultAt) > workerResultTTL {
			delete(cl.inflight, id)
			continue
		}
		out = append(out, wsproto.InflightJob{
			JobID:     id,
			Status:    f.wireStatus(),
			StdoutOff: f.stdoutOff,
			StderrOff: f.stderrOff,
			Seq:       f.seq,
		})
	}
	return out
}

// logRecoveringJobs emits one worker.job_recovering event per job still held when a
// connection dropped (RECOV-01). It mirrors the hub's own worker.job_recovering
// (component=server, emitted when the hub suspends the same jobs) so an operator can
// pair the two: the hub holds the host job in `recovering` while this process keeps
// running it and re-announces it (`inflight`) on the next register.
func (cl *Client) logRecoveringJobs() {
	for _, id := range cl.inflightIDs() {
		slog.Info("worker.job_recovering", "event", "worker.job_recovering", "component", "worker", "worker_id", cl.workerID, "job_id", id)
	}
}

// applyResume applies the hub's resume ack (RECOV-01). It runs on the freshly
// handshaken connection BEFORE that connection is published (see runSession), so no
// log frame can slip out with a stale offset in the window between ack and rewind:
//
//   - every resumed job's log offsets are rewound to the hub's DURABLE byte counts.
//     The hub is the source of truth for what the host actually has, so whatever
//     this worker sent but the hub never persisted is re-sent from there — no gap,
//     no duplicated chunk.
//   - every terminal Result this process could not deliver (a job that finished
//     while the connection was down) is replayed, and its entry dropped once the
//     write lands. The hub is already waiting for it (the job's `inflight` status
//     was terminal) and finishes the host job from it.
func (cl *Client) applyResume(ctx context.Context, conn *websocket.Conn, resumes []wsproto.ResumeJob) {
	for _, r := range resumes {
		if !cl.inflightRewind(r.JobID, r.StdoutOff, r.StderrOff) {
			// The hub is resuming a job this process does not track (a stale ack, or a
			// job whose Result already went out): nothing to rewind, and the hub's own
			// window governs what happens to it.
			continue
		}
		slog.Info("worker.job_resumed", "event", "worker.job_resumed", "component", "worker",
			"worker_id", cl.workerID, "job_id", r.JobID, "stdout_off", r.StdoutOff, "stderr_off", r.StderrOff)
	}
	cl.replayCachedResults(ctx, conn)
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
	// One reload executor per worker process, started before the first connect and
	// living ACROSS reconnects: a reload applies to the local core (and the caps we
	// will register with) whether or not a hub is currently attached. It exits with
	// ctx (worker shutdown).
	go cl.reloadLoop(ctx)
	// Best-effort last-known-good cache retry (POLICY mode with a cache path); a no-op
	// otherwise. Also lives across reconnects and exits with ctx.
	go cl.cacheRetryLoop(ctx)
	// SUP-01 G: the mirrored-event pump. Like the reload executor it lives across
	// reconnects (a job outlives the connection it was dispatched on) and exits with
	// ctx; a frame written while disconnected is dropped, not queued for replay.
	go cl.jobEventLoop(ctx)
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
			slog.Warn("worker.reconnecting", "event", "worker.reconnecting", "component", "worker",
				"worker_id", cl.workerID, "attempt", attempt, "backoff_ms", wait.Milliseconds(), "error", err)
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
	// XFER-01 X2: the job file seam fetches a staged upload from the hub's HTTP
	// origin, which is this session's ws URL with the scheme swapped — recorded per
	// session so a failover to another address can never leave a stale base behind.
	if base, berr := xferHTTPBase(url); berr == nil {
		cl.setHubBase(base)
		defer cl.setHubBase("")
	}
	// going-away (1001) on a clean shutdown; the deferred close also covers the
	// drop/error paths so the fd is always released (no leak, §5.6).
	defer conn.Close(websocket.StatusGoingAway, "worker session end")

	// register → registered (bare ctx; the read deadline governs the steady-state
	// recv loop, not the handshake). The capability fields come from the CURRENT
	// snapshot, so a worker that reloaded its config while disconnected re-registers
	// with what it can do NOW.
	//
	// RECOV-01: the handshake runs on the RAW connection, which is only published as
	// cl.conn further down (setConn) — never here. A dispatch goroutine outlives a
	// blip and keeps polling its job's logs the whole time (see handleDispatch); if
	// cl.conn already pointed at this half-handshaken connection it could push a Log
	// frame AHEAD of the register frame — the hub reads the first frame as the
	// register — or, in the window between the ack and the resume rewind below, push
	// bytes the host already has. Until setConn, those writes hit the previous (dead)
	// connection and fail fast, and the tailer simply retries them afterwards.
	caps := cl.currentCaps()
	if err := cl.writeFrameOn(ctx, conn, wsproto.TypeRegister, "", wsproto.Register{
		WorkerID:        cl.workerID,
		InstanceID:      cl.instanceID,
		ProtocolVersion: wsproto.CurrentProtocolVersion, // the version THIS worker build implements
		PtyCapable:      ptyrunner.Available(),
		OS:              runtime.GOOS,
		Arch:            runtime.GOARCH,
		Hostname:        cl.hostname,
		GoferVersion:    cl.goferVersion,
		StartedAt:       cl.startedAt,
		Labels:          caps.Labels,
		Projects:        caps.Projects,
		Agents:          caps.Agents,
		AgentCaps:       caps.AgentCaps,
		MaxConcurrent:   caps.MaxConc,
		// RECOV-01: what this process still holds, so the hub can pair it against the
		// jobs it is holding in `recovering`. ALWAYS non-nil (an empty list is a
		// statement — "I track nothing" — while nil would mean "old worker, cannot
		// prove anything"); see inflightSnapshot.
		Inflight: cl.inflightSnapshot(),
	}); err != nil {
		return false, fmt.Errorf("send register: %w", err)
	}
	env, err := readEnvelopeOn(ctx, conn)
	if err != nil {
		return false, fmt.Errorf("read registered: %w", err)
	}
	// The first frame MUST be the registered ack. Asserting the type (and not dropping
	// the decode error) stops a stray non-registered frame — a policy push that raced
	// ahead of the ack (B3), a protocol desync — from being mis-decoded As[Registered]
	// into Accepted=false with an empty reason, which used to masquerade as a
	// "registration rejected" and drive a reconnect storm.
	if env.Type != wsproto.TypeRegistered {
		return false, fmt.Errorf("handshake: expected registered frame, got %q", env.Type)
	}
	reg, err := wsproto.As[wsproto.Registered](env)
	if err != nil {
		return false, fmt.Errorf("decode registered frame: %w", err)
	}
	if !reg.Accepted {
		// A binding/token mismatch will not self-heal, but the supervisor still
		// retries (the config may be fixed) — just backed off (§5.2).
		slog.Warn("worker.disconnected", "event", "worker.disconnected", "component", "worker",
			"worker_id", cl.workerID, "reason", "registration_rejected", "error", reg.Reason)
		return false, fmt.Errorf("register rejected: %s", reg.Reason)
	}
	cl.notify("registered")
	sess := daemon.SessionInfo()
	slog.Info("worker.registered", "event", "worker.registered", "component", "worker",
		"worker_id", cl.workerID, "url", url, "labels", caps.Labels, "max_concurrent", caps.MaxConc,
		"session", sess.ID, "interactive", sess.Interactive)

	// RECOV-01: the hub's resume ack is the FIRST thing applied to the new session —
	// still on the raw connection, so no frame can carry a pre-rewind offset — and
	// only then is the connection published and the (still running) log tailers free
	// to push onto it again.
	cl.applyResume(ctx, conn, reg.Resume)
	cl.setConn(conn)

	// Per-session heartbeat: start the ping sender, stop it when the recv loop ends.
	done := make(chan struct{})
	defer close(done)
	cl.startHeartbeat(ctx, done)

	// Open a policy session for this connection (T5-C/D). beginSession bumps the
	// generation, clears the per-session Rev (a new server counts from its own Rev 1)
	// and seeds any ack-bundled Policy as the first pending — the gen it returns tags
	// every TypePolicy frame recvLoop reads on this connection. A non-POLICY worker
	// never applies a pushed Policy; it only acknowledges one so the hub clears pending.
	var gen uint64
	if cl.policyMode {
		if reg.Policy == nil && !wsproto.SupportsPolicy(reg.ProtocolVersion) {
			// The server does not push policy (v3 or older). The worker keeps its
			// last-known-good; a cold start with none simply has no projects until it
			// reaches a v4 server (verification 16).
			slog.Warn("worker in policy mode but server does not push policy; keeping last-known-good",
				"worker_id", cl.workerID, "url", url, "server_proto", reg.ProtocolVersion)
		}
		gen = cl.beginSession(reg.Policy)
	} else if reg.Policy != nil {
		cl.replyLegacyApplied(ctx, reg.Policy.Rev)
	}

	err = cl.recvLoop(ctx, url, gen)
	cl.notify("disconnected")
	// RECOV-01: the connection is gone, the jobs it carried are NOT. Each one keeps
	// running under its own (process-scoped) dispatch ctx and will be re-announced in
	// the next register frame's `inflight` list; the hub mirrors these events with its
	// own worker.job_recovering as it suspends the same jobs.
	cl.logRecoveringJobs()
	reason := "disconnected"
	if err != nil {
		reason = err.Error()
	}
	slog.Info("worker.disconnected", "event", "worker.disconnected", "component", "worker", "worker_id", cl.workerID, "reason", reason)
	return true, err
}

// recvLoop is the single read goroutine for one connection: it reads frames with
// a per-read deadline (half-open hub detection, §5.1) and dispatches each. A
// dispatch is handled in its own goroutine so the worker runs multiple jobs
// concurrently; control frames (cancel/answer/ping) are handled inline. It
// returns the error that ended the connection (disconnect / read-deadline / ctx).
// url is THIS session's hub address; it is threaded to handleDispatch so an
// interactive dispatch derives its pty-connect URL from the same hub it arrived
// on (D-P2-7 per-dispatch URL). gen is the policy session generation opened by
// beginSession for this connection; it tags every TypePolicy frame so a frame from a
// superseded session can never be applied (T5-D).
func (cl *Client) recvLoop(ctx context.Context, url string, gen uint64) error {
	for {
		rctx, cancel := context.WithTimeout(ctx, cl.readDeadline)
		env, err := cl.readEnvelope(rctx)
		cancel()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				slog.Warn("worker.heartbeat_missed", "event", "worker.heartbeat_missed", "component", "worker", "worker_id", cl.workerID, "error", err)
			}
			return err // disconnect / read-deadline / ctx done
		}
		switch env.Type {
		case wsproto.TypeDispatch:
			d, derr := wsproto.As[wsproto.Dispatch](env)
			if derr != nil {
				continue
			}
			cl.dispatchWG.Add(1)
			go func() {
				defer cl.dispatchWG.Done()
				// RECOV-01: ctx here is the PROCESS ctx (recvLoop ← runSession ← Run),
				// deliberately NOT a per-connection one — the dispatch must outlive the
				// connection it arrived on. See handleDispatch's lifetime note.
				cl.handleDispatch(ctx, url, d)
			}()
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
		case wsproto.TypeReload:
			// P1/T3: ONLY enqueue. Running the reload here would block the read loop
			// (no pongs, no cancels) and running it in a fresh goroutine per frame
			// would let two reloads apply out of order — the serial executor
			// (reloadLoop) owns the apply.
			rf, derr := wsproto.As[wsproto.Reload](env)
			if derr != nil {
				continue
			}
			cl.onReload(ctx, rf)
		case wsproto.TypePolicy:
			// P3 policy push (mid-session or ack catch-up). A POLICY worker offers it to
			// its session state (latest-wins, gen-tagged); the serial executor applies it
			// and reports Applied. A non-POLICY worker never applies a pushed Policy — it
			// only acknowledges the Rev so the hub clears pending (verification 1).
			pf, derr := wsproto.As[wsproto.Policy](env)
			if derr != nil {
				continue
			}
			if cl.policyMode {
				cl.offerPolicy(gen, pf)
			} else {
				cl.replyLegacyApplied(ctx, pf.Rev)
			}
		case wsproto.TypeTunnelOpen:
			t, derr := wsproto.As[wsproto.TunnelOpen](env)
			if derr == nil {
				go cl.handleTunnelOpen(ctx, url, t)
			}
		case wsproto.TypeFileXfer:
			// XFER-01: a transfer instruction for THIS worker. Handled in its own
			// goroutine for the same reason a dispatch is — a 256MB transfer would
			// otherwise stall every pong, cancel and dispatch on this connection. It is
			// bounded by its own deadline inside handleFileXfer, and it always answers
			// with a file_xfer_result frame (that frame, not the goroutine, is the
			// completion signal the hub waits on).
			fxf, derr := wsproto.As[wsproto.FileXfer](env)
			if derr == nil {
				go cl.handleFileXfer(ctx, url, fxf)
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

// WaitIdle waits until all currently-running dispatch goroutines have returned,
// or ctx is cancelled. It is primarily a test/shutdown synchronization helper;
// the reconnect loop intentionally does not wait on dispatches between sessions.
func (cl *Client) WaitIdle(ctx context.Context) bool {
	done := make(chan struct{})
	go func() {
		cl.dispatchWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
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
//
// RECOV-01 (see docs/design/2026-09-16-job-recovery-and-worktree-design.md): it
// intentionally writes to the CURRENT cl.conn. That is the point, not a
// limitation: a job outlives the connection it was dispatched on, so its Log /
// Outcome / Result frames must follow the current one — and because a session's
// connection is published (setConn) only after its handshake is complete, a write
// either goes to a fully registered connection or fails fast.
func (cl *Client) writeFrame(ctx context.Context, t wsproto.FrameType, jobID string, payload any) error {
	cl.writeMu.Lock()
	defer cl.writeMu.Unlock()
	if cl.conn == nil {
		// The reload executor outlives any single connection (a SIGHUP can land
		// before the first connect / while reconnecting): applying the config still
		// works, only the re-report has nowhere to go.
		return errors.New("worker: not connected to a hub")
	}
	return wsjson.Write(ctx, cl.conn, wsproto.Envelope{Type: t, JobID: jobID, Payload: mustRaw(payload)})
}

// writeFrameOn is writeFrame bound to an EXPLICIT connection, for the frames that
// must reach the wire BEFORE that connection is published: the register frame, and
// the Results replayed from the resume ack (RECOV-01). Both belong to the handshake,
// which by design happens while cl.conn still points at the previous connection.
func (cl *Client) writeFrameOn(ctx context.Context, conn *websocket.Conn, t wsproto.FrameType, jobID string, payload any) error {
	cl.writeMu.Lock()
	defer cl.writeMu.Unlock()
	return wsjson.Write(ctx, conn, wsproto.Envelope{Type: t, JobID: jobID, Payload: mustRaw(payload)})
}

// setConn publishes the current session's connection under writeMu — the same lock
// writeFrame reads it under, so the reload executor (which writes frames across
// reconnects) never races the reconnect loop's swap.
func (cl *Client) setConn(conn *websocket.Conn) {
	cl.writeMu.Lock()
	cl.conn = conn
	cl.writeMu.Unlock()
}

func (cl *Client) readEnvelope(ctx context.Context) (wsproto.Envelope, error) {
	return readEnvelopeOn(ctx, cl.conn)
}

// readEnvelopeOn reads one frame from an explicit connection: the handshake reads
// its ack off the connection it registered on, which is not yet cl.conn (see
// runSession).
func readEnvelopeOn(ctx context.Context, conn *websocket.Conn) (wsproto.Envelope, error) {
	var env wsproto.Envelope
	if err := wsjson.Read(ctx, conn, &env); err != nil {
		return wsproto.Envelope{}, err
	}
	return env, nil
}
