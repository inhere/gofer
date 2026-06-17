// Package httpapi exposes the dev-agent-bridge control plane over HTTP using
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

	"github.com/gookit/rux/v2"

	"dev-agent-bridge/internal/agent"
	"dev-agent-bridge/internal/config"
	"dev-agent-bridge/internal/job"
	"dev-agent-bridge/internal/project"
)

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
	}
	s.router = s.buildRouter()
	return s
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
	}, s.authMiddleware)

	return r
}

// Handler exposes the rux router as an http.Handler (rux.Router implements
// ServeHTTP), used by httptest and by Run.
func (s *Server) Handler() http.Handler { return s.router }

// Run starts the HTTP server on addr and blocks. The listen address is logged;
// the token is never logged (plan §11).
func (s *Server) Run(addr string) error {
	srv := &http.Server{Addr: addr, Handler: s.router}
	fmt.Printf("agent-bridge: listening on %s\n", addr)
	return srv.ListenAndServe()
}
