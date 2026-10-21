package httpapi

import (
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/store"
	"github.com/inhere/gofer/internal/xfer"
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

	// A caller with a scope ceiling submits only into its projects; outside is "no such
	// project" (404, BodyProject). A submit that names no project is outside too.
	if !s.requireProjectScope(c, req.ProjectKey) {
		return
	}

	// Stamp the authenticated caller id, overriding any client-supplied value
	// (anti-spoof): the identity is the server's auth decision, not the body.
	req.CallerID = callerFromCtx(c)

	// JOB-06① 决策 4: a job credential may not switch the MANDATORY rules off. A job
	// is exactly the thing the discipline is meant to govern, so the caller that runs
	// under a job's own token has no say in it; a user caller may (`--no-rules`).
	// Checked first, before the submit permission gate: this is a refusal of the
	// REQUEST SHAPE, and reporting it as "you may not submit" would name the wrong
	// reason.
	if req.NoRules && callerKindFromCtx(c) == callerKindJob {
		writeError(c, http.StatusForbidden, "rules cannot be disabled by a job caller",
			"a job credential may not submit a job with no_rules; only a user caller may turn the rules off for one job")
		return
	}

	// SEC-01: a job credential may submit only under the narrow rule the design gives
	// it (member job, same project, an agent its own agent/role listed in
	// submit_agents) — and the accepted request is tagged with its origin. Checked
	// BEFORE anything is admitted, so a refused submit starts nothing.
	if !s.jobMaySubmit(c, &req) {
		return
	}
	if req.IssueID != "" && callerKindFromCtx(c) == callerKindJob {
		asking, ok := s.jobs.Get(callerFromCtx(c))
		if !ok || asking.IssueID != req.IssueID || asking.TrackerID != req.TrackerID {
			writeError(c, http.StatusForbidden, "issue link denied", "job caller may only link its associated issue")
			return
		}
	}
	if req.IssueID != "" && !s.trackerIssueExists(req.TrackerID, req.IssueID) {
		req.Tags = append(req.Tags, "tracker:issue:"+req.IssueID)
	}

	// The CEILING OWNER is decided here, at the entry, and never by the body: a user
	// submits as themselves, a job credential submits as the owner of the job it belongs
	// to (design 2026-10-18, 上限所有者). Everything downstream — the persisted row, a
	// retry replay, a continuation, a failover — inherits this value. A job credential
	// whose job cannot be read is refused (fail closed, 403): admitting it as a system job
	// would let a restricted caller's chain submit unchecked.
	//
	// It runs AFTER the request-shape refusals above on purpose: those answer "this
	// request is malformed" (no_rules from a job caller), and naming the ceiling instead
	// would send the caller after the wrong thing.
	owner, err := s.originCallerFromCtx(c)
	if err != nil {
		writeError(c, http.StatusForbidden, "caller ceiling", err.Error())
		return
	}
	req.OriginCaller = owner

	// Submission provenance: the client IP is authoritative (the server observes
	// it), so fill Client when the submitter did not provide one — e.g. the web
	// console, where a browser can't supply a hostname. The CLI stamps its own
	// os.Hostname() into Client, which we keep. Channel stays client-declared
	// (cli/web/mcp), informational (see JobRequest.Channel/Client).
	if req.Client == "" {
		req.Client = clientIP(c)
	}

	// XFER-01 X2: a job's uploads must name transfers ALREADY staged on this server.
	// A client-side path is not accepted (the server never reads the submitter's
	// filesystem), and neither is another project's transfer: the request carries ids
	// whose payloads are already here and already verified.
	if status, msg, detail := s.validateJobUploads(req); status != 0 {
		writeError(c, status, msg, detail)
		return
	}

	// The wantSync decision (?wait=1 / ?wait=true query param) is an HTTP-transport
	// concern; the submit + sync-wait + clamp + async-fallback policy lives in
	// job.Service.SubmitSync. The handler only maps the outcome to HTTP表现.
	res, async, err := s.jobs.SubmitSync(req, wantSync(c, req))
	if err != nil {
		if errors.Is(err, job.ErrNotifyTargetRejected) {
			c.JSON(submitStatus(err), errorBody{Error: "job rejected", Detail: err.Error(), Code: job.ErrCodeNotifyTargetRejected})
			return
		}
		if errors.Is(err, job.ErrCeilingDenied) {
			// gofer-7798 第二期 E7: a submit refused by the caller's ceiling leaves an auth_audit row —
			// the 403 alone names nothing to whoever asks later why the job never appeared.
			s.recordCeilingDenied(c, ceilingDenial{route: "POST /v1/jobs", status: http.StatusForbidden, category: ceilingDenySubmit, text: err.Error()})
		}
		writeError(c, submitStatus(err), "job rejected", err.Error())
		return
	}
	if async {
		// Exceeded the server wait cap and still not terminal: fall back to async
		// semantics (202 + X-Gofer-Async). The job keeps running; the client polls.
		// A held job (gofer-9b1b) lands here at once: it waits for a person, and the
		// body says where that person approves it.
		c.SetHeader("X-Gofer-Async", "1")
		c.JSON(http.StatusAccepted, s.submitView(res))
		return
	}
	c.JSON(http.StatusOK, s.submitView(res))
}

