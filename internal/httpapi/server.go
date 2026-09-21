// Package httpapi exposes the gofer control plane over HTTP using
// github.com/gookit/rux/v2. Handlers only parse params, enforce the auth
// context and encode responses; all business logic lives in the job service and
// the project/agent registries (plan §7).
//
// Error responses use a small uniform shape (NOT the company {status,code,
// message} envelope), because this is a local developer tool, not an internal
// business service (plan §7):
//
//	{"error":"unknown project","detail":"project_key other not found"}
package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gookit/rux/v2"
	"golang.org/x/time/rate"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/buildinfo"
	"github.com/inhere/gofer/internal/castrec"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/metrics"
	"github.com/inhere/gofer/internal/presence"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/ptyrelay"
	"github.com/inhere/gofer/internal/sessionrelay"
	"github.com/inhere/gofer/internal/tunnel"
	"github.com/inhere/gofer/internal/webui"
	"github.com/inhere/gofer/internal/xfer"
)

// ctxCallerID is the rux context key under which authMiddleware stores the
// authenticated caller id for handlers to read (callerFromCtx). It is empty for
// the allow_empty_token pass-through path (no token configured).
const ctxCallerID = "caller_id"
const ctxCallerKind = "caller_kind"

const (
	callerKindUser   = "user"
	callerKindWorker = "worker"
)

// callerEntry pairs a known bearer token with the caller id stamped onto jobs
// that authenticate with it (C2). The token is held in memory only.
type callerEntry struct {
	id    string
	token string
	kind  string
}

