package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/config"
)

var workerIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type workerRegistrationReq struct {
	WorkerID string   `json:"worker_id"`
	Labels   []string `json:"labels,omitempty"`
	Projects []string `json:"projects,omitempty"`
}

type workerRegistrationResp struct {
	WorkerID         string `json:"worker_id"`
	WorkerToken      string `json:"worker_token"`
	WorkerConnectURL string `json:"worker_connect_url"`
}

func (s *Server) handleRegisterWorker(c *rux.Context) {
	caller := callerFromCtx(c)
	if !s.callerMayAdmin(caller) {
		writeError(c, http.StatusForbidden, "admin capability required", "registering workers requires can_admin")
		return
	}
	var req workerRegistrationReq
	if err := c.BindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request", err.Error())
		return
	}
	id := strings.TrimSpace(req.WorkerID)
	if !workerIDPattern.MatchString(id) {
		writeError(c, http.StatusBadRequest, "invalid worker id", "worker_id must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
		return
	}
	for i := range req.Labels {
		req.Labels[i] = strings.TrimSpace(req.Labels[i])
	}
	for i := range req.Projects {
		req.Projects[i] = strings.TrimSpace(req.Projects[i])
	}
	token, err := newWorkerToken()
	if err != nil {
		writeError(c, http.StatusInternalServerError, "generate worker token failed", err.Error())
		return
	}
	if s.core == nil {
		writeError(c, http.StatusServiceUnavailable, "config writer unavailable", "worker registration is only available on a running server")
		return
	}
	err = s.core.Update(func(cfg *config.Config) error {
		if cfg.Server.Workers == nil {
			cfg.Server.Workers = map[string]config.WorkerAuthConfig{}
		}
		if _, exists := cfg.Server.Workers[id]; exists {
			return errWorkerAlreadyRegistered
		}
		if cfg.Runners == nil {
			cfg.Runners = map[string]config.RunnerConfig{}
		}
		if rc, exists := cfg.Runners[id]; exists && (rc.Type != "worker" || rc.WorkerID != id) {
			return fmt.Errorf("runner %q already exists", id)
		}
		for _, projectKey := range req.Projects {
			p, ok := cfg.Projects[projectKey]
			if !ok {
				return fmt.Errorf("project %q not found", projectKey)
			}
			if !containsString(p.AllowedRunners, id) {
				p.AllowedRunners = append(p.AllowedRunners, id)
				cfg.Projects[projectKey] = p
			}
		}
		cfg.Server.Workers[id] = config.WorkerAuthConfig{Token: token, Labels: append([]string(nil), req.Labels...)}
		cfg.Runners[id] = config.RunnerConfig{Type: "worker", WorkerID: id}
		return nil
	})
	if err != nil {
		if err == errWorkerAlreadyRegistered {
			writeError(c, http.StatusConflict, "worker already exists", id)
			return
		}
		writeError(c, http.StatusBadRequest, "register worker failed", err.Error())
		return
	}
	s.recordConfigUpdate(caller, "workers", id, []string{"worker_id", "labels", "projects"})
	c.JSON(http.StatusCreated, workerRegistrationResp{WorkerID: id, WorkerToken: token, WorkerConnectURL: s.workerConnectURL()})
}

func (s *Server) handleRemoveWorker(c *rux.Context) {
	caller := callerFromCtx(c)
	if !s.callerMayAdmin(caller) {
		writeError(c, http.StatusForbidden, "admin capability required", "removing workers requires can_admin")
		return
	}
	id := strings.TrimSpace(c.Param("id"))
	if !workerIDPattern.MatchString(id) {
		writeError(c, http.StatusBadRequest, "invalid worker id", "worker_id must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
		return
	}
	if s.core == nil {
		writeError(c, http.StatusServiceUnavailable, "config writer unavailable", "worker removal is only available on a running server")
		return
	}
	found := false
	err := s.core.Update(func(cfg *config.Config) error {
		if _, ok := cfg.Server.Workers[id]; !ok {
			return errWorkerNotFound
		}
		found = true
		delete(cfg.Server.Workers, id)
		for key, rc := range cfg.Runners {
			if key == id || (rc.Type == "worker" && rc.WorkerID == id) {
				delete(cfg.Runners, key)
			}
		}
		for key, p := range cfg.Projects {
			if len(p.AllowedRunners) == 0 {
				continue
			}
			filtered := p.AllowedRunners[:0]
			for _, runner := range p.AllowedRunners {
				if runner != id {
					filtered = append(filtered, runner)
				}
			}
			p.AllowedRunners = filtered
			cfg.Projects[key] = p
		}
		return nil
	})
	if err != nil {
		if err == errWorkerNotFound || !found {
			writeError(c, http.StatusNotFound, "worker not found", id)
			return
		}
		writeError(c, http.StatusBadRequest, "remove worker failed", err.Error())
		return
	}
	if d, ok := s.hub.(interface{ DisconnectWorker(string) }); ok {
		d.DisconnectWorker(id)
	}
	s.recordConfigUpdate(caller, "workers", id, []string{"worker_id", "projects"})
	c.Resp.WriteHeader(http.StatusNoContent)
}

var (
	errWorkerAlreadyRegistered = fmt.Errorf("worker already registered")
	errWorkerNotFound          = fmt.Errorf("worker not found")
)

func newWorkerToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "gw_" + hex.EncodeToString(b), nil
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func (s *Server) workerConnectURL() string {
	addr := "127.0.0.1"
	if s.cfg != nil && strings.TrimSpace(s.cfg.Addr) != "" {
		addr = strings.TrimSpace(s.cfg.Addr)
	}
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		addr = strings.TrimPrefix(strings.TrimPrefix(addr, "http://"), "https://")
	}
	return "ws://" + addr + "/v1/workers/connect"
}
