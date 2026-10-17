package httpapi

import (
	"errors"
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
	if !adminUserOnly(c, "register a worker") {
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
	if err := config.CheckWorkerID(id); err != nil {
		writeError(c, http.StatusBadRequest, "invalid worker id", err.Error())
		return
	}
	for i := range req.Labels {
		req.Labels[i] = strings.TrimSpace(req.Labels[i])
	}
	for i := range req.Projects {
		req.Projects[i] = strings.TrimSpace(req.Projects[i])
	}
	token, err := config.NewWorkerToken()
	if err != nil {
		writeError(c, http.StatusInternalServerError, "generate worker token failed", err.Error())
		return
	}
	if s.core == nil {
		writeError(c, http.StatusServiceUnavailable, "config writer unavailable", "worker registration is only available on a running server")
		return
	}
	err = s.core.Update(func(cfg *config.Config) error {
		return config.RegisterWorkerInto(cfg, id, req.Labels, req.Projects, token)
	})
	if err != nil {
		if errors.Is(err, config.ErrWorkerAlreadyRegistered) {
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
	if !adminUserOnly(c, "remove a worker") {
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
		var err error
		found, err = config.RemoveWorkerFrom(cfg, id, "")
		return err
	})
	if err != nil {
		if errors.Is(err, config.ErrWorkerNotFound) || !found {
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

func (s *Server) workerConnectURL() string {
	addr := "127.0.0.1"
	if cfg := s.serverCfg(); cfg != nil && strings.TrimSpace(cfg.Addr) != "" {
		addr = strings.TrimSpace(cfg.Addr)
	}
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		addr = strings.TrimPrefix(strings.TrimPrefix(addr, "http://"), "https://")
	}
	return "ws://" + addr + "/v1/workers/connect"
}
