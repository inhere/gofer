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
	"net/http"
	"os"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/webui"
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
}

// New builds a Server: it resolves the effective token, wires the rux router
// (routes + auth middleware) and returns it ready to Run or hand to httptest.
//
// token is the already-resolved effective token (serve resolves config.token /
// token_env / --token before calling New). allowEmptyToken mirrors the
// config/flag of the same name.
func New(serverCfg *config.ServerConfig, token string, allowEmptyToken bool, jobs *job.Service, projects *project.Registry, agents *agent.Registry) *Server {
	s := &Server{
		cfg:             serverCfg,
		jobs:            jobs,
		projects:        projects,
		agents:          agents,
		token:           token,
		allowEmptyToken: allowEmptyToken,
		callers:         buildCallers(serverCfg, token),
		webEnabled:      serverCfg.IsWebEnabled(),
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

	r.Group("/v1", func() {
		r.GET("/projects", s.handleListProjects)
		r.GET("/projects/{key}", s.handleGetProject)
		r.GET("/agents", s.handleListAgents)

		r.POST("/jobs", s.handleCreateJob)
		r.GET("/jobs", s.handleListJobs)
		r.GET("/jobs/{id}", s.handleGetJob)
		r.GET("/jobs/{id}/logs/stdout", s.handleJobLogsStdout)
		r.GET("/jobs/{id}/logs/stderr", s.handleJobLogsStderr)
		r.GET("/jobs/{id}/stream", s.handleJobStream)
		r.POST("/jobs/{id}/cancel", s.handleCancelJob)

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