// submitResponse is the create-job body: the job plus, for a job parked awaiting
// approval (gofer-9b1b), the console page a person approves it on. ApproveURL is
// empty when server.web_base_url is unset (the CLI then builds it from the address it
// talked to).
type submitResponse struct {
	job.JobResult
	ApproveURL string `json:"approve_url,omitempty"`
}

// submitView decorates a submit result for the response body.
func (s *Server) submitView(res job.JobResult) any {
	if res.Status != job.StatusAwaitingApproval {
		return res
	}
	return submitResponse{JobResult: res, ApproveURL: s.jobs.WebURL("/jobs/" + res.ID)}
}

// validateJobUploads checks a submit's uploads before anything is admitted: every
// entry names a transfer staged on THIS server, in the job's own project, still
// staged, and a put (XFER-01 X2). It returns a zero status when the request may
// proceed. The same conditions are re-checked when the bytes are actually placed
// (the job's bridge), because a transfer's state can change between the two — one is
// the API's contract, the other is the transfer's reality.
func (s *Server) validateJobUploads(req job.JobRequest) (int, string, string) {
	for _, up := range req.Uploads {
		if strings.TrimSpace(up.XferID) == "" || strings.TrimSpace(up.Dest) == "" {
			return http.StatusBadRequest, "invalid upload", "each upload needs an xfer_id (staged with POST /v1/xfer) and a dest"
		}
		if s.xfer == nil {
			return http.StatusServiceUnavailable, "xfer unavailable", "this server has no transfer manager wired"
		}
		rec, ok, err := s.xfer.Get(up.XferID)
		if err != nil {
			return http.StatusInternalServerError, "upload lookup failed", err.Error()
		}
		if !ok {
			return http.StatusBadRequest, "unknown upload",
				"no transfer with id " + up.XferID + "; stage it with POST /v1/xfer and pass the returned id"
		}
		if rec.Op != string(xfer.OpPut) {
			return http.StatusBadRequest, "wrong transfer direction",
				"transfer " + up.XferID + " is a get; a job upload must be a staged put"
		}
		if req.ProjectKey != "" && rec.ProjectKey != req.ProjectKey {
			return http.StatusBadRequest, "cross-project upload",
				"transfer " + up.XferID + " belongs to project " + rec.ProjectKey
		}
		if xfer.State(rec.State) != xfer.StateStaged {
			return http.StatusBadRequest, "upload not staged",
				"transfer " + up.XferID + " is " + rec.State
		}
	}
	return 0, "", ""
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
// requested labels is a 503 (transient — retry / pick another); a decision hook's
// refusal and a caller-ceiling refusal are 403 (a permission, not a malformed
// request); every other rejection is a 400.
func submitStatus(err error) int {
	if errors.Is(err, job.ErrUnknownProject) {
		return http.StatusNotFound
	}
	if errors.Is(err, job.ErrNoEligibleWorker) {
		return http.StatusServiceUnavailable
	}
	if errors.Is(err, job.ErrDecisionDenied) || errors.Is(err, job.ErrCeilingDenied) {
		return http.StatusForbidden
	}
	return http.StatusBadRequest
}

// handleListJobs returns job snapshots merged from the per-project index and
// the in-memory live state. Optional query params: status, project, caller,
// tag, agent, runner, session, plan, source_job, workflow_id, since (unix 秒), limit, offset. A non-numeric limit/since/
// offset falls back to 0 (default/no-filter). The list is always a non-nil array,
// so an empty result serialises as {"jobs":[]}。
func (s *Server) handleListJobs(c *rux.Context) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	offset, _ := strconv.Atoi(c.Query("offset"))
	// since 非数值 -> 0 -> 不过滤（仿 limit 容错）。
	since, _ := strconv.ParseInt(c.Query("since"), 10, 64)
	all := c.Query("all") == "1" || strings.EqualFold(c.Query("all"), "true")
	caller := c.Query("caller")
	if !s.mayListAllJobs(c) {
		// 过滤型（设计 §1）：没有 job.read.others 的调用方只看到自己的 job，不报 403。
		caller = callerFromCtx(c)
	}
	project := c.Query("project")
	var projects []string
	if project == "" {
		// A ceiling naming exactly one literal project is pushed into the query, so the page
		// is cut AFTER the project filter.
		project = s.singleCeilingProject(c)
		if project == "" && s.ceilingActive(c) {
			// gofer-7798 第二期 E5: a ceiling naming several projects (or a glob, expanded to the
			// literal projects this server serves) goes into the query as a list, so limit / offset
			// cut the page AFTER the project filter instead of paginating the whole server first and
			// dropping the rows outside afterwards.
			projects = s.jobListScopeProjects(s.scopeFilter(c))
			if len(projects) == 0 {
				// The ceiling admits none of this server's projects: nothing is readable.
				c.JSON(http.StatusOK, map[string]any{"jobs": []job.JobResult{}})
				return
			}
		}
	}
	list, err := s.jobs.ListJobs(job.ListOpts{
		Project:    project,
		Projects:   projects,
		Status:     c.Query("status"),
		Caller:     caller,
		Tag:        c.Query("tag"),
		Agent:      c.Query("agent"),
		Runner:     c.Query("runner"),
		Session:    c.Query("session"),
		Plan:       c.Query("plan"),
		SourceJob:  c.Query("source_job"),
		WorkflowID: c.Query("workflow_id"),
		Since:      since,
		Limit:      limit,
		Offset:     offset,
		All:        all,
	})
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list jobs failed", err.Error())
		return
	}
	// Filter: a restricted caller sees the jobs of its projects only.
	list = scopeRows(s, c, list, func(j job.JobResult) string { return j.ProjectKey })
	c.JSON(http.StatusOK, map[string]any{"jobs": list})
}

