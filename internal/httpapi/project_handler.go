package httpapi

import (
	"net/http"
	"time"

	"github.com/gookit/rux/v2"
)

// handleHealth is the unauthenticated liveness probe (plan §7).
func (s *Server) handleHealth(c *rux.Context) {
	c.JSON(http.StatusOK, rux.M{
		"ok":          true,
		"service":     "agent-bridge",
		"server_time": time.Now().UnixMilli(),
	})
}

// handleListProjects returns the registered project keys.
func (s *Server) handleListProjects(c *rux.Context) {
	c.JSON(http.StatusOK, rux.M{"projects": s.projects.List()})
}

// projectView is the per-project detail payload. It deliberately omits nothing
// sensitive (project config holds no secrets); host/container paths are useful
// for the operator driving the bridge.
type projectView struct {
	Key               string   `json:"key"`
	HostPath          string   `json:"host_path"`
	ContainerPath     string   `json:"container_path,omitempty"`
	DefaultAgent      string   `json:"default_agent,omitempty"`
	AllowedAgents     []string `json:"allowed_agents,omitempty"`
	AllowedRunners    []string `json:"allowed_runners,omitempty"`
	AllowExec         bool     `json:"allow_exec"`
	MaxConcurrentJobs int      `json:"max_concurrent_jobs,omitempty"`
}

// handleGetProject returns one project's detail; an unknown key is a 404.
func (s *Server) handleGetProject(c *rux.Context) {
	key := c.Param("key")
	p, err := s.projects.Get(key)
	if err != nil {
		writeError(c, http.StatusNotFound, "unknown project", err.Error())
		return
	}
	c.JSON(http.StatusOK, projectView{
		Key:               key,
		HostPath:          p.HostPath,
		ContainerPath:     p.ContainerPath,
		DefaultAgent:      p.DefaultAgent,
		AllowedAgents:     p.AllowedAgents,
		AllowedRunners:    p.AllowedRunners,
		AllowExec:         p.AllowExec,
		MaxConcurrentJobs: p.MaxConcurrentJobs,
	})
}
