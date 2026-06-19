package httpapi

import (
	"errors"
	"net/http"
	"path/filepath"
	"strconv"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/store"
)

// maxLogTailBytes caps the response size of the log endpoints: only the last
// 256KB of a stream is returned (plan §9-P5/§11). Full logs remain inspectable
// on disk via the result dir.
const maxLogTailBytes = 256 * 1024

// handleCreateJob parses a JobRequest, submits it and returns the initial
// JobResult (with the assigned id). Validation failures map to a 404 (unknown
// project) or 400 (anything else) via the job package sentinels.
func (s *Server) handleCreateJob(c *rux.Context) {
	var req job.JobRequest
	if err := c.BindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}

	res, err := s.jobs.Submit(req)
	if err != nil {
		writeError(c, submitStatus(err), "job rejected", err.Error())
		return
	}
	c.JSON(http.StatusOK, res)
}

// submitStatus maps a Submit error to an HTTP status: an unknown project is a
// 404 (consistent with GET /v1/projects/{key}); every other rejection is a 400.
func submitStatus(err error) int {
	if errors.Is(err, job.ErrUnknownProject) {
		return http.StatusNotFound
	}
	return http.StatusBadRequest
}

// handleListJobs returns job snapshots merged from the per-project index and
// the in-memory live state. Optional query params: status, project, limit (a
// non-numeric limit falls back to the default). The list is always a non-nil
// array, so an empty result serialises as {"jobs":[]}.
func (s *Server) handleListJobs(c *rux.Context) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	list, err := s.jobs.ListJobs(job.ListOpts{
		Project: c.Query("project"),
		Status:  c.Query("status"),
		Limit:   limit,
	})
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list jobs failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"jobs": list})
}

// handleGetJob returns the current snapshot of a job; an unknown id is a 404.
func (s *Server) handleGetJob(c *rux.Context) {
	id := c.Param("id")
	res, ok := s.jobs.Get(id)
	if !ok {
		writeError(c, http.StatusNotFound, "unknown job", "no job with id "+id)
		return
	}
	c.JSON(http.StatusOK, res)
}

// handleJobLogsStdout / handleJobLogsStderr return the tail of a job's log
// stream (capped at maxLogTailBytes). Output is sent as text/plain.
func (s *Server) handleJobLogsStdout(c *rux.Context) { s.serveLog(c, store.StreamStdout) }
func (s *Server) handleJobLogsStderr(c *rux.Context) { s.serveLog(c, store.StreamStderr) }

// serveLog reads the last maxLogTailBytes of the given stream for the job and
// writes it as text/plain. It locates the job's result dir from its JobResult
// (ResultDir == <base>/<job_id>), so the FileStore base is its parent dir.
func (s *Server) serveLog(c *rux.Context, stream store.Stream) {
	id := c.Param("id")
	res, ok := s.jobs.Get(id)
	if !ok {
		writeError(c, http.StatusNotFound, "unknown job", "no job with id "+id)
		return
	}

	base := filepath.Dir(res.ResultDir)
	data, err := store.NewFileStore(base).ReadLogTail(id, stream, maxLogTailBytes)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "read log failed", err.Error())
		return
	}
	c.Text(http.StatusOK, string(data))
}

// handleCancelJob requests cancellation. Cancel is a stable no-op for an already
// terminal job (returns the current snapshot), and a 404 for an unknown id.
func (s *Server) handleCancelJob(c *rux.Context) {
	id := c.Param("id")
	if err := s.jobs.Cancel(id); err != nil {
		// The only Cancel error is an unknown job id (terminal jobs are no-ops).
		writeError(c, http.StatusNotFound, "unknown job", err.Error())
		return
	}
	res, _ := s.jobs.Get(id)
	c.JSON(http.StatusOK, res)
}