// callerFromCtx returns the authenticated caller id stored by authMiddleware,
// or "" when none was set (empty-token pass-through, or a non-string value).
func callerFromCtx(c *rux.Context) string {
	if v, ok := c.Get(ctxCallerID); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func callerKindFromCtx(c *rux.Context) string {
	if v, ok := c.Get(ctxCallerKind); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return callerKindUser
}

func (s *Server) callerMayAdmin(caller string) bool {
	if s.cfg == nil || !s.cfg.Governance.RequireAdminCapability {
		return true
	}
	return s.cfg.CallerCanAdmin(caller)
}

// PtySessionStore is the narrow persistence seam the WEB-03 P3 pty handlers use to
// record/read pty relay session metadata (design review 高1). It is defined here —
// rather than the Server holding a raw *jobstore.Store — so the entry layer keeps a
// minimal, intention-revealing surface; *jobstore.Store satisfies it. A nil
// PtySessionStore means "no persistence" (mcp / most tests) and handlers (T5/T6)
// must guard against it.
type PtySessionStore interface {
	UpsertPtySession(rec jobstore.PtySessionRecord) error
	GetPtySessionByJob(jobID string) (jobstore.PtySessionRecord, bool, error)
	ListPtySessionsByJob(jobID string) ([]jobstore.PtySessionRecord, error)
	ListRecentPtySessions(limit int) ([]jobstore.PtySessionRecord, error)
}

// workerHub is the consumer-side narrow interface (D2/G022) for the ws-worker hub:
// the only two things this entry layer asks of it are "take over this upgrade" and
// "is that worker live". Naming *wshub.Hub here instead would drag the whole hub
// package into httpapi's import graph — the boundary every worker-facing type in
// this package (AgentBrief, WorkerStatus, WorkerCaps) exists to keep. *wshub.Hub
// satisfies it; callers with no hub pass an untyped nil.
type workerHub interface {
	// Accept authenticates nothing (the caller already did) and upgrades the request
	// to the worker's WS connection.
	Accept(w http.ResponseWriter, req *http.Request, callerID string)
	// LiveInstance reports the current connection instance of a worker, ok=false
	// when it is offline.
	LiveInstance(workerID string) (string, bool)
	// WorkerProtocol reports the wire protocol version of a worker's LIVE
	// connection, ok=false when it is offline. The transfer paths use it to refuse
	// a runner that cannot speak the file-transfer frames BEFORE a payload is
	// streamed to it (bd h-aii-gnm3).
	WorkerProtocol(workerID string) (int, bool)
	OpenTunnel(workerID, tunnelID, network, target, relayNonce string) error
}

// Server holds the wired dependencies and the rux router. It is constructed once
// (New) and either started with Run or exposed as an http.Handler (Handler) for
// httptest.
type Server struct {
	cfg       *config.ServerConfig
	jobs      *job.Service
	workflow  *workflow.Engine
	projects  *project.Registry
	agents    *agent.Registry
	router    *rux.Router
	build     buildinfo.Info
	startedAt time.Time

	// token is the effective bearer token (already resolved from config/env/flag
	// by the caller). When empty, auth is only permitted if allowEmptyToken is
	// true; otherwise every /v1 request is rejected (defence in depth — the serve
	// command also refuses to start, see internal/commands/serve.go).
	token           string
	allowEmptyToken bool
	// callers is the resolved multi-caller auth set (C2): the legacy token (as
	// caller id "default") plus every config.Callers entry with a non-empty token.
	// authMiddleware constant-time compares the presented bearer against each.
	callers []callerEntry

	// webEnabled mounts the embedded web console (static SPA) as the NotFound
	// fallback for GET requests. Resolved from serverCfg.IsWebEnabled() in New.
	webEnabled bool
	webDir     string

	// hub is the ws-worker hub; when non-nil the /v1/workers/connect WS route is
	// mounted (ws-worker). It is nil for callers that do not run the hub. Its type
	// is the narrow workerHub interface (not *wshub.Hub) so this entry layer keeps
	// no import of the hub package (D2/G022); serve passes the singleton.
	hub workerHub
	// reloader is the D2 seam for POST /v1/workers/{id}/reload (nil = not wired =>
	// 503). serve injects an adapter over the same hub.
	reloader workerReloader
	// relayNonces/ptyRelays are live-only WEB-03 PTY relay state. T4 only wires
	// them; T5/T7 mount handlers that consume the same instances.
	relayNonces *ptyrelay.NonceStore
	ptyRelays   *ptyrelay.Registry
	// attachTickets are short-lived one-time browser attach tickets. T6 issues
	// them via authenticated HTTP; T7 consumes them during the WS attach upgrade.
	attachTickets *AttachTicketStore

	// castRecorder is the WEB-03 P3 cast recording factory (nil = recording off,
	// the default). serve resolves it from storage.cast at startup and injects it
	// via SetCastRecorder; mcp/tests build httpapi.New directly and leave it nil so
	// recording is off with zero behaviour change (G023). The pty-connect handler
	// (T5) mints a per-session sink from it; the recording download gate (T6)
	// stream-decrypts encrypted casts through it.
	castRecorder *castrec.Recorder
	// ptyTranscriptMax is the pty text transcript tail cap (PTY-01 §四);
	// 0 = ptyrelay.DefaultTranscriptMaxBytes. Set by serve from pty.transcript_max_bytes.
	ptyTranscriptMax int

	// scheduleTriggerMu guards scheduleTriggerAt: the per-schedule last-webhook-trigger
	// times of the AUTO-02b rate limit. In-memory and per-process on purpose (see
	// allowScheduleTrigger) — a restart only ever costs one extra run.
	scheduleTriggerMu sync.Mutex
	scheduleTriggerAt map[string]time.Time

	// ptySessions persists pty relay session metadata (WEB-03 P3). It is the narrow
	// store seam the pty handlers write/read (D-P3, review 高1: the Server holds no
	// raw jobstore handle). nil-safe: nil means no persistence (mcp/tests) and the
	// handlers guard it.
	ptySessions PtySessionStore

	// runners is the configured runner set the C6/P4 GET /v1/runners endpoint
	// enumerates (name → type / base_url / worker_id). It is the top-level
	// config.Runners map (the Server otherwise only holds ServerConfig). nil/empty
	// => the endpoint reports only the implicit `local` runner.
	runners map[string]config.RunnerConfig
	// prober and workers feed the C6/P4 GET /v1/runners observability endpoint.
	// Both are nil-safe narrow interfaces (D2): a nil prober renders every
	// peer-http row `unknown`; a nil workers renders every worker row `unknown`.
	// serve injects the concrete adapters (peer-http probe cache + the hub); other
	// callers (most tests, mcp) pass nil.
	prober  runnerProber
	workers workerRegistry
	tunnels *tunnel.Registry

	// metrics is the E16 Prometheus instrumentation (nil = no /metrics endpoint,
	// no HTTP middleware). It is injected post-construction by SetMetrics (serve)
	// rather than via New so the existing positional constructor and its many call
	// sites stay untouched. metricsEnabled/metricsToken mirror serverCfg.Metrics
	// and gate the endpoint's mounting + optional Bearer check.
	metrics        *metrics.Metrics
	metricsEnabled bool
	metricsToken   string

	// xfer is the XFER-01 transfer manager behind /v1/xfer*. Injected
	// post-construction by SetXfer (serve), like SetPresence. The routes are
	// ALWAYS mounted (they answer 503 while it is nil), so this seam needs no
	// router rebuild; mcp/tests simply leave it nil.
	xfer *xfer.Manager

	// presence is the E36 driver-agent identity/mailbox service backing the
	// /v1/agents/* + /v1/messages endpoints. Injected post-construction by
	// SetPresence (serve), mirroring SetMetrics so the wide positional New stays
	// untouched (design D3). nil => the presence routes are not mounted (most
	// tests / mcp-less callers).
	presence *presence.Service
	// relay is the session-relay service (SESS-01) behind /v1/sessions/*. It only
	// needs the shared job store, so it is built in New and always mounted.
	relay *sessionrelay.Service

	// limiters holds one token-bucket per caller for the E17 submit-rate limit
	// (design §7.3). Guarded by its OWN limMu (NOT s.mu, which lives in the job
	// service): the rate-limit path must not contend with job bookkeeping. The
	// limiter set is built lazily by limiterFor; its Limit/Burst are re-synced on
	// every request from the job service's CURRENT config (hot-reload真源), so this
	// map is pure per-caller state, never a copy of the throttle config itself.
	limiters map[string]*rate.Limiter
	limMu    sync.Mutex
}

// SetMetrics injects the E16 Prometheus instrumentation and mounts the /metrics
// endpoint + the /v1 HTTP middleware (design §6.2). It MUST be called before the
// server starts serving (serve calls it right after New, before Run). enabled
// and token come from serverCfg.Metrics. Passing a nil m leaves metrics off.
//
// The router is rebuilt so the /metrics route + the metricsMiddleware on /v1 are
// registered (rux requires routes/middleware at build time). This is safe because
// SetMetrics runs single-threaded at assemble time, before any request is served.
func (s *Server) SetMetrics(m *metrics.Metrics, enabled bool, token string) {
	s.metrics = m
	s.metricsEnabled = enabled
	s.metricsToken = token
	s.router = s.buildRouter()
}

// SetPresence injects the E36 presence/mailbox service and rebuilds the router so
// the /v1/agents/* + /v1/messages routes are mounted (they are registered only
// when s.presence is non-nil). Mirrors SetMetrics: called post-construction by
// serve (after SetMetrics) so the wide positional New and its many call sites stay
// untouched. Must run single-threaded at assemble time, before serving.
func (s *Server) SetPresence(p *presence.Service) {
	s.presence = p
	s.router = s.buildRouter()
}

// SetPtyRelay injects the shared live-only PTY relay state for worker pty-connect
// and browser attach routes. It is a post-construction setter to avoid widening
// New's already-large positional constructor.
func (s *Server) SetPtyRelay(nonces *ptyrelay.NonceStore, relays *ptyrelay.Registry) {
	s.relayNonces = nonces
	s.ptyRelays = relays
	s.router = s.buildRouter()
}

// SetCastRecorder injects the WEB-03 P3 cast recording factory (nil = recording
// off, the zero-regression default). Like SetPtyRelay it is a post-construction
// setter so the wide positional New stays untouched; serve calls it at startup
// after resolving storage.cast and its encryption key. It mounts no routes, so —
// unlike SetMetrics/SetPresence/SetPtyRelay — it does NOT rebuild the router.
func (s *Server) SetCastRecorder(rec *castrec.Recorder) { s.castRecorder = rec }

// SetPtyTranscriptMaxBytes sets the pty text transcript's tail cap (PTY-01 §四,
// config key pty.transcript_max_bytes). serve injects ptyrelay.DefaultTranscript…
// resolved value at startup; hub-less callers (mcp/tests) leave it unset and get
// the default. <= 0 restores the default.
func (s *Server) SetPtyTranscriptMaxBytes(n int64) {
	if n > 0 {
		s.ptyTranscriptMax = int(n)
	}
}

func (s *Server) ptyTranscriptMaxBytes() int {
	if s == nil || s.ptyTranscriptMax <= 0 {
		return ptyrelay.DefaultTranscriptMaxBytes
	}
	return s.ptyTranscriptMax
}

// SetPtySessionStore injects the pty-session persistence seam (WEB-03 P3). serve
// passes the *jobstore.Store (which satisfies PtySessionStore); a nil store leaves
// pty session persistence off (mcp/tests). It mounts no routes → no router rebuild.
func (s *Server) SetPtySessionStore(store PtySessionStore) { s.ptySessions = store }

// SetXfer injects the XFER-01 transfer manager (serve passes core's). Unlike
// SetPresence it needs no router rebuild: the /v1/xfer routes are always mounted
// and answer 503 while no manager is wired, so a server without transfers (mcp,
// most tests) keeps the identical router.
func (s *Server) SetXfer(m *xfer.Manager) { s.xfer = m }

// SetSessionRelayPolicy injects the effective session-relay auto-arm policy
// (SESS-01 R2 + SUP-01 D): the keyboard idle threshold and the last-human-input
// fallback threshold, the supervision gate and its look-back window, all in
// seconds (see config.SessionConfig; 0 disables that criterion). serve calls it
// with the whole config's effective values — the `session:` block — while a Server
// built from a bare ServerConfig keeps New's shipped default. No-op without a relay
// service (a server with no job store).
func (s *Server) SetSessionRelayPolicy(idleSec, turnSec int, skipWhenSupervising bool, supervisingWindowSec int) {
	if s.relay == nil {
		return
	}
	s.relay.AutoArmIdleSec = idleSec
	s.relay.AutoArmTurnSec = turnSec
	s.relay.SkipWhenSupervising = skipWhenSupervising
	s.relay.SupervisingWindowSec = supervisingWindowSec
}

// SetSessionInjectCommands injects the tmux-injection whitelist (SESS-01 §9.1 A,
// session.inject_commands): the CLI commands a reply may be typed into. Empty
// keeps the relay's built-in list. Like SetSessionRelayPolicy it is a no-op
// without a relay service.
func (s *Server) SetSessionInjectCommands(commands []string) {
	if s.relay == nil {
		return
	}
	s.relay.SetInjectCommands(commands)
}

// New builds a Server: it resolves the effective token, wires the rux router
// (routes + auth middleware) and returns it ready to Run or hand to httptest.
//
// token is the already-resolved effective token (serve resolves config.token /
// token_env / --token before calling New). allowEmptyToken mirrors the
// config/flag of the same name.
//
// runners is the configured runner set for the C6/P4 GET /v1/runners endpoint
// (nil/empty => only the implicit local row). prober and workers are the C6/P4
// observability sources (both nil-safe: a nil renders the corresponding runner
// rows `unknown`). serve injects them (config.Runners + the peer-http probe cache
// + a hub adapter); other callers pass nil.
//
// NOTE (D3, deferred): New is now a wide positional constructor. The plan flags a
// future functional-option / Deps-struct refactor as optional cleanup; it is
// intentionally NOT done here to keep the change backward-compatible and focused.
func New(serverCfg *config.ServerConfig, token string, allowEmptyToken bool, jobs *job.Service, wf *workflow.Engine, projects *project.Registry, agents *agent.Registry, hub workerHub, runners map[string]config.RunnerConfig, prober runnerProber, workers workerRegistry) *Server {
	s := &Server{
		cfg:             serverCfg,
		jobs:            jobs,
		workflow:        wf,
		projects:        projects,
		agents:          agents,
		token:           token,
		allowEmptyToken: allowEmptyToken,
		callers:         buildCallers(serverCfg, token),
		webEnabled:      serverCfg.IsWebEnabled(),
		webDir:          serverCfg.WebDir,
		hub:             hub,
		runners:         runners,
		prober:          prober,
		workers:         workers,
		tunnels:         tunnel.NewRegistry(),
		limiters:        map[string]*rate.Limiter{},
		attachTickets:   NewAttachTicketStore(),
		startedAt:       time.UnixMilli(nowMillis()),
	}
	if jobs != nil && jobs.Meta() != nil {
		s.relay = sessionrelay.NewService(jobs.Meta())
		// Session-relay auto-arm thresholds. A Server built from a bare ServerConfig
		// has no `session:` block in hand, so it takes the shipped idle default;
		// serve, which holds the whole config, overrides both thresholds through
		// SetSessionRelayPolicy (R2).
		s.relay.AutoArmIdleSec = config.DefaultSessionAutoRelayIdleSec
		// The supervision gate (SUP-01 D) has no ServerConfig-visible key — the
		// `session:` block lives on the whole config — so a bare-ServerConfig
		// server takes the documented defaults (skip ON, 2h window) and serve
		// overrides both through SetSessionRelayPolicy.
		s.relay.SkipWhenSupervising = true
		s.relay.SupervisingWindowSec = config.DefaultSessionSupervisingWindowSec
		// job.Service is the outbound notifier (webhook queue + IM adapters).
		s.relay.SetNotifier(jobs)
		// job.Service also runs path A's internal injection jobs (§9.1 A) and path
		// B's interactive takeover jobs (§9.1 B): one adapter, because both are
		// "submit an internal job on the session's own runner".
		s.relay.SetInjector(sessionInjector{jobs: jobs, projects: projects, agents: agents})
		s.relay.SetTakeoverer(sessionInjector{jobs: jobs, projects: projects, agents: agents})
		// SUP-02 R1: a terminal path-B takeover job hands its session back. The relay
		// service and the job service are siblings, so the ASSEMBLY wires the two: the
		// job's terminal hook is filtered by the takeover tag (the cheap, positive
		// signal that this job could hold a session) and the release itself is a no-op
		// for every other job. Best-effort by design — a failed release is a session a
		// human can still free with `session release-takeover`, never a job failure.
		jobs.OnTerminal(func(r job.JobResult) {
			if !hasTag(r.Tags, sessionrelay.TagRelayTakeover) {
				return
			}
			if _, err := s.relay.ReleaseTakeoverForJob(context.Background(), r.ID); err != nil {
				slog.Warn("release session takeover on job end", "job_id", r.ID, "err", err)
			}
		})
	}
	s.router = s.buildRouter()
	return s
}

// hasTag reports whether a job carries a tag — the terminal hook's filter below.
// Tags are a small free-form list, so a linear scan IS the whole implementation.
func hasTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}

