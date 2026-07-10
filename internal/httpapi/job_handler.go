package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/store"
)

// maxLogTailBytes caps the legacy ?bytes= log endpoint: only the last 256KB of
// a stream is returned unless the caller asks for a smaller byte tail. Full logs
// remain inspectable on disk via the result dir.
const maxLogTailBytes = 256 * 1024

const defaultLogLines = 200

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

	// Submission provenance: the client IP is authoritative (the server observes
	// it), so fill Client when the submitter did not provide one — e.g. the web
	// console, where a browser can't supply a hostname. The CLI stamps its own
	// os.Hostname() into Client, which we keep. Channel stays client-declared
	// (cli/web/mcp), informational (see JobRequest.Channel/Client).
	if req.Client == "" {
		req.Client = clientIP(c)
	}

	// The wantSync decision (?wait=1 / ?wait=true query param) is an HTTP-transport
	// concern; the submit + sync-wait + clamp + async-fallback policy lives in
	// job.Service.SubmitSync. The handler only maps the outcome to HTTP表现.
	res, async, err := s.jobs.SubmitSync(req, wantSync(c, req))
	if err != nil {
		writeError(c, submitStatus(err), "job rejected", err.Error())
		return
	}
	if async {
		// Exceeded the server wait cap and still not terminal: fall back to async
		// semantics (202 + X-Gofer-Async). The job keeps running; the client polls.
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

// clientIP returns the originating client IP for submission provenance: the first
// X-Forwarded-For hop when present (web console behind a proxy), else the
// RemoteAddr host with the port stripped. Best-effort — returns RemoteAddr
// verbatim if it cannot be split.
func clientIP(c *rux.Context) string {
	if xff := c.Req.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(c.Req.RemoteAddr)
	if err != nil {
		return c.Req.RemoteAddr
	}
	return host
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
// tag, agent, runner, session, plan, since (unix 秒), limit, offset. A non-numeric limit/since/
// offset falls back to 0 (default/no-filter). The list is always a non-nil array,
// so an empty result serialises as {"jobs":[]}。
func (s *Server) handleListJobs(c *rux.Context) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	offset, _ := strconv.Atoi(c.Query("offset"))
	// since 非数值 -> 0 -> 不过滤（仿 limit 容错）。
	since, _ := strconv.ParseInt(c.Query("since"), 10, 64)
	list, err := s.jobs.ListJobs(job.ListOpts{
		Project: c.Query("project"),
		Status:  c.Query("status"),
		Caller:  c.Query("caller"),
		Tag:     c.Query("tag"),
		Agent:   c.Query("agent"),
		Runner:  c.Query("runner"),
		Session: c.Query("session"),
		Plan:    c.Query("plan"),
		Since:   since,
		Limit:   limit,
		Offset:  offset,
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
	c.JSON(http.StatusOK, jobDetailView{
		JobResult: res,
		// can_attach 是详情视图计算位；列表端点保持原 JobResult 数组不变。
		CanAttach: s.canAttachNow(callerFromCtx(c), res),
	})
}

type jobDetailView struct {
	job.JobResult
	CanAttach bool `json:"can_attach"`
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

// handleJobLogsStdout / handleJobLogsStderr return a tail window of a job's log
// stream. By default the newest defaultLogLines lines are returned; ?bytes=
// preserves the old byte-tail contract for non-Web callers. Output is text/plain.
func (s *Server) handleJobLogsStdout(c *rux.Context) { s.serveLog(c, store.StreamStdout) }
func (s *Server) handleJobLogsStderr(c *rux.Context) { s.serveLog(c, store.StreamStderr) }

// serveLog reads the requested log window for the job and writes it as
// text/plain. It locates the job's result dir from its JobResult (ResultDir ==
// <base>/<job_id>), so the FileStore base is its parent dir.
func (s *Server) serveLog(c *rux.Context, stream store.Stream) {
	id := c.Param("id")
	res, ok := s.jobs.Get(id)
	if !ok {
		writeError(c, http.StatusNotFound, "unknown job", "no job with id "+id)
		return
	}

	base := filepath.Dir(res.ResultDir)
	fs := store.NewFileStore(base)

	if c.Query("bytes") != "" {
		maxBytes := parseLogBytes(c.Query("bytes"))
		data, err := fs.ReadLogTail(id, stream, maxBytes)
		if err != nil {
			writeError(c, http.StatusInternalServerError, "read log failed", err.Error())
			return
		}
		c.Text(http.StatusOK, string(data))
		return
	}

	lines, offset := parseLogLineWindow(c)
	data, total, err := fs.ReadLogLines(id, stream, lines, offset)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "read log failed", err.Error())
		return
	}
	c.SetHeader("X-Log-Total-Lines", strconv.Itoa(total))
	c.SetHeader("X-Log-Offset", strconv.Itoa(offset))
	c.SetHeader("X-Log-Lines", strconv.Itoa(countResponseLines(data)))
	c.Text(http.StatusOK, string(data))
}

func parseLogBytes(raw string) int64 {
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 || n > maxLogTailBytes {
		return maxLogTailBytes
	}
	return n
}

func parseLogLineWindow(c *rux.Context) (lines int, offset int) {
	lines = defaultLogLines
	if c.Query("full") == "1" {
		return 0, 0
	} else if raw := c.Query("lines"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			lines = n
		}
	}
	if raw := c.Query("offset"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			offset = n
		}
	}
	return lines, offset
}

func countResponseLines(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	n := 0
	for _, b := range data {
		if b == '\n' {
			n++
		}
	}
	if data[len(data)-1] != '\n' {
		n++
	}
	return n
}

// resumeJobReq is the POST body for resuming a job's底层 agent 会话
// (session-capture P2). prompt is the new turn's text; runner is OPTIONAL and,
// when set, must equal the source job's runner (同 runner 约束) — a mismatch is a
// 400. An empty prompt is allowed (some agents accept a bare resume).
type resumeJobReq struct {
	Prompt string `json:"prompt"`
	Runner string `json:"runner,omitempty"`
}

// handleResumeJob starts a NEW job that续接 the source job's底层 agent CLI 会话
// (session-capture P2, design §5.2). The编排 lives in job.Service.ResumeJob
// (G021): the handler only parses the body, stamps the authenticated caller
// (anti-spoof, like handleCreateJob) and maps the job-package sentinels to a
// status. On success it returns the new job's JobResult (its session_id links
// back to the source session). Default async — the client watches the new job.
func (s *Server) handleResumeJob(c *rux.Context) {
	id := c.Param("id")
	var req resumeJobReq
	if err := c.BindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	res, err := s.jobs.ResumeJob(id, req.Prompt, req.Runner, callerFromCtx(c))
	if err != nil {
		writeError(c, resumeStatus(err), "resume rejected", err.Error())
		return
	}
	c.JSON(http.StatusOK, res)
}

// resumeStatus maps a ResumeJob error to an HTTP status: an unknown source job is
// a 404; the well-formed-but-not-permitted resume rejections (no captured
// session, agent without a resume template, cross-runner) are 400. Submit errors
// surfaced by the inner re-submit are mapped via submitStatus (unknown project →
// 404, etc.).
func resumeStatus(err error) int {
	if errors.Is(err, job.ErrUnknownJob) {
		return http.StatusNotFound
	}
	if errors.Is(err, job.ErrNoSession) ||
		errors.Is(err, job.ErrResumeUnsupported) ||
		errors.Is(err, job.ErrCrossRunner) {
		return http.StatusBadRequest
	}
	return submitStatus(err)
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
