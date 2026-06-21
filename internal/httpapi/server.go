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
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/webui"
	"github.com/inhere/gofer/internal/wshub"
)

// ctxCallerID is the rux context key under which authMiddleware stores the
// authenticated caller id for handlers to read (callerFromCtx). It is empty for
// the allow_empty_token pass-through path (no token configured).
const ctxCallerID = "caller_id"

// callerEntry pairs a known bearer token with the caller id stamped onto jobs
// that authenticate with it (C2). The token is held in memory only.
type callerEntry struct {
	id    string
	token string
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

// Server holds the wired dependencies and the rux router. It is constructed once
// (New) and either started with Run or exposed as an http.Handler (Handler) for
// httptest.
type Server struct {
	cfg      *config.ServerConfig
	jobs     *job.Service
	projects *project.Registry
	agents   *agent.Registry
	router   *rux.Router

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
func New(serverCfg *config.ServerConfig, token string, allowEmptyToken bool, jobs *job.Service, projects *project.Registry, agents *agent.Registry, hub *wshub.Hub, runners map[string]config.RunnerConfig, prober runnerProber, workers workerRegistry) *Server {
	s := &Server{
		cfg:             serverCfg,
		jobs:            jobs,
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
	}
	s.router = s.buildRouter()
	return s
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
			out = append(out, callerEntry{id: cc.ID, token: tok})
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
			out = append(out, callerEntry{id: workerID, token: tok})
		}
	}
	if token != "" {
		out = append(out, callerEntry{id: "default", token: token})
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

	// ws-worker WS endpoint. Registered OUTSIDE the /v1 authMiddleware group: a WS
	// upgrade cannot use the JSON error envelope / web fallback — a rejected
	// handshake must be a bare 401 (not a {error,detail} body). It does its own
	// Bearer auth (reusing lookupCaller) then hands off to hub.Accept. Mounted only
	// when the hub is wired (serve); nil for hub-less callers (some tests).
	if s.hub != nil {
		r.GET("/v1/workers/connect", s.handleWorkerConnect)
	}

	r.Group("/v1", func() {
		r.GET("/projects", s.handleListProjects)
		r.GET("/projects/{key}", s.handleGetProject)
		r.GET("/agents", s.handleListAgents)

		// C6/P4: remote-node observability — status of every configured runner
		// (local / peer-http probe / worker heartbeat). Normal authed JSON endpoint
		// (NOT the bare-401 WS path), list-style shape mirroring /v1/jobs.
		r.GET("/runners", s.handleListRunners)

		// G4 (design §6.4): read-only form-options aggregate for the web console
		// submit form (projects/agents/runners/workers in one authed GET).
		r.GET("/meta", s.handleMeta)

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

		r.POST("/jobs/{id}/cancel", s.handleCancelJob)

		// 工作流(job 链)：提交/列表/详情(含 step 链)/取消。详情附每步 {step_index,
		// name,job_id,status}，列表 ?status= 过滤；提交校验失败复用 submitStatus(404/400)。
		r.POST("/workflows", s.handleCreateWorkflow)
		r.GET("/workflows", s.handleListWorkflows)
		r.GET("/workflows/{id}", s.handleGetWorkflow)
		r.POST("/workflows/{id}/cancel", s.handleCancelWorkflow)

		// P9 running-job two-way interactions.
		r.POST("/jobs/{id}/interactions", s.handleCreateInteraction)
		r.GET("/jobs/{id}/interactions", s.handleListInteractions)
		r.POST("/jobs/{id}/interactions/{interaction_id}/answer", s.handleAnswerInteraction)
	}, s.authMiddleware)

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

// Run starts the HTTP server on addr and blocks. The listen address is logged;
// the token is never logged (plan §11).
func (s *Server) Run(addr string) error {
	srv := &http.Server{Addr: addr, Handler: s.router}
	fmt.Printf("gofer: listening on %s\n", addr)
	return srv.ListenAndServe()
}
