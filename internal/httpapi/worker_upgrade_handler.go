package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gookit/rux/v2"
	"github.com/inhere/gofer/internal/wsproto"
)

type workerUpgrader interface {
	UpgradeWorker(context.Context, string, wsproto.Upgrade) error
}

func (s *Server) SetWorkerUpgrader(u workerUpgrader) { s.upgrader = u }

func (s *Server) handleWorkerUpgrade(c *rux.Context) {
	caller := callerFromCtx(c)
	if !s.callerMayAdmin(caller) {
		writeError(c, http.StatusForbidden, "admin not permitted for this caller", "caller lacks can_admin capability")
		return
	}
	id := c.Param("id")
	if _, ok := s.workerConfigs()[id]; !ok {
		writeError(c, http.StatusNotFound, "unknown worker", id)
		return
	}
	if s.upgrader == nil {
		writeError(c, http.StatusServiceUnavailable, "worker upgrade unavailable", "this server has no worker upgrade hub")
		return
	}
	var req wsproto.Upgrade
	if c.Req.Body != nil && c.Req.ContentLength != 0 {
		if err := json.NewDecoder(c.Req.Body).Decode(&req); err != nil {
			writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
			return
		}
	}
	if req.SHA256 == "" || req.Size < 0 || req.URLPath == "" {
		writeError(c, http.StatusBadRequest, "invalid upgrade request", "sha256, size and url_path are required")
		return
	}
	ctx, cancel := context.WithTimeout(c.Req.Context(), 60*time.Second)
	defer cancel()
	if err := s.upgrader.UpgradeWorker(ctx, id, req); err != nil {
		status := http.StatusConflict
		if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		writeError(c, status, "worker upgrade failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"worker_id": id, "upgraded": true})
}
