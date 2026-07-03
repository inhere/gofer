package httpapi

import (
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/config"
)

// handleHealth is the unauthenticated liveness probe (plan §7).
func (s *Server) handleHealth(c *rux.Context) {
	c.JSON(http.StatusOK, rux.M{
		"ok":          true,
		"service":     "gofer",
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

type projectWriteReq struct {
	Key               string   `json:"key"`
	HostPath          string   `json:"host_path"`
	ContainerPath     string   `json:"container_path,omitempty"`
	DefaultAgent      string   `json:"default_agent,omitempty"`
	AllowedAgents     []string `json:"allowed_agents,omitempty"`
	AllowedRunners    []string `json:"allowed_runners,omitempty"`
	AllowExec         bool     `json:"allow_exec"`
	MaxConcurrentJobs int      `json:"max_concurrent_jobs,omitempty"`
}

type projectWriteResp struct {
	projectView
	Warnings []string `json:"warnings,omitempty"`
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

func (s *Server) handleCreateProject(c *rux.Context) {
	caller := callerFromCtx(c)
	if !s.callerMayAdmin(caller) {
		writeError(c, http.StatusForbidden, "admin not permitted for this caller", "caller lacks can_admin capability")
		return
	}
	var req projectWriteReq
	if err := c.BindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	key, proj, err := s.validateProjectWrite(strings.TrimSpace(req.Key), req)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid project config", err.Error())
		return
	}
	if err := s.projects.Add(key, proj, false); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			writeError(c, http.StatusConflict, "project already exists", err.Error())
			return
		}
		writeError(c, http.StatusInternalServerError, "create project failed", err.Error())
		return
	}
	recordConfigProjectEvent("created", caller, key)
	c.JSON(http.StatusOK, s.projectWriteResponse(key, proj))
}

func (s *Server) handleUpdateProject(c *rux.Context) {
	caller := callerFromCtx(c)
	if !s.callerMayAdmin(caller) {
		writeError(c, http.StatusForbidden, "admin not permitted for this caller", "caller lacks can_admin capability")
		return
	}
	var req projectWriteReq
	if err := c.BindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	key, proj, err := s.validateProjectWrite(strings.TrimSpace(c.Param("key")), req)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid project config", err.Error())
		return
	}
	if err := s.projects.Add(key, proj, true); err != nil {
		writeError(c, http.StatusInternalServerError, "update project failed", err.Error())
		return
	}
	recordConfigProjectEvent("updated", caller, key)
	c.JSON(http.StatusOK, s.projectWriteResponse(key, proj))
}

func (s *Server) handleDeleteProject(c *rux.Context) {
	caller := callerFromCtx(c)
	if !s.callerMayAdmin(caller) {
		writeError(c, http.StatusForbidden, "admin not permitted for this caller", "caller lacks can_admin capability")
		return
	}
	key := strings.TrimSpace(c.Param("key"))
	if err := s.projects.Remove(key); err != nil {
		if strings.Contains(err.Error(), "unknown project") {
			writeError(c, http.StatusNotFound, "unknown project", err.Error())
			return
		}
		writeError(c, http.StatusInternalServerError, "delete project failed", err.Error())
		return
	}
	recordConfigProjectEvent("deleted", caller, key)
	c.JSON(http.StatusOK, rux.M{"status": "ok"})
}

func (s *Server) validateProjectWrite(key string, req projectWriteReq) (string, config.ProjectConfig, error) {
	if key == "" {
		return "", config.ProjectConfig{}, fmt.Errorf("key is required")
	}
	hostPath := strings.TrimSpace(req.HostPath)
	if hostPath == "" {
		return "", config.ProjectConfig{}, fmt.Errorf("host_path is required")
	}
	cfg := s.projects.Config()
	if req.DefaultAgent != "" {
		if _, ok := cfg.Agents[req.DefaultAgent]; !ok {
			return "", config.ProjectConfig{}, fmt.Errorf("default_agent %q is not defined", req.DefaultAgent)
		}
		if len(req.AllowedAgents) > 0 && !slices.Contains(req.AllowedAgents, req.DefaultAgent) {
			return "", config.ProjectConfig{}, fmt.Errorf("default_agent %q is not in allowed_agents", req.DefaultAgent)
		}
	}
	for _, a := range req.AllowedAgents {
		if _, ok := cfg.Agents[a]; !ok {
			return "", config.ProjectConfig{}, fmt.Errorf("allowed_agent %q is not defined", a)
		}
	}
	for _, rn := range req.AllowedRunners {
		if rn == "local" {
			continue
		}
		if _, ok := cfg.Runners[rn]; !ok {
			return "", config.ProjectConfig{}, fmt.Errorf("allowed_runner %q is not defined", rn)
		}
	}
	return key, config.ProjectConfig{
		HostPath:          hostPath,
		ContainerPath:     strings.TrimSpace(req.ContainerPath),
		DefaultAgent:      req.DefaultAgent,
		AllowedAgents:     req.AllowedAgents,
		AllowedRunners:    req.AllowedRunners,
		AllowExec:         req.AllowExec,
		MaxConcurrentJobs: req.MaxConcurrentJobs,
	}, nil
}

func (s *Server) projectWriteResponse(key string, proj config.ProjectConfig) projectWriteResp {
	resp := projectWriteResp{
		projectView: projectView{
			Key:               key,
			HostPath:          proj.HostPath,
			ContainerPath:     proj.ContainerPath,
			DefaultAgent:      proj.DefaultAgent,
			AllowedAgents:     proj.AllowedAgents,
			AllowedRunners:    proj.AllowedRunners,
			AllowExec:         proj.AllowExec,
			MaxConcurrentJobs: proj.MaxConcurrentJobs,
		},
	}
	results, _, err := s.projects.Validate(key)
	if err != nil {
		resp.Warnings = append(resp.Warnings, err.Error())
		return resp
	}
	for _, r := range results {
		if !r.OK {
			resp.Warnings = append(resp.Warnings, r.Info)
		}
	}
	return resp
}

func recordConfigProjectEvent(action, caller, key string) {
	slog.Info("config project "+action, "caller_id", caller, "project_key", key)
}