// jobListScopeProjects expands a project scope into the literal keys the jobs query is pushed down
// with. On top of the projects the server DECLARES it adds the projects a worker serves: a job can
// belong to a project that exists only on a worker (no `projects:` entry in the server config), and
// a glob ceiling such as `p*` admits it — the pushdown must not drop it. Only keys the scope admits
// are added, so the list never widens. The project of a worker is what it reports while connected
// (`/v1/workers/{id}/projects`) and keeps being served after a reconnect.
func (s *Server) jobListScopeProjects(scope func(project string) bool) []string {
	out := s.scopedProjectKeys(scope)
	seen := make(map[string]struct{}, len(out))
	for _, p := range out {
		seen[p] = struct{}{}
	}
	add := func(p string) {
		if p == "" || !scope(p) {
			return
		}
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	cfg := s.serverCfg()
	if cfg == nil || s.workers == nil {
		return out
	}
	for id := range cfg.Workers {
		if ws, ok := s.workers.WorkerStatus(id); ok {
			for _, p := range ws.Projects {
				add(p)
			}
		}
	}
	return out
}

// canDeleteJob is the can_delete display bit of the job detail view: the job has ended
// and the caller may delete it — the caller's own job, or job.delete.others (the same
// rule DELETE /v1/jobs/{id} applies, plan T1.5a).
func (s *Server) canDeleteJob(c *rux.Context, res job.JobResult) bool {
	terminal := res.Status == job.StatusDone || res.Status == job.StatusFailed || res.Status == job.StatusCancelled || res.Status == job.StatusTimeout || res.Status == job.StatusRejected
	return terminal && s.mayDeleteJob(c, res)
}

// handleGetJobRequest returns the SECRET-STRIPPED JobRequest a job was created from
// (P5, closes h-aii-xqe1). It no longer echoes request_json verbatim — the only verbatim
// consumer (CLI rerun) moved server-side to POST /jobs/{id}/rebuild. env values / secret-
// looking prompt/cmd/cwd/system_prompt are redacted (job.RedactedRequest); when anything
// was stripped an X-Gofer-Redacted: 1 header is set (workflow-export parity). It backs the
// web rebuild PREFILL (display only). Unknown id / no request → 404.
func (s *Server) handleGetJobRequest(c *rux.Context) {
	id := c.Param("id")
	if res, ok := s.jobForAccess(id); ok && !s.mayReadJob(c, res) {
		s.denyJobAction(c, authz.PermJobReadOthers)
		return
	}
	req, ok, redacted, err := s.jobs.RedactedRequest(id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "read request failed", err.Error())
		return
	}
	if !ok {
		writeError(c, http.StatusNotFound, "no request recorded", "job "+id+" has no stored request")
		return
	}
	if redacted {
		c.SetHeader("X-Gofer-Redacted", "1")
	}
	c.JSON(http.StatusOK, req)
}

