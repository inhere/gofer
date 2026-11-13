package httpapi

import (
	"net/http"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/workerupgrade"
)

type workerDetailView struct {
	WorkerID  string        `json:"worker_id"`
	Connected bool          `json:"connected"`
	Worker    *WorkerStatus `json:"worker,omitempty"`
	// Upgrade is the latest remote-upgrade attempt of this worker (any state), kept
	// across restarts so the outcome stays readable after the worker comes back.
	Upgrade        *workerupgrade.Record  `json:"upgrade,omitempty"`
	UpgradeHistory []workerupgrade.Record `json:"upgrade_history,omitempty"`
	// LastReplaced is the latest time another process with a different instance_id
	// took this worker_id over; nil when it never happened.
	LastReplaced *workerReplacedView `json:"last_replaced,omitempty"`
}

func (s *Server) handleWorkerView(c *rux.Context) {
	id := c.Param("id")
	cfg := s.serverCfg()
	if id == "" || cfg == nil {
		writeError(c, http.StatusBadRequest, "missing worker id", "path must be /v1/workers/{id}")
		return
	}
	if _, configured := cfg.Workers[id]; !configured {
		writeError(c, http.StatusNotFound, "unknown worker", "worker "+id+" is not configured")
		return
	}
	view := workerDetailView{WorkerID: id, Upgrade: s.latestUpgrade(id), UpgradeHistory: s.upgradeHistory(id), LastReplaced: s.latestReplaced(id)}
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
	cfg := s.serverCfg()
	if cfg == nil {
		writeError(c, http.StatusServiceUnavailable, "worker view unavailable", "server config is not loaded")
		return
	}
	if _, configured := cfg.Workers[id]; !configured {
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
