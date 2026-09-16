package httpapi

import (
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/agent"
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
//
// AllowInteractive carries the RESOLVED switch (config.ProjectConfig
// .IsInteractiveAllowed, so a legacy non-empty interactive_allowed_agents reads as
// true) and InteractiveAllowedAgents the optional narrowing list. Both are emitted
// UNCONDITIONALLY (no omitempty), like allow_exec and /v1/meta's gates: the console
// is served from disk while the binary ships separately, so a new console may talk to
// an older server and must read a MISSING field as "server predates AGT-02", never as
// "gate off".
type projectView struct {
	Key                      string   `json:"key"`
	HostPath                 string   `json:"host_path"`
	ContainerPath            string   `json:"container_path,omitempty"`
	DefaultAgent             string   `json:"default_agent,omitempty"`
	AllowedAgents            []string `json:"allowed_agents,omitempty"`
	AllowedRunners           []string `json:"allowed_runners,omitempty"`
	AllowInteractive         bool     `json:"allow_interactive"`
	InteractiveAllowedAgents []string `json:"interactive_allowed_agents"`
	AllowExec                bool     `json:"allow_exec"`
	MaxConcurrentJobs        int      `json:"max_concurrent_jobs,omitempty"`
}

// projectWriteReq is the create/update body. Every value field is a pointer so "not
// in the request" is distinguishable from "cleared": PUT MERGES the request over the
// stored ProjectConfig (bd h-aii-3scy), so an omitted field must keep its stored
// value — before this, saving the console's form silently wiped exchange_subdir /
// result_subdir / capture_diff / notify_enabled, none of which the form carries. A
// present empty value ("", [], false, 0) is applied as written.
type projectWriteReq struct {
	Key                      string    `json:"key"`
	HostPath                 *string   `json:"host_path,omitempty"`
	ContainerPath            *string   `json:"container_path,omitempty"`
	DefaultAgent             *string   `json:"default_agent,omitempty"`
	AllowedAgents            *[]string `json:"allowed_agents,omitempty"`
	AllowedRunners           *[]string `json:"allowed_runners,omitempty"`
	AllowInteractive         *bool     `json:"allow_interactive,omitempty"`
	InteractiveAllowedAgents *[]string `json:"interactive_allowed_agents,omitempty"`
	AllowExec                *bool     `json:"allow_exec,omitempty"`
	MaxConcurrentJobs        *int      `json:"max_concurrent_jobs,omitempty"`
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
	c.JSON(http.StatusOK, projectViewOf(key, p))
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
	key := strings.TrimSpace(req.Key)
	// No stored config to preserve on create: an omitted field simply stays zero.
	proj := mergeProjectWrite(config.ProjectConfig{}, req)
	if err := s.validateProjectWrite(key, proj); err != nil {
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

// handleUpdateProject merges the request over the STORED config (bd h-aii-3scy) and
// writes the result back. Fields the request does not carry — exchange_subdir,
// result_subdir, capture_diff, notify_enabled, … — survive the round-trip instead of
// being reset by a form that never knew about them. A PUT for an unknown key still
// creates it (force), keeping the previous behaviour.
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
	key := strings.TrimSpace(c.Param("key"))
	base := config.ProjectConfig{}
	if stored, err := s.projects.Get(key); err == nil {
		base = stored
	}
	proj := mergeProjectWrite(base, req)
	if err := s.validateProjectWrite(key, proj); err != nil {
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

// mergeProjectWrite applies every field the request explicitly carried onto base and
// returns the result. A nil pointer means "not in the request" and keeps base's value
// verbatim — that is the whole point of the pointer fields (see projectWriteReq). The
// switch pointer is copied, never aliased into the stored config.
func mergeProjectWrite(base config.ProjectConfig, req projectWriteReq) config.ProjectConfig {
	if req.HostPath != nil {
		base.HostPath = strings.TrimSpace(*req.HostPath)
	}
	if req.ContainerPath != nil {
		base.ContainerPath = strings.TrimSpace(*req.ContainerPath)
	}
	if req.DefaultAgent != nil {
		base.DefaultAgent = strings.TrimSpace(*req.DefaultAgent)
	}
	if req.AllowedAgents != nil {
		base.AllowedAgents = *req.AllowedAgents
	}
	if req.AllowedRunners != nil {
		base.AllowedRunners = *req.AllowedRunners
	}
	if req.AllowInteractive != nil {
		allow := *req.AllowInteractive
		base.AllowInteractive = &allow
	}
	if req.InteractiveAllowedAgents != nil {
		base.InteractiveAllowedAgents = *req.InteractiveAllowedAgents
	}
	if req.AllowExec != nil {
		base.AllowExec = *req.AllowExec
	}
	if req.MaxConcurrentJobs != nil {
		base.MaxConcurrentJobs = *req.MaxConcurrentJobs
	}
	return base
}

// validateProjectWrite checks the MERGED project — what will actually be stored, not
// just what this request carried — so a partial PUT cannot leave the config in a state
// its own validation would reject.
func (s *Server) validateProjectWrite(key string, proj config.ProjectConfig) error {
	if key == "" {
		return fmt.Errorf("key is required")
	}
	if proj.HostPath == "" {
		return fmt.Errorf("host_path is required")
	}
	cfg := s.projects.Config()
	if proj.DefaultAgent != "" {
		if _, ok := cfg.Agents[proj.DefaultAgent]; !ok {
			return fmt.Errorf("default_agent %q is not defined", proj.DefaultAgent)
		}
		if len(proj.AllowedAgents) > 0 && !slices.Contains(proj.AllowedAgents, proj.DefaultAgent) {
			return fmt.Errorf("default_agent %q is not in allowed_agents", proj.DefaultAgent)
		}
	}
	for _, a := range proj.AllowedAgents {
		if _, ok := cfg.Agents[a]; !ok {
			return fmt.Errorf("allowed_agent %q is not defined", a)
		}
	}
	for _, rn := range proj.AllowedRunners {
		if rn == "local" {
			continue
		}
		if _, ok := cfg.Runners[rn]; !ok {
			return fmt.Errorf("allowed_runner %q is not defined", rn)
		}
	}
	// AGT-02: every entry of the interactive narrowing list must name an agent the
	// project may use at all AND one that can actually run interactively — otherwise
	// the console offers a submit admission is guaranteed to reject.
	for _, a := range proj.InteractiveAllowedAgents {
		ac, ok := cfg.Agents[a]
		if !ok {
			return fmt.Errorf("interactive_allowed_agents: agent %q is not defined", a)
		}
		if _, interactive := agent.Modes(ac); !interactive {
			return fmt.Errorf("interactive_allowed_agents: agent %q has no interactive mode", a)
		}
		if len(proj.AllowedAgents) > 0 && !slices.Contains(proj.AllowedAgents, a) {
			return fmt.Errorf("interactive_allowed_agents: agent %q is not in allowed_agents", a)
		}
	}
	return nil
}

// projectViewOf renders one stored project for the read paths (GET /v1/projects/{key}
// and the /v1/config aggregate), so the two can never drift.
func projectViewOf(key string, proj config.ProjectConfig) projectView {
	return projectView{
		Key:                      key,
		HostPath:                 proj.HostPath,
		ContainerPath:            proj.ContainerPath,
		DefaultAgent:             proj.DefaultAgent,
		AllowedAgents:            proj.AllowedAgents,
		AllowedRunners:           proj.AllowedRunners,
		AllowInteractive:         proj.IsInteractiveAllowed(),
		InteractiveAllowedAgents: nonNil(proj.InteractiveAllowedAgents),
		AllowExec:                proj.AllowExec,
		MaxConcurrentJobs:        proj.MaxConcurrentJobs,
	}
}

func (s *Server) projectWriteResponse(key string, proj config.ProjectConfig) projectWriteResp {
	resp := projectWriteResp{projectView: projectViewOf(key, proj)}
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
