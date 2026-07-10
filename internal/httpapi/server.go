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
	"github.com/inhere/gofer/internal/webui"
	"github.com/inhere/gofer/internal/wshub"
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

	// hub is the ws-worker hub singleton; when non-nil the /v1/workers/connect WS
	// route is mounted (ws-worker). It is nil for callers that do not run the hub.
	hub *wshub.Hub
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

	// metrics is the E16 Prometheus instrumentation (nil = no /metrics endpoint,
	// no HTTP middleware). It is injected post-construction by SetMetrics (serve)
	// rather than via New so the existing positional constructor and its many call
	// sites stay untouched. metricsEnabled/metricsToken mirror serverCfg.Metrics
	// and gate the endpoint's mounting + optional Bearer check.
	metrics        *metrics.Metrics
	metricsEnabled bool
	metricsToken   string

	// presence is the E36 driver-agent identity/mailbox service backing the
	// /v1/agents/* + /v1/messages endpoints. Injected post-construction by
	// SetPresence (serve), mirroring SetMetrics so the wide positional New stays
	// untouched (design D3). nil => the presence routes are not mounted (most
	// tests / mcp-less callers).
	presence *presence.Service

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

// SetPtySessionStore injects the pty-session persistence seam (WEB-03 P3). serve
// passes the *jobstore.Store (which satisfies PtySessionStore); a nil store leaves
// pty session persistence off (mcp/tests). It mounts no routes → no router rebuild.
func (s *Server) SetPtySessionStore(store PtySessionStore) { s.ptySessions = store }

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
func New(serverCfg *config.ServerConfig, token string, allowEmptyToken bool, jobs *job.Service, wf *workflow.Engine, projects *project.Registry, agents *agent.Registry, hub *wshub.Hub, runners map[string]config.RunnerConfig, prober runnerProber, workers workerRegistry) *Server {
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
		hub:             hub,
		runners:         runners,
		prober:          prober,
		workers:         workers,
		limiters:        map[string]*rate.Limiter{},
		attachTickets:   NewAttachTicketStore(),
		startedAt:       time.UnixMilli(nowMillis()),
	}
	s.router = s.buildRouter()
	return s
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
	}
	if s.hub != nil && s.relayNonces != nil && s.ptyRelays != nil {
		r.GET("/v1/workers/pty-connect", s.handlePtyConnect)
	}
	r.GET("/v1/jobs/{id}/attach", s.handleJobAttach)

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

		r.GET("/agents", s.handleListAgents)

		// C6/P4: remote-node observability — status of every configured runner
		// (local / peer-http probe / worker heartbeat). Normal authed JSON endpoint
		// (NOT the bare-401 WS path), list-style shape mirroring /v1/jobs.
		r.GET("/runners", s.handleListRunners)

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

		// WEB-03 P3 (D-P3-7): download a job's recorded pty session (asciinema v2
		// cast). Same owner/admin gate as the browser attach path. The recording
		// gate does NOT do a remote-source 409 (unlike artifact download): a pty
		// cast is always written hub-side, so it is served regardless of where the
		// job ran. Encrypted casts are stream-decrypted.
		r.GET("/pty/sessions", s.handleRecentPtySessions)
		r.GET("/jobs/{id}/pty/recording", s.handlePtyRecording)
		r.GET("/jobs/{id}/pty/sessions", s.handlePtySessions)

		r.POST("/jobs/{id}/cancel", s.handleCancelJob)

		// session-capture(P2)：用源 job 的 SessionID 续接底层 agent 会话，起一个新
		// exec job（同 runner）。body {prompt, runner?}；校验失败 4xx/404(resumeStatus)。
		r.POST("/jobs/{id}/resume", s.handleResumeJob)

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
		r.POST("/plans/{id}/jobs", s.handleAttachPlanJob)
		r.POST("/plans/{id}/todos", s.handleAddPlanTodo)
		r.PATCH("/todos/{todo_id}", s.handleUpdateTodo)

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
		h, _ := webui.Handler()
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
