package httpapi

import (
	"io"
	"net/http"
	"strconv"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/streaming"
)

// handleJobACPStream resolves one live or historical job and delegates all ACP
// normalization/follow work to internal/streaming. Authentication and SEC-01 GET
// permission are applied by the same middleware chain as /jobs/{id}/stream.
func (s *Server) handleJobACPStream(c *rux.Context) {
	id := c.Param("id")
	res, live := s.jobs.Get(id)
	if !live {
		jobs, _ := s.jobs.ListJobs(job.ListOpts{})
		for _, candidate := range jobs {
			if candidate.ID == id {
				res = candidate
				break
			}
		}
		if res.ID == "" {
			writeError(c, http.StatusNotFound, "unknown job", "no job with id "+id)
			return
		}
	}

	flusher, ok := c.Resp.(http.Flusher)
	if !ok {
		writeError(c, http.StatusInternalServerError, "streaming unsupported", "response writer is not a flusher")
		return
	}

	w := c.Resp
	head := w.Header()
	head.Set("Content-Type", "text/event-stream")
	head.Set("Cache-Control", "no-cache")
	head.Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	if _, err := io.WriteString(w, ": open\n\n"); err != nil {
		return
	}
	flusher.Flush()

	var opts streaming.ACPStreamOpts
	if tail, err := strconv.Atoi(c.Query("tail")); err == nil && tail > 0 {
		opts.TailEvents = min(tail, 5000)
	}
	streaming.StreamACP(c.Req.Context(), w, flusher, s.jobs, id, res, live, opts)
}
