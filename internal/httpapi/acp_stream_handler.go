package httpapi

import (
	"context"
	"io"
	"net/http"
	"strconv"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/streaming"
)

// handleJobACPStream resolves one live or historical job and delegates all ACP
// normalization/follow work to internal/streaming. Authentication and SEC-01 GET
// permission are applied by the same middleware chain as /jobs/{id}/stream; the
// InHandler(job.read.others, deny) rule on the job's owner is applied here (plan T1.5a).
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
	if !s.mayReadJob(c, res) {
		s.denyJobAction(c, authz.PermJobReadOthers)
		return
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
	opts.PromptAttachments = s.promptAttachments(res.ID, res.SessionID)
	if s.hub != nil {
		opts.WorkerProtocol = s.hub.WorkerProtocol
	}
	// Design §6: an ACP stream is a long connection too, registered with the permission it read the
	// job under — a revoked job.read.others ends it (the same rule as /v1/jobs/{id}/stream).
	streamCtx, cancelStream := context.WithCancel(c.Req.Context())
	defer cancelStream()
	spec := s.requestLiveConnSpec(c, liveConnSSE, id, s.liveJobReadPerms(c, res.CallerID)...)
	// 7798 第二期: the stream is judged by the job's project on every re-judgment too, so a
	// tightened ceiling ends it.
	spec.projects = s.liveConnJobProjects(id)
	spec.close = func(string) { cancelStream() }
	releaseConn := s.registerLiveConn(spec)
	defer releaseConn.release()

	streaming.StreamACP(streamCtx, w, flusher, s.jobs, id, res, live, opts)
}
