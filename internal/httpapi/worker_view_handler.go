package httpapi

import (
	"net/http"

	"github.com/gookit/rux/v2"
)

type workerDetailView struct {
	WorkerID  string        `json:"worker_id"`
	Connected bool          `json:"connected"`
	Worker    *WorkerStatus `json:"worker,omitempty"`
}

func (s *Server) handleWorkerView(c *rux.Context) {
	id := c.Param("id")
	if id == "" || s.cfg == nil {
		writeError(c, http.StatusBadRequest, "missing worker id", "path must be /v1/workers/{id}")
		return
	}
	if _, configured := s.cfg.Workers[id]; !configured {
		writeError(c, http.StatusNotFound, "unknown worker", "worker "+id+" is not configured")
		return
	}
	view := workerDetailView{WorkerID: id}
	if s.workers != nil {
		if status, ok := s.workers.WorkerStatus(id); ok {
			view.Connected = status.Connected
			view.Worker = &status
		}
	}
	c.JSON(http.StatusOK, view)
}

func (s *Server) handleWorkerProjects(c *rux.Context) {
	id := c.Param("id")
	if id == "" {
		writeError(c, http.StatusBadRequest, "missing worker id", "path must be /v1/workers/{id}/projects")
		return
	}
	view := workerDetailView{WorkerID: id}
	if s.workers != nil {
		if status, ok := s.workers.WorkerStatus(id); ok {
			view.Connected = status.Connected
			view.Worker = &status
		}
	}
	if s.cfg == nil {
		writeError(c, http.StatusServiceUnavailable, "worker view unavailable", "server config is not loaded")
		return
	}
	if _, configured := s.cfg.Workers[id]; !configured {
		writeError(c, http.StatusNotFound, "unknown worker", "worker "+id+" is not configured")
		return
	}
	c.JSON(http.StatusOK, map[string]any{
		"worker_id": id,
		"connected": view.Connected,
		"projects": func() []string {
			if view.Worker == nil {
				return []string{}
			}
			return view.Worker.Projects
		}(),
		"worker": view.Worker,
	})
}
