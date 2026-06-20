package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/store"
)

// maxLogTailBytes caps the response size of the log endpoints: only the last
// 256KB of a stream is returned (plan §9-P5/§11). Full logs remain inspectable
// on disk via the result dir.
const maxLogTailBytes = 256 * 1024

// Synchronous-submit wait caps (design §6.1 / P1-a): the server blocks at most
// defaultWaitSec when sync is requested without an explicit wait_timeout_sec,
// and never longer than maxWaitSec, after which it returns 202 + X-Gofer-Async.
const (
	defaultWaitSec = 30
	maxWaitSec     = 60
)

// handleCreateJob parses a JobRequest, submits it and returns the initial
// JobResult (with the assigned id). The body is JSON by default; a
// Content-Type of text/markdown (or application/x-gofer-md) is parsed as
// yaml-frontmatter + prose (design §6.2). When sync is requested (body.sync or
// ?wait=1) it blocks for the final JobResult (capped), else returns immediately.
// Validation failures map to a 404 (unknown project) or 400 (anything else) via
// the job package sentinels.
func (s *Server) handleCreateJob(c *rux.Context) {
	var req job.JobRequest
	ct := c.Req.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "text/markdown") || strings.HasPrefix(ct, "application/x-gofer-md") {
		raw, _ := io.ReadAll(c.Req.Body)
		parsed, err := parseMarkdownRequest(raw)
		if err != nil {
			writeError(c, http.StatusBadRequest, "invalid markdown request", err.Error())
			return
		}
		// md prose maps to prompt (cli-agents only); exec wants argv via JSON cmd.
		if parsed.Agent == "exec" {
			writeError(c, http.StatusBadRequest, "markdown submit is for cli-agents", "use JSON + cmd for exec agents")
			return
		}
		req = parsed
	} else if err := c.BindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}

	// Stamp the authenticated caller id, overriding any client-supplied value
	// (anti-spoof): the identity is the server's auth decision, not the body.
	req.CallerID = callerFromCtx(c)

	res, err := s.jobs.Submit(req)
	if err != nil {
		writeError(c, submitStatus(err), "job rejected", err.Error())
		return
	}

	// Synchronous submit: block until terminal (capped). An already-terminal
	// result (e.g. an idempotent hit on a finished job) returns immediately.
	if wantSync(c, req) && !job.IsTerminal(res.Status) {
		if final, ok := s.jobs.WaitFor(res.ID, clampWait(req.WaitTimeoutSec)); ok {
			c.JSON(http.StatusOK, final)
			return
		}
		// Exceeded the server wait cap and still not terminal: fall back to async
		// semantics. The job keeps running; the client should switch to polling.
		c.SetHeader("X-Gofer-Async", "1")
		c.JSON(http.StatusAccepted, res)
		return
	}
	c.JSON(http.StatusOK, res)
}

// wantSync reports whether the request asked for synchronous submit, via the
// body sync field or a ?wait=1 / ?wait=true query param.
func wantSync(c *rux.Context, req job.JobRequest) bool {
	return req.Sync || c.Query("wait") == "1" || c.Query("wait") == "true"
}

// clampWait turns a requested wait_timeout_sec into a duration, applying the
// default (when 0/negative) and the hard server cap.
func clampWait(sec int) time.Duration {
	if sec <= 0 {
		sec = defaultWaitSec
	}
	if sec > maxWaitSec {
		sec = maxWaitSec
	}
	return time.Duration(sec) * time.Second
}

// submitStatus maps a Submit error to an HTTP status: an unknown project is a
// 404 (consistent with GET /v1/projects/{key}); no eligible worker for the
// requested labels is a 503 (transient — retry / pick another); every other
// rejection is a 400.
func submitStatus(err error) int {
	if errors.Is(err, job.ErrUnknownProject) {
		return http.StatusNotFound
	}
	if errors.Is(err, job.ErrNoEligibleWorker) {
		return http.StatusServiceUnavailable
	}
	return http.StatusBadRequest
}

// handleListJobs returns job snapshots merged from the per-project index and
// the in-memory live state. Optional query params: status, project, caller,
// tag, agent, runner, since (unix 秒), limit. A non-numeric limit/since falls
// back to 0 (default/no-filter). The list is always a non-nil array, so an empty
// result serialises as {"jobs":[]}.
func (s *Server) handleListJobs(c *rux.Context) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	// since 非数值 -> 0 -> 不过滤（仿 limit 容错）。
	since, _ := strconv.ParseInt(c.Query("since"), 10, 64)
	list, err := s.jobs.ListJobs(job.ListOpts{
		Project: c.Query("project"),
		Status:  c.Query("status"),
		Caller:  c.Query("caller"),
		Tag:     c.Query("tag"),
		Agent:   c.Query("agent"),
		Runner:  c.Query("runner"),
		Since:   since,
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

// handleGetJobRequest returns the original JobRequest a job was created from
// (P2-b), read from the persisted jobs.request_json column. It exists for the
// CLI `job rerun` path (re-submit the same request with a fresh idempotency key)
// and audit. The request_json is intentionally NOT part of handleGetJob's
// response (it would bloat list responses, D1), so this is a dedicated endpoint.
// An unknown id or a job with no recorded request is a 404. The stored bytes are
// already a valid JobRequest JSON object, so they are echoed verbatim
// (json.RawMessage) without a re-marshal round-trip.
func (s *Server) handleGetJobRequest(c *rux.Context) {
	id := c.Param("id")
	res, ok := s.jobs.Get(id)
	if !ok {
		writeError(c, http.StatusNotFound, "unknown job", "no job with id "+id)
		return
	}
	if res.RequestJSON == "" {
		writeError(c, http.StatusNotFound, "no request recorded", "job "+id+" has no stored request")
		return
	}
	c.JSON(http.StatusOK, json.RawMessage(res.RequestJSON))
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
