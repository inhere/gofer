package httpapi

import (
	"io"
	"net/http"
	"strconv"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/streaming"
)

// handleJobStream serves Server-Sent Events for a single job: incremental
// stdout/stderr (`log` events) plus `status` events on every status change, and
// a final `end` event once the job reaches a terminal state (web-T3).
//
// The endpoint works for both live jobs (in-memory, status polled each tick) and
// historical jobs surviving a restart (resolved via ListJobs, status is then
// static so we replay logs and close immediately).
//
// This handler only resolves the job, prepares the SSE response (flushable
// writer + headers + opening comment) and parses the ?from resume offset; the
// streaming orchestration (log/interaction/event pumps, throttle, rotation,
// eviction fallback) lives in internal/streaming.StreamJob.
func (s *Server) handleJobStream(c *rux.Context) {
	id := c.Param("id")

	res, live := s.jobs.Get(id)
	if !live {
		// Fall back to the cross-project index so a job that survived a restart
		// (status no longer tracked in-memory) can still be streamed/replayed.
		jobs, _ := s.jobs.ListJobs(job.ListOpts{})
		for _, jr := range jobs {
			if jr.ID == id {
				res = jr
				break
			}
		}
		if res.ID == "" {
			writeError(c, http.StatusNotFound, "unknown job", "no job with id "+id)
			return
		}
	}

	// SSE needs a flushable writer; bail before sending any body if the
	// environment cannot stream (e.g. httptest.NewRecorder).
	flusher, ok := c.Resp.(http.Flusher)
	if !ok {
		writeError(c, http.StatusInternalServerError, "streaming unsupported", "response writer is not a flusher")
		return
	}

	w := c.Resp
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	// Open the stream with an SSE comment line. Writing (rather than only
	// flushing) commits the status code now, so the rux response wrapper does not
	// re-emit it at end-of-chain ("superfluous WriteHeader").
	if _, err := io.WriteString(w, ": open\n\n"); err != nil {
		return
	}
	flusher.Flush()

	// stdout supports resume via ?from (a byte offset); a missing/negative/invalid
	// value starts from the beginning.
	var opts streaming.StreamOpts
	if from, err := strconv.ParseInt(c.Query("from"), 10, 64); err == nil && from > 0 {
		opts.StdoutFrom = from
	}

	streaming.StreamJob(c.Req.Context(), w, flusher, s.jobs, id, res, live, opts)
}
