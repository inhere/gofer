package httpapi

import (
	"errors"
	"net/http"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/streaming"
)

// handleJobTranscript serves GET /v1/jobs/{id}/transcript (gofer-mmh0): one page of the
// job's per-turn transcript. Permission is the same as the job's logs (InHandler deny,
// job.read_others on someone else's job). The query selects exactly one mode —
// outline (default), turns=A-B, head=N, tail=N, search=<text> — and cursor=<turn:offset>
// continues a page the response marked truncated. The server writes no file: the CLI's
// --out is a client-side export.
func (s *Server) handleJobTranscript(c *rux.Context) {
	id := c.Param("id")
	res, ok := s.jobs.Get(id)
	if !ok {
		writeError(c, http.StatusNotFound, "unknown job", "no job with id "+id)
		return
	}
	if !s.mayReadJob(c, res) {
		s.denyJobAction(c, authz.PermJobReadOthers)
		return
	}
	outline := c.Query("outline")
	opts, err := streaming.ParseTranscriptQuery(streaming.TranscriptQuery{
		Outline: outline == "1" || outline == "true",
		Turns:   c.Query("turns"),
		Head:    c.Query("head"),
		Tail:    c.Query("tail"),
		Search:  c.Query("search"),
		Cursor:  c.Query("cursor"),
	})
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid transcript request", err.Error())
		return
	}
	page, err := streaming.JobTranscript(res, opts)
	switch {
	case errors.Is(err, streaming.ErrTranscriptPath):
		writeError(c, http.StatusForbidden, "transcript unavailable", "the job's record resolves outside its result directory")
		return
	case err != nil:
		writeError(c, http.StatusInternalServerError, "read transcript failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, page)
}