// handleJobLogsStdout / handleJobLogsStderr return a tail window of a job's log
// stream. By default the newest defaultLogLines lines are returned; ?bytes=
// preserves the old byte-tail contract for non-Web callers. Output is text/plain.
func (s *Server) handleJobLogsStdout(c *rux.Context) { s.serveLog(c, store.StreamStdout) }
func (s *Server) handleJobLogsStderr(c *rux.Context) { s.serveLog(c, store.StreamStderr) }

// serveLog reads the requested log window for the job and writes it as
// text/plain. It locates the job's result dir from its JobResult (ResultDir ==
// <base>/<job_id>), so the FileStore base is its parent dir.
//
// An interactive job's pty output never reaches stdout.log (the pty runner owns
// that stream), so a stdout request falls back to the de-ANSI'd pty.txt
// transcript (PTY-01 §四) — `job logs` and the web log page then show the same
// text the TUI printed instead of an empty pane.
func (s *Server) serveLog(c *rux.Context, stream store.Stream) {
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
	if stream == store.StreamStdout && res.Interactive && !hasStdoutLog(res.ResultDir) {
		stream = store.StreamPty
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
	if c.Query("head") == "1" {
		if c.Query("offset") != "" {
			writeError(c, http.StatusBadRequest, "invalid log window", "head and offset are mutually exclusive")
			return
		}
		lines, _ := strconv.Atoi(c.Query("lines"))
		if lines <= 0 {
			lines = defaultLogLines
		}
		data, total, err := fs.ReadLogHead(id, stream, lines)
		if err != nil {
			writeError(c, http.StatusInternalServerError, "read log failed", err.Error())
			return
		}
		c.SetHeader("X-Log-Total-Lines", strconv.Itoa(total))
		c.SetHeader("X-Log-Lines", strconv.Itoa(countResponseLines(data)))
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

// hasStdoutLog reports whether the job left a non-empty stdout.log: an
// interactive job has none (or an empty placeholder), and only then is the pty
// transcript the right thing to serve.
func hasStdoutLog(resultDir string) bool {
	fi, err := os.Stat(filepath.Join(resultDir, store.StdoutFile))
	return err == nil && fi.Size() > 0
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
//
// mode (optional) picks the continuation form — session (resident ACP), interactive
// (pty) or batch (one-shot `--resume -p`); empty keeps the form the source job
// implies. agent (optional) continues with another agent of the SAME session family
// (e.g. claude-acp <-> claude). Unsupported combinations are a 400.
type resumeJobReq struct {
	Prompt string `json:"prompt"`
	Runner string `json:"runner,omitempty"`
	Mode   string `json:"mode,omitempty"`
	Agent  string `json:"agent,omitempty"`
	// Model overrides the model inherited from the source job (N1 §B).
	Model string `json:"model,omitempty"`
	// Budget overrides, per dimension, the budget inherited from the source job (N2 §B).
	Budget *job.Budget `json:"budget,omitempty"`
	// Env is the continuation's own explicit env (overrides what it inherits from the
	// source agent / source job; recorded in the new request like any submit env).
	Env map[string]string `json:"env,omitempty"`
	// PlanID overrides the plan the continuation is grouped under: absent keeps the
	// source job's plan, an id moves the continuation to that plan, and an explicit
	// empty string detaches it (the CLI's `--no-plan`).
	PlanID *string `json:"plan_id,omitempty"`
	// NoNotify drops the end-notification the continuation otherwise inherits from its
	// source (gofer-3db4; the CLI's `job resume --no-notify`).
	NoNotify bool `json:"no_notify,omitempty"`
	// LockPaths narrows (or moves) the directory lock the continuation would inherit
	// from its source (gofer-jkd2; the CLI's `job resume --lock`). Omitted / empty keeps
	// the source's declaration — a resume cannot clear a lock (see job.ResumeOptions).
	LockPaths []string `json:"lock_paths,omitempty"`
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
	if res, ok := s.jobForAccess(id); ok && !s.mayManageJob(c, res, true) {
		s.denyJobAction(c, authz.PermJobManageOthers, authz.PermJobSubmit)
		return
	}
	res, err := s.jobs.ResumeJobWith(id, req.Prompt, req.Runner, callerFromCtx(c), job.ResumeOptions{Mode: req.Mode, Agent: req.Agent, Model: req.Model, Budget: req.Budget, Env: req.Env, Plan: req.PlanID, NoNotify: req.NoNotify, LockPaths: req.LockPaths})
	if err != nil {
		writeError(c, resumeStatus(err), "resume rejected", err.Error())
		return
	}
	c.JSON(http.StatusOK, res)
}

// handleRebuildJob starts a NEW job from a source job's request + the caller's edits
// (P5, D1). 编排 in job.Service.RebuildJob (G021); the handler binds the overrides, stamps
// the authenticated caller + client IP (anti-spoof, like handleCreateJob) and maps
// sentinels. Default async — the client watches the new job. env plaintext is never in the
// request or response (only env_set/env_unset carry NEW values in).
func (s *Server) handleRebuildJob(c *rux.Context) {
	id := c.Param("id")
	var ov job.RebuildOverrides
	if err := c.BindJSON(&ov); err != nil && !errors.Is(err, io.EOF) {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if res, ok := s.jobForAccess(id); ok && !s.mayManageJob(c, res, true) {
		s.denyJobAction(c, authz.PermJobManageOthers, authz.PermJobSubmit)
		return
	}
	res, err := s.jobs.RebuildJob(id, ov, callerFromCtx(c), clientIP(c))
	if err != nil {
		writeError(c, rebuildStatus(err), "rebuild rejected", err.Error())
		return
	}
	c.JSON(http.StatusOK, res)
}

// rebuildStatus: unknown source job → 404; a lingering placeholder → 400; inner Submit
// errors (unknown project etc.) via submitStatus.
func rebuildStatus(err error) int {
	if errors.Is(err, job.ErrUnknownJob) {
		return http.StatusNotFound
	}
	if errors.Is(err, job.ErrJobNotTerminal) {
		return http.StatusBadRequest
	}
	if errors.Is(err, job.ErrRedactedPlaceholder) {
		return http.StatusBadRequest
	}
	return submitStatus(err)
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
	if errors.Is(err, job.ErrJobNotTerminal) ||
		errors.Is(err, job.ErrNoSession) ||
		errors.Is(err, job.ErrResumeUnsupported) ||
		errors.Is(err, job.ErrCrossRunner) {
		return http.StatusBadRequest
	}
	return submitStatus(err)
}

// handleCancelJob requests cancellation. Cancel is a stable no-op for an already
// terminal job (returns the current snapshot), a 409 for a job that has finished but
// is not terminal yet (needs_review: the caller wants reject), and a 404 for an
// unknown id.
func (s *Server) handleCancelJob(c *rux.Context) {
	id := c.Param("id")
	if res, ok := s.jobForAccess(id); ok && !s.mayCancelJob(c, res) {
		s.denyJobAction(c, authz.PermJobCancelOthers)
		return
	}
	if err := s.jobs.Cancel(id); err != nil {
		if errors.Is(err, job.ErrJobNotRunning) {
			writeError(c, http.StatusConflict, "job is not running", err.Error())
			return
		}
		// The only other Cancel error is an unknown job id (terminal jobs are no-ops).
		writeError(c, http.StatusNotFound, "unknown job", err.Error())
		return
	}
	res, _ := s.jobs.Get(id)
	c.JSON(http.StatusOK, res)
}

// reviewJobReq is the optional body of POST /jobs/{id}/accept|reject|approve: the note
// is the human's reason (required for a reject of a delivery, optional for an accept, an
// approval or a reject of a held job) and, with resume, the continuation's prompt.
type reviewJobReq struct {
	Note string `json:"note,omitempty"`
	// Reason is accepted as a synonym of Note (gofer-9b1b: "reject a held job with a
	// reason" reads naturally that way); Note wins when both are given.
	Reason string `json:"reason,omitempty"`
	// AsJob is the retired in-job identity field (MCP-05 阶段 B used it to tell a
	// LEADER job apart from the human it ran beside). A leader can no longer ask for a
	// verdict at all: it authenticates with its own job credential, and the SEC-01
	// permission table refuses accept/reject for every job caller before the handler
	// runs — a stronger rule than the field ever expressed, because it does not depend
	// on what the body claims. The field stays accepted-and-ignored so an old client
	// does not get a 400.
	//
	// DEPRECATED(v0.58): remove in v0.61
	AsJob string `json:"as_job,omitempty"`
	// Resume asks a REJECT to continue the work: a new job is started with the note as
	// its prompt. Ignored by accept.
	Resume bool `json:"resume,omitempty"`
	// NoDeliver accepts without delivering the worktree branch (GIT-02). Ignored by reject.
	NoDeliver bool `json:"no_deliver,omitempty"`
}

// handleAcceptJob records a human's acceptance of a needs_review job (→done). The
// orchestration lives in job.Service.AcceptJob (G021); the handler only enforces the
// human-only rule, binds the optional note and maps the job-package sentinels.
func (s *Server) handleAcceptJob(c *rux.Context) {
	caller, ok := s.jobReviewer(c, "accept")
	if !ok {
		return
	}
	req, ok := bindReviewJobReq(c)
	if !ok {
		return
	}
	accept := s.jobs.AcceptJob
	if req.NoDeliver {
		accept = s.jobs.AcceptJobWithoutDelivery
	}
	res, err := accept(c.Param("id"), caller, req.Note)
	if err != nil {
		writeError(c, reviewStatus(err), "accept failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, res)
}

// handleRejectJob records a human's rejection of a needs_review job (→ the terminal
// rejected) and, with resume, starts the continuation the note asks for. The rejection
// itself is durable even when the continuation cannot be started — the error then
// reports only that (job.Service.RejectJob).
func (s *Server) handleRejectJob(c *rux.Context) {
	caller, ok := s.jobReviewer(c, "reject")
	if !ok {
		return
	}
	req, ok := bindReviewJobReq(c)
	if !ok {
		return
	}
	// A resume starts a NEW job, so it is a job.submit entry on top of the review
	// verdict (catalog: `reject?resume=true`). Checked before RejectJob: the rejection
	// itself must not be recorded for a caller that may not start the continuation.
	if req.Resume && !s.can(c, authz.PermJobSubmit) {
		s.denyJobAction(c, authz.PermJobSubmit)
		return
	}
	res, err := s.jobs.RejectJob(c.Param("id"), caller, req.Note, req.Resume)
	if err != nil {
		writeError(c, reviewStatus(err), "reject failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, res)
}

// handleApproveJob records a human's approval of a job awaiting approval (gofer-9b1b):
// the job moves to queued and runs under the same id. Same caller rule as accept /
// reject (jobReviewer; a job credential never gets here — the permission table
// refuses it). The orchestration is job.Service.ApproveJob (G021).
func (s *Server) handleApproveJob(c *rux.Context) {
	caller, ok := s.jobReviewer(c, "approve")
	if !ok {
		return
	}
	req, ok := bindReviewJobReq(c)
	if !ok {
		return
	}
	res, err := s.jobs.ApproveJob(c.Param("id"), caller, req.Note)
	if err != nil {
		writeError(c, reviewStatus(err), "approve failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, res)
}

// jobReviewer enforces the GATE-01 S3 rule that only a PERSON signs off a delivery: a
// worker caller (an executing machine, and in practice the agent's own runtime) is refused
// outright, and the caller must hold `job.review` — the permission the design gives the
// answer group (with governance.require_answer_capability on, only a caller declaring
// can_answer is derived it). It writes the 403 and returns ok=false when the caller may not
// review. It is the caller-kind half of the pre-migration review check, kept in the handler because
// the permission model says nothing about a non-user caller (design §2).
//
// gofer-7798 第二期 (计划决策 6) adds two rules on top, both of which need the JOB, so the
// routes are InHandler instead of Require:
//   - reviewing SOMEONE ELSE's job (the job's CallerID is not the caller) also needs
//     job.review.others; one's own delivery still needs only job.review;
//   - a caller under a project ceiling may not review a job whose ceiling owner
//     (OriginCaller) is the caller itself — that would be the robot approving its own work —
//     and gets a 403 naming the reason. A caller without a ceiling keeps the old behaviour.
//
// An unknown job passes here on purpose: the orchestration keeps its own 404 / sentinel.
//
// A job caller never reaches here: jobCredentialMiddleware refuses accept/reject/approve for
// any job credential, which is the SEC-01 replacement for the old "a LEADER job may not
// accept" check (that one had to trust the request body; this one cannot be talked
// around).
func (s *Server) jobReviewer(c *rux.Context, action string) (string, bool) {
	if callerKindFromCtx(c) == callerKindWorker {
		writeError(c, http.StatusForbidden, action+" not permitted for this caller",
			"worker tokens cannot "+action+" a job: only a human makes this decision")
		return "", false
	}
	if !s.jobMayReview(c) {
		s.denyJobAction(c, authz.PermJobReview)
		return "", false
	}
	caller := callerFromCtx(c)
	if res, ok := s.jobForAccess(c.Param("id")); ok {
		if res.CallerID != caller && !s.can(c, authz.PermJobReviewOthers) {
			s.denyJobAction(c, authz.PermJobReviewOthers)
			return "", false
		}
		if s.ceilingActive(c) && res.OriginCaller == caller {
			writeError(c, http.StatusForbidden, action+" denied",
				"a caller with a project ceiling may not "+action+" a job its own ceiling owns: "+
					"someone else has to review it")
			return "", false
		}
	}
	return caller, true
}

// bindReviewJobReq binds the optional review body. An empty body is legal (accept with
// no note): the JSON binder reports io.EOF for it, which is not an error here.
func bindReviewJobReq(c *rux.Context) (reviewJobReq, bool) {
	var req reviewJobReq
	if err := c.BindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return reviewJobReq{}, false
	}
	if strings.TrimSpace(req.Note) == "" {
		req.Note = req.Reason
	}
	return req, true
}

// reviewStatus maps an accept/reject/approve error to an HTTP status: an unknown job is
// 404; "not awaiting review", "not awaiting approval", a held request that drifted and
// "not running" (cancel of a needs_review job) are 409 — the job exists but is in the
// wrong state for the request; a draining server is 503; a missing reject note is a
// 400; a continuation that could not start keeps the caller's attention on the request
// (400) while the rejection itself already stands.
func reviewStatus(err error) int {
	switch {
	case errors.Is(err, job.ErrUnknownJob):
		return http.StatusNotFound
	case errors.Is(err, job.ErrJobNotNeedsReview), errors.Is(err, job.ErrJobNotRunning),
		errors.Is(err, job.ErrJobNotAwaitingApproval), errors.Is(err, job.ErrHoldDrift):
		return http.StatusConflict
	case errors.Is(err, job.ErrReviewNoteRequired):
		return http.StatusBadRequest
	case errors.Is(err, job.ErrUpgradeDraining):
		// The server is draining for an upgrade: the held job keeps waiting, retry later.
		return http.StatusServiceUnavailable
	default:
		return submitStatus(err)
	}
}

// originCallerFromCtx decides the CEILING OWNER of a submitted job (design
// 2026-10-18, 上限所有者): the user-class caller itself, or — for a job credential —
// the owner of the job that credential belongs to. It is the only place the value is
// decided on the HTTP path; a body-supplied origin_caller is ignored.
//
// A job credential whose job row cannot be read (gone, or a store error) is FAIL CLOSED:
// the error is ErrCeilingDenied and the caller answers 403, because admitting "no owner"
// would turn a restricted caller's derived chain into an unchecked system job.
func (s *Server) originCallerFromCtx(c *rux.Context) (string, error) {
	id := callerFromCtx(c)
	if callerKindFromCtx(c) != callerKindJob {
		return id, nil
	}
	return s.jobs.CredentialCeilingOwner(id)
}