// SetBuildInfo injects linker build metadata for runtime status endpoints.
func (s *Server) SetBuildInfo(info buildinfo.Info) {
	s.build = info
}

// buildCallers resolves the multi-caller auth set once at startup: each
// config.Callers entry (token literal or token_env, empty tokens skipped) plus
// the legacy effective token as caller id "default" (only when non-empty). The
// legacy token is appended last so an explicit caller entry that happens to
// share the same token wins the id (first match in the constant-time scan).
func buildCallers(serverCfg *config.ServerConfig, token string) []callerEntry {
	var out []callerEntry
	if serverCfg != nil {
		for _, cc := range serverCfg.Callers {
			tok := cc.Token
			if tok == "" && cc.TokenEnv != "" {
				tok = os.Getenv(cc.TokenEnv)
			}
			if tok == "" {
				continue // a caller with no resolvable token cannot authenticate
			}
			out = append(out, callerEntry{id: cc.ID, token: tok, kind: callerKindUser})
		}
		// ws-worker (review #1): each registered worker authenticates with its own
		// token; its caller id IS its worker_id, so lookupCaller returns worker_id
		// and hub.Accept can bind it directly. Appended before the legacy token so
		// a worker-specific token wins the id in the constant-time scan.
		for workerID, wc := range serverCfg.Workers {
			tok := wc.Token
			if tok == "" && wc.TokenEnv != "" {
				tok = os.Getenv(wc.TokenEnv)
			}
			if tok == "" {
				continue
			}
			out = append(out, callerEntry{id: workerID, token: tok, kind: callerKindWorker})
		}
	}
	if token != "" {
		out = append(out, callerEntry{id: "default", token: token, kind: callerKindUser})
	}
	return out
}

