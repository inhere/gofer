package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// Knowledge candidates (gofer-3nxa.2):
//
//	GET  /v1/memory-candidates?job_id=&project=&status=pending|accepted|rejected|all
//	POST /v1/memory-candidates/{n}/accept   {key, kind, summary, global, project}
//	POST /v1/memory-candidates/{n}/reject
//
// Candidates are recorded by the job service when a job delivers; accepting (it writes
// a scoped memory) and rejecting are a person's decisions — a job credential may only
// read, a worker transport token not even that.

func (s *Server) handleListMemoryCandidates(c *rux.Context) {
	if callerKindFromCtx(c) == callerKindWorker {
		writeError(c, http.StatusForbidden, "memory candidates not permitted for this caller", "worker tokens are a transport credential")
		return
	}
	q := c.Req.URL.Query()
	items, err := s.jobs.ListMemoryCandidates(jobstore.MemoryCandidateFilter{JobID: q.Get("job_id"), ProjectKey: q.Get("project"), Status: q.Get("status")})
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list memory candidates failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"candidates": items})
}

func (s *Server) handleDecideMemoryCandidate(accept bool) rux.HandlerFunc {
	what := "reject a memory candidate"
	if accept {
		what = "accept a memory candidate"
	}
	return func(c *rux.Context) {
		if !stewardUserOnly(c, what) {
			return
		}
		n, err := strconv.ParseInt(c.Param("n"), 10, 64)
		if err != nil || n <= 0 {
			writeError(c, http.StatusBadRequest, "invalid candidate id", c.Param("n"))
			return
		}
		by := callerFromCtx(c)
		if !accept {
			cand, err := s.jobs.RejectMemoryCandidate(n, by)
			if err != nil {
				writeMemoryCandidateError(c, err, what)
				return
			}
			c.JSON(http.StatusOK, job.MemoryCandidateDecision{Candidate: cand})
			return
		}
		var body job.AcceptMemoryCandidateInput
		if err := c.BindJSON(&body); err != nil {
			writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
			return
		}
		out, err := s.jobs.AcceptMemoryCandidate(n, body, by)
		if err != nil {
			writeMemoryCandidateError(c, err, what)
			return
		}
		c.JSON(http.StatusOK, out)
	}
}

func writeMemoryCandidateError(c *rux.Context, err error, what string) {
	switch {
	case errors.Is(err, job.ErrInvalidRequest):
		writeError(c, http.StatusBadRequest, what+" failed", err.Error())
	case errors.Is(err, jobstore.ErrMemoryCandidateNotFound):
		writeError(c, http.StatusNotFound, what+" failed", err.Error())
	case errors.Is(err, jobstore.ErrMemoryCandidateDecided), errors.Is(err, job.ErrMemoryCandidateKeyExists):
		writeError(c, http.StatusConflict, what+" failed", err.Error())
	default:
		writeError(c, http.StatusInternalServerError, what+" failed", err.Error())
	}
}