// buildRouter registers the routes. /health is unauthenticated; everything under
// /v1 is guarded by the bearer-token middleware applied at group level (rux
// requires Use() before any route registration, so per-group middleware is the
// clean way to keep /health open — plan §7).
func (s *Server) buildRouter() *rux.Router {
	r := rux.New()
	r.Use(s.httpErrorMiddleware)

	r.GET("/health", s.handleHealth)

	// E16: Prometheus scrape endpoint. Sibling of /health, registered OUTSIDE the
	// /v1 authMiddleware group: scrapers rarely carry a Bearer token and the
	// intranet admission boundary guards it (SR202). An optional metrics.token
	// re-adds a Bearer check (handleMetrics). Mounted only when metrics is wired
	// AND enabled (serverCfg.metrics.enabled, default true).
	if s.metrics != nil && s.metricsEnabled {
		r.GET("/metrics", s.handleMetrics)
	}

	// ws-worker WS endpoint. Registered OUTSIDE the /v1 authMiddleware group: a WS
	// upgrade cannot use the JSON error envelope / web fallback — a rejected
	// handshake must be a bare 401 (not a {error,detail} body). It does its own
	// Bearer auth (reusing lookupCaller) then hands off to hub.Accept. Mounted only
	// when the hub is wired (serve); nil for hub-less callers (some tests).
	if s.hub != nil {
		r.GET("/v1/workers/connect", s.handleWorkerConnect)
		r.GET(tunnel.ConnectPath, s.handleTunnelConnect)
		r.GET(tunnel.WorkerConnectPath, s.handleWorkerTunnelConnect)
	}
	if s.hub != nil && s.relayNonces != nil && s.ptyRelays != nil {
		r.GET("/v1/workers/pty-connect", s.handlePtyConnect)
	}
	r.GET("/v1/jobs/{id}/attach", s.handleJobAttach)
	// AUTO-02b schedule webhook: an EXTERNAL caller (some other system's automation)
	// has no gofer bearer, so the schedule's own trigger_token is the credential and
	// this route is registered OUTSIDE the /v1 auth group — exactly like the WS and
	// attach paths above. The handler does its own constant-time token check.
	r.POST("/v1/schedules/{id}/trigger", s.handleScheduleTrigger)

	r.Group("/v1", func() {
		r.GET("/config", s.handleGetConfig)
		r.GET("/projects", s.handleListProjects)
		r.POST("/projects", s.handleCreateProject)
		r.GET("/projects/{key}", s.handleGetProject)
		r.PUT("/projects/{key}", s.handleUpdateProject)
		r.DELETE("/projects/{key}", s.handleDeleteProject)

		// Web 控制台 v2 只读层(design §7): 项目 git 状态(E20) / 子 git 发现(E32) /
		// 白名单关键文件读取(E32)。全只读、参数固定、SafeJoin+白名单+大小/二进制限制。
		r.GET("/projects/{key}/git", s.handleGetProjectGit)
		r.GET("/projects/{key}/repos", s.handleListRepos)
		r.GET("/projects/{key}/file", s.handleGetProjectFile)
		// SUP-01 P5: 任务书模板（只读）——项目 .gofer/templates 优先，随后全局
		// <config-dir>/templates。提交面不需要新端点：template/vars 就在 JobRequest 里。
		r.GET("/projects/{key}/templates", s.handleListProjectTemplates)
		r.GET("/projects/{key}/templates/{name}", s.handleGetProjectTemplate)

		r.GET("/agents", s.handleListAgents)
		// SUP-01 P3: 探针——用普通 job 提交路径验活一个 agent（`gofer agent probe`、
		// web Agents 页的按钮都走这里）。段名用 {id}（而非 {key}）：rux 的 radix 树要求
		// 同一位置只有一个参数名，presence 路由的 /agents/{id}/... 已经占了它。
		r.POST("/agents/{id}/probe", s.handleProbeAgent)

		// C6/P4: remote-node observability — status of every configured runner
		// (local / peer-http probe / worker heartbeat). Normal authed JSON endpoint
		// (NOT the bare-401 WS path), list-style shape mirroring /v1/jobs.
		r.GET("/runners", s.handleListRunners)
		r.GET("/tunnels", s.handleListTunnels)

		// XFER-01 file transfer (design §一.2). The payload always rides HTTP; the
		// WS carries only the instruction. `POST /v1/xfer` is the user surface
		// (multipart push / JSON pull), `GET|PUT .../content` is shared with the
		// executing worker (whose token may only touch transfers assigned to it).
		// Always mounted; 503 until serve injects the manager.
		r.POST("/xfer", s.handleXferCreate)
		// The precheck is a POST on a distinct path, so it never collides with the
		// {id} routes below (bd h-aii-gnm3).
		r.POST("/xfer/precheck", s.handleXferPrecheck)
		r.GET("/xfer", s.handleXferList)
		r.GET("/xfer/{id}", s.handleXferStatus)
		r.DELETE("/xfer/{id}", s.handleXferDelete)
		r.GET("/xfer/{id}/content", s.handleXferContentGet)
		r.PUT("/xfer/{id}/content", s.handleXferContentPut)

		// Worker config reload (authed JSON, unlike the bare-401 WS routes above):
		// ask one worker to re-read its own config and WAIT for its receipt, so a
		// config the worker refuses fails this request instead of vanishing into a
		// log. Always registered; answers 503 while no hub is wired.
		r.POST("/workers/{id}/reload", s.handleWorkerReload)

		// G4 (design §6.4): read-only form-options aggregate for the web console
		// submit form (projects/agents/runners/workers in one authed GET).
		r.GET("/meta", s.handleMeta)
		r.GET("/stats", s.handleStats)

		r.POST("/jobs", s.handleCreateJob)
		r.GET("/jobs", s.handleListJobs)
		r.GET("/jobs/{id}", s.handleGetJob)
		// E2 (P2-b): original JobRequest for re-submit/audit (request_json column).
		// Separate from get_job so the list/get responses stay lean (D1).
		r.GET("/jobs/{id}/request", s.handleGetJobRequest)
		r.GET("/jobs/{id}/logs/stdout", s.handleJobLogsStdout)
		r.GET("/jobs/{id}/logs/stderr", s.handleJobLogsStderr)
		r.GET("/jobs/{id}/stream", s.handleJobStream)

		// E13: append-only lifecycle event stream (?since=<seq> for incremental).
		r.GET("/jobs/{id}/events", s.handleListEvents)

		// E14: read-only webhook delivery status for a job (delivered/retry/failed).
		r.GET("/jobs/{id}/deliveries", s.handleListDeliveries)

		// E1 产物回取(P2)：清单 + 下载。下载 {name:.+} 是 catch-all（rux 把
		// {name:.+}/{name:.*} 转成 *name 通配，匹配含 '/' 的子路径，如 sub/b.bin），
		// name 经 safeJoinUnder 做路径安全校验（拒 ../绝对/软链逃逸）。
		r.GET("/jobs/{id}/artifacts", s.handleListArtifacts)
		r.GET("/jobs/{id}/artifacts/{name:.+}", s.handleDownloadArtifact)

		// E12 diff 快照(P3)：默认回 --stat 摘要(库)，?full=1 回 changes.diff 全量。
		r.GET("/jobs/{id}/diff", s.handleGetDiff)

		// WT-01 受管 worktree：GET 实时状态（分支/head/领先提交数/脏/是否已合并），
		// DELETE 移除（有未提交改动且无 ?force=1 → 409；?delete_branch=1 连带删分支）。
		r.GET("/jobs/{id}/worktree", s.handleGetJobWorktree)
		r.DELETE("/jobs/{id}/worktree", s.handleDeleteJobWorktree)

		// WEB-03 P3 (D-P3-7): download a job's recorded pty session (asciinema v2
		// cast). Same owner/admin gate as the browser attach path. The recording
		// gate does NOT do a remote-source 409 (unlike artifact download): a pty
		// cast is always written hub-side, so it is served regardless of where the
		// job ran. Encrypted casts are stream-decrypted.
		r.GET("/pty/sessions", s.handleRecentPtySessions)
		r.GET("/jobs/{id}/pty/recording", s.handlePtyRecording)
		r.GET("/jobs/{id}/pty/sessions", s.handlePtySessions)

		r.POST("/jobs/{id}/cancel", s.handleCancelJob)

		// GATE-01 S3 人工验收：accept/reject 是人对交付物的裁决（agent 永远不能 accept
		// 自己的工作）。仅 user caller（worker 403）；governance.require_answer_capability
		// 开启时需 can_answer。reject {note,resume?} 可带 resume 以 note 为 prompt 续投。
		r.POST("/jobs/{id}/accept", s.handleAcceptJob)
		r.POST("/jobs/{id}/reject", s.handleRejectJob)

		// session-capture(P2)：用源 job 的 SessionID 续接底层 agent 会话，起一个新
		// exec job（同 runner）。body {prompt, runner?}；校验失败 4xx/404(resumeStatus)。
		r.POST("/jobs/{id}/resume", s.handleResumeJob)

		// P5: rebuild a NEW job from a source job's request + edits (env stays server-side).
		// rerun = empty-body rebuild. 校验失败 4xx/404(rebuildStatus)。
		r.POST("/jobs/{id}/rebuild", s.handleRebuildJob)

		// JOB-09 wakeups: register an event subscription / timer on a job that starts a
		// CONTINUATION of it (resume, or a rebuild with the instruction appended) when
		// it fires. POST {kind,at|every_sec|cron|event_types|…} is a user-caller
		// surface (worker 403, like resume's other human-only siblings); the per-wakeup
		// routes manage an already registered one. 校验失败 4xx/404(wakeupStatus)。
		r.POST("/jobs/{id}/wakeups", s.handleCreateWakeup)
		r.GET("/jobs/{id}/wakeups", s.handleListWakeups)
		r.GET("/wakeups/{wid}", s.handleGetWakeup)
		r.PATCH("/wakeups/{wid}", s.handleUpdateWakeup)
		r.DELETE("/wakeups/{wid}", s.handleDeleteWakeup)

		// 工作流(job 链)：提交/列表/详情(含 step 链)/取消。详情附每步 {step_index,
		// name,job_id,status}，列表 ?status= 过滤；提交校验失败复用 submitStatus(404/400)。
		r.POST("/workflows", s.handleCreateWorkflow)
		r.GET("/workflows", s.handleListWorkflows)
		r.GET("/workflows/{id}", s.handleGetWorkflow)
		// P1: workflow 级 append-only 事件流（?since=<seq> 增量）。
		r.GET("/workflows/{id}/events", s.handleListWorkflowEvents)
		// P4(T4.1): 导出 WorkflowSpec(从 spec_json，剥离 secret，可再导入复现)。
		r.GET("/workflows/{id}/export", s.handleExportWorkflow)
		r.POST("/workflows/{id}/cancel", s.handleCancelWorkflow)

		// plan 编排：纯归组容器。jobs.plan_id 支持提交即归组；attach 补挂已有 job。
		r.POST("/plans", s.handleCreatePlan)
		r.GET("/plans", s.handleListPlans)
		r.GET("/plans/{id}", s.handleGetPlan)
		r.PATCH("/plans/{id}", s.handleUpdatePlan)
		r.POST("/plans/{id}/jobs", s.handleAttachPlanJob)
		r.POST("/plans/{id}/todos", s.handleAddPlanTodo)
		// PLAN-03 chain control: run starts the ready work (and releases pause/block),
		// pause holds the automatic advance, resume releases both and advances.
		r.POST("/plans/{id}/run", s.handleRunPlan)
		r.POST("/plans/{id}/pause", s.handlePausePlan)
		r.POST("/plans/{id}/resume", s.handleResumePlan)
		r.PATCH("/todos/{todo_id}", s.handleUpdateTodo)
		// PLAN-02 P2: explicit dispatch of an assigned item (ignores its status).
		r.POST("/todos/{todo_id}/dispatch", s.handleDispatchTodo)

		// 决策通道 (decision channel, Part C §C3): agent raises a blocking question
		// (MCP gofer_ask_human), a human answers here. D1: single ask entry with
		// optional plan_id in the body; list/get serve the bell + MCP polling.
		// 会话中继 (session relay, SESS-01): terminal agent-CLI sessions register via
		// their hooks; the Stop hook posts a turn and long-polls for the human reply
		// the web gives, then injects it back into the same terminal session.
		r.POST("/sessions", s.handleRegisterSession)
		r.GET("/sessions", s.handleListSessions)
		r.GET("/sessions/{sid}", s.handleGetSession)
		r.DELETE("/sessions/{sid}", s.handleDeleteSession)
		r.POST("/sessions/{sid}/heartbeat", s.handleSessionHeartbeat)
		r.POST("/sessions/{sid}/relay", s.handleSetSessionRelay)
		r.POST("/sessions/{sid}/turns", s.handleOpenTurn)
		r.GET("/sessions/{sid}/turns/{id}", s.handleWaitTurn)
		r.POST("/sessions/{sid}/turns/{id}/release", s.handleReleaseTurn)
		r.POST("/sessions/{sid}/say", s.handleSessionSay)
		// §9.1 A: deliver to a session that is NOT waiting — tmux send-keys.
		r.POST("/sessions/{sid}/deliver", s.handleSessionDeliver)
		// §9.1 B: give a taken-over session (`--resume` pty job) back to its terminal.
		r.POST("/sessions/{sid}/release-takeover", s.handleSessionReleaseTakeover)

		r.POST("/decisions", s.handleAskDecision)
		r.GET("/decisions", s.handleListDecisions)
		r.GET("/decisions/{id}", s.handleGetDecision)
		r.POST("/decisions/{id}/answer", s.handleAnswerDecision)

		// P9 running-job two-way interactions.
		r.POST("/jobs/{id}/attach-ticket", s.handleAttachTicket)
		r.POST("/jobs/{id}/interactions", s.handleCreateInteraction)
		r.GET("/jobs/{id}/interactions", s.handleListInteractions)
		r.POST("/jobs/{id}/interactions/{interaction_id}/answer", s.handleAnswerInteraction)
		// y5wt: 通用 sup marks a pending interaction needs_human (高危/拿不准 → 留给人).
		r.POST("/jobs/{id}/interactions/{interaction_id}/punt", s.handlePuntInteraction)
		// E25: cross-job pending interactions (supervisor discovery). Always mounted
		// (reads job.Service, no extra wiring); ?status=pending (default).
		r.GET("/interactions", s.handleListPendingInteractions)

		// AUTO-02 cron schedules: store a prepared JobRequest behind a cron expr.
		r.POST("/schedules", s.handleCreateSchedule)
		r.GET("/schedules", s.handleListSchedules)
		r.GET("/schedules/{id}", s.handleGetSchedule)
		r.DELETE("/schedules/{id}", s.handleDeleteSchedule)
		r.POST("/schedules/{id}/enable", s.handleEnableSchedule)
		r.POST("/schedules/{id}/disable", s.handleDisableSchedule)
		r.POST("/schedules/{id}/run-now", s.handleRunSchedule)
		// AUTO-02b: rotate the webhook secret (authenticated; enabling a schedule's
		// webhook for the first time is the same call).
		r.POST("/schedules/{id}/rotate-token", s.handleRotateScheduleToken)

		// E36 driver-agent identity/mailbox (design §10). Mounted only when the
		// presence service is wired (SetPresence, serve); nil for presence-less
		// callers (most tests / peer bridges) so their routers stay unchanged.
		if s.presence != nil {
			r.POST("/agents/register", s.handleRegisterAgent)
			r.GET("/agents/presence", s.handleListPresence)
			r.POST("/agents/{id}/inbox/poll", s.handlePollInbox)
			r.GET("/agents/{id}/inbox", s.handleListInbox) // P5 read-only观察(不消费/不刷心跳)
			r.POST("/messages", s.handlePostMessage)
			r.POST("/agents/{id}/deregister", s.handleDeregister)
		}
		// Middleware chain order (rux runs group middlewares left-to-right, see
		// internal/core/router.go applyGroup + context.Next):
		//   1. metricsMiddleware — runs first / records LAST (it wraps c.Next), so even
		//      a 401-rejected OR 429-rate-limited request is counted in
		//      gofer_http_requests_total (E16). It does not read the caller id.
		//   2. authMiddleware — sets caller_id in the rux ctx (or aborts 401).
		//   3. rateLimitMiddleware (E17) — MUST run AFTER auth because it keys the
		//      token-bucket on the auth-set caller_id (callerFromCtx). Only writes
		//      POST /v1/jobs|/workflows are gated (isSubmitPath); over-rate → 429.
	}, s.metricsMiddleware, s.authMiddleware, s.rateLimitMiddleware)

	// Mount the embedded web console (static SPA shell, no auth) as the NotFound
	// fallback. /health and /v1/* are concrete routes and match first; any other
	// GET falls through to the SPA so client-side routes (e.g. /board) resolve to
	// index.html. Non-GET unmatched requests return 404 (rux routes method
	// mismatches to NotFound too, see plan/T4 notes).
	if s.webEnabled {
		var h http.Handler
		var ok bool
		if s.webDir != "" {
			h, ok = webui.HandlerForDir(s.webDir)
		} else {
			h, ok = webui.Handler()
		}
		_ = ok
		r.NotFound(func(c *rux.Context) {
			if c.Req.Method != http.MethodGet {
				http.NotFound(c.Resp, c.Req)
				return
			}
			h.ServeHTTP(c.Resp, c.Req)
		})
	}

	return r
}

// handleWorkerConnect authenticates the WS upgrade with the same Bearer scheme
// as /v1 but rejects with a BARE 401 (no JSON envelope, no web fallback) since a
// failed handshake is a raw HTTP rejection, then hands the upgrade to hub.Accept
// with the resolved caller id (= the token-bound worker_id). The
// allow_empty_token mode does NOT waive worker auth: a worker with no resolvable
// caller is rejected here, and even a matched empty caller is later rejected by
// hub.Accept's binding check (per-worker token is mandatory, review #1).
func (s *Server) handleWorkerConnect(c *rux.Context) {
	got, ok := bearerToken(c.Req.Header.Get("Authorization"))
	callerID, matched := "", false
	if ok {
		callerID, matched = s.lookupCaller(got)
	}
	if !matched {
		// Worker presented no/unknown bearer token: it never reaches the hub
		// register handshake. Log so a token mismatch is visible (the worker side
		// only sees the WS close).
		slog.Warn("worker auth rejected at hub upgrade", "remote", c.Req.RemoteAddr,
			"reason", "missing or unknown bearer token")
		c.Resp.WriteHeader(http.StatusUnauthorized) // bare 401, no body
		return
	}
	s.hub.Accept(c.Resp, c.Req, callerID)
}

// Handler exposes the rux router as an http.Handler (rux.Router implements
// ServeHTTP), used by httptest and by Run.
func (s *Server) Handler() http.Handler { return s.router }

// shutdownGrace bounds the graceful-shutdown drain so a stuck connection cannot
// hang the process forever (preStop / `serve stop` expect a bounded stop).
const shutdownGrace = 10 * time.Second

// RunCtx starts the HTTP server on addr and blocks until ctx is cancelled — then
// it gracefully shuts down (stop accepting new conns, drain in-flight up to
// shutdownGrace) and returns nil — or until the listener fails. http.ErrServerClosed
// from a graceful Shutdown is expected and mapped to nil. The address is logged;
// the token is never logged (plan §11).
func (s *Server) RunCtx(ctx context.Context, addr string) error {
	s.startedAt = time.UnixMilli(nowMillis())
	srv := &http.Server{Addr: addr, Handler: s.router}
	go func() {
		<-ctx.Done()
		// Use a fresh (non-cancelled) ctx so Shutdown itself gets its grace window.
		shutCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
	fmt.Printf("gofer: listening on %s\n", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Run starts the HTTP server on addr and blocks until the listener fails, with
// no graceful-shutdown signal. Retained for callers that do not manage a context.
func (s *Server) Run(addr string) error {
	return s.RunCtx(context.Background(), addr)
}
