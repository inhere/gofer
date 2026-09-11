// Package client is a thin HTTP client for the gofer control plane.
// It mirrors the /v1/jobs API (plan §7) and reuses the job package's
// JobRequest/JobResult structs as the wire types so the CLI (P6) and the MCP
// server (P8) share one transport.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/presence"
	"github.com/inhere/gofer/internal/tunnel"
)

// TunnelError reports an HTTP tunnel refusal.
type TunnelError struct {
	Status int
	Msg    string
}

func (e *TunnelError) Error() string { return fmt.Sprintf("tunnel: HTTP %d: %s", e.Status, e.Msg) }

type TunnelInfo struct {
	ID           string    `json:"id"`
	CallerID     string    `json:"caller_id"`
	WorkerID     string    `json:"worker_id"`
	Target       string    `json:"target"`
	ClientRemote string    `json:"client_remote"`
	StartedAt    time.Time `json:"started_at"`
	BytesUp      int64     `json:"bytes_up"`
	BytesDown    int64     `json:"bytes_down"`
}

// DialTunnel opens a worker TCP tunnel websocket.
func (c *Client) DialTunnel(ctx context.Context, workerID, target string) (*websocket.Conn, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme == "http" {
		u.Scheme = "ws"
	} else if u.Scheme == "https" {
		u.Scheme = "wss"
	}
	u.Path = tunnel.ConnectPath
	q := u.Query()
	q.Set("worker", workerID)
	q.Set("target", target)
	u.RawQuery = q.Encode()
	h := http.Header{}
	if c.token != "" {
		h.Set("Authorization", "Bearer "+c.token)
	}
	ws, resp, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{HTTPHeader: h, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		if resp != nil {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			return nil, &TunnelError{Status: resp.StatusCode, Msg: strings.TrimSpace(string(b))}
		}
		return nil, err
	}
	ws.SetReadLimit(tunnel.ReadLimit)
	return ws, nil
}

// ListTunnels lists active tunnels.
func (c *Client) ListTunnels() ([]TunnelInfo, error) {
	var out struct {
		Tunnels []TunnelInfo `json:"tunnels"`
	}
	err := c.doJSON(http.MethodGet, "/v1/tunnels", nil, &out)
	return out.Tunnels, err
}

// Client talks to a running gofer server. It is safe for sequential use;
// the zero value is not usable — construct it with New.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New builds a Client for baseURL with an optional bearer token. baseURL is
// normalised (scheme added, 0.0.0.0 rewritten to 127.0.0.1) via NormalizeBaseURL
// so callers may pass a bare `host:port`. When token is empty no Authorization
// header is sent (the server must then allow empty-token auth).
func New(baseURL, token string) *Client {
	return &Client{
		baseURL: NormalizeBaseURL(baseURL),
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// NormalizeBaseURL makes a user-supplied server address connectable:
//   - a bare `host:port` (or `host`) gets an `http://` scheme;
//   - a `0.0.0.0` host (the default listen address, not a connectable address)
//     is rewritten to `127.0.0.1`;
//   - a trailing slash is trimmed.
func NormalizeBaseURL(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	if !strings.Contains(addr, "://") {
		addr = "http://" + addr
	}
	if u, err := url.Parse(addr); err == nil {
		if u.Hostname() == "0.0.0.0" {
			port := u.Port()
			u.Host = "127.0.0.1"
			if port != "" {
				u.Host += ":" + port
			}
			addr = u.String()
		}
	}
	return strings.TrimRight(addr, "/")
}

// SubmitResult wraps the create-job response. Async is true when the server
// could not finish a synchronous submit within its wait cap and returned 202 +
// X-Gofer-Async (the job keeps running); the caller should poll. For a plain
// async submit (no sync requested) the server returns 200 and Async is false.
type SubmitResult struct {
	Job   job.JobResult
	Async bool
}

// SubmitJob POSTs a JobRequest to /v1/jobs and returns the initial JobResult
// (with the assigned id and queued/running status). It ignores the 202/async
// distinction; use SubmitJobSync when that matters.
func (c *Client) SubmitJob(req job.JobRequest) (job.JobResult, error) {
	out, err := c.SubmitJobSync(req)
	return out.Job, err
}

// SubmitJobSync POSTs a JobRequest as JSON and reports whether the server fell
// back to async (202 + X-Gofer-Async) so a sync caller can switch to polling.
func (c *Client) SubmitJobSync(req job.JobRequest) (SubmitResult, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return SubmitResult{}, fmt.Errorf("encode job request: %w", err)
	}
	return c.submit("application/json", bytes.NewReader(body))
}

// SubmitMarkdown POSTs a md+yaml document (frontmatter + prose) to /v1/jobs with
// Content-Type text/markdown so the server parses it into a JobRequest (design
// §6.2). Like SubmitJobSync it surfaces the 202/async fallback.
func (c *Client) SubmitMarkdown(body []byte) (SubmitResult, error) {
	return c.submit("text/markdown", bytes.NewReader(body))
}

// submit performs the create-job POST with an explicit content type and decodes
// the JobResult, flagging the 202 async-fallback case.
func (c *Client) submit(contentType string, body io.Reader) (SubmitResult, error) {
	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/v1/jobs", body)
	if err != nil {
		return SubmitResult{}, fmt.Errorf("build request: %w", err)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := c.http.Do(req)
	if err != nil {
		return SubmitResult{}, fmt.Errorf("request POST /v1/jobs: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return SubmitResult{}, fmt.Errorf("read response: %w", err)
	}
	if err := errorFor(resp.StatusCode, data); err != nil {
		return SubmitResult{}, err
	}
	var out SubmitResult
	if err := json.Unmarshal(data, &out.Job); err != nil {
		return SubmitResult{}, fmt.Errorf("decode response: %w", err)
	}
	out.Async = resp.StatusCode == http.StatusAccepted || resp.Header.Get("X-Gofer-Async") == "1"
	return out, nil
}

// GetJob fetches the current snapshot of a job by id.
func (c *Client) GetJob(id string) (job.JobResult, error) {
	var res job.JobResult
	err := c.doJSON(http.MethodGet, "/v1/jobs/"+url.PathEscape(id), nil, &res)
	return res, err
}

// ListJobs queries GET /v1/jobs with the given filters and returns the unwrapped
// job array (from the {"jobs":[...]} envelope). Empty filter fields are omitted
// from the query string. It reuses job.ListOpts (the same shape the server
// consumes) so the CLI (P2-c) and the server stay in lockstep; a zero-value opts
// lists every project's jobs up to the server default limit.
func (c *Client) ListJobs(opts job.ListOpts) ([]job.JobResult, error) {
	q := url.Values{}
	if opts.Project != "" {
		q.Set("project", opts.Project)
	}
	if opts.Status != "" {
		q.Set("status", opts.Status)
	}
	if opts.Caller != "" {
		q.Set("caller", opts.Caller)
	}
	if opts.Tag != "" {
		q.Set("tag", opts.Tag)
	}
	if opts.Agent != "" {
		q.Set("agent", opts.Agent)
	}
	if opts.Runner != "" {
		q.Set("runner", opts.Runner)
	}
	if opts.Session != "" {
		q.Set("session", opts.Session)
	}
	if opts.Plan != "" {
		q.Set("plan", opts.Plan)
	}
	if opts.SourceJob != "" {
		q.Set("source_job", opts.SourceJob)
	}
	if opts.Since > 0 {
		q.Set("since", strconv.FormatInt(opts.Since, 10))
	}
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	path := "/v1/jobs"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	var resp struct {
		Jobs []job.JobResult `json:"jobs"`
	}
	if err := c.doJSON(http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Jobs, nil
}

// ProjectMeta is one project as the server exposes it via /v1/meta (key +
// allowlists + default agent). It carries no host_path (that is a server-side
// filesystem path; the meta endpoint omits it). Used by `project list --remote`
// (E38②) and shared with the mcp client mode / worker init (E28/E37).
type ProjectMeta struct {
	Key            string   `json:"key"`
	AllowedAgents  []string `json:"allowed_agents,omitempty"`
	AllowedRunners []string `json:"allowed_runners,omitempty"`
	DefaultAgent   string   `json:"default_agent,omitempty"`
}

// Schedule is the client-side view of /v1/schedules. It mirrors the HTTP wire
// shape without importing internal/httpapi from the client package.
type Schedule struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Cron       string         `json:"cron"`
	Enabled    int            `json:"enabled"`
	CatchUp    int            `json:"catch_up"`
	NextRunAt  int64          `json:"next_run_at"`
	LastRunAt  int64          `json:"last_run_at"`
	LastJobID  string         `json:"last_job_id"`
	ProjectKey string         `json:"project_key"`
	Request    job.JobRequest `json:"request"`
}

// CreateScheduleRequest is POST /v1/schedules. Enabled/CatchUp are pointers so
// callers can omit them and let the server defaults apply.
type CreateScheduleRequest struct {
	Name     string         `json:"name"`
	Type     string         `json:"type,omitempty"`
	Cron     string         `json:"cron"`
	DelaySec int64          `json:"delay_sec,omitempty"`
	RunAt    int64          `json:"run_at,omitempty"`
	Request  job.JobRequest `json:"request"`
	Enabled  *bool          `json:"enabled,omitempty"`
	CatchUp  *bool          `json:"catch_up,omitempty"`
}

// ListProjects returns the server's live projects (GET /v1/meta → projects). It
// is the remote counterpart to reading the local config's projects, so a node
// (esp. a worker) can see what the SERVER has registered.
func (c *Client) ListProjects() ([]ProjectMeta, error) {
	var resp struct {
		Projects []ProjectMeta `json:"projects"`
	}
	if err := c.doJSON(http.MethodGet, "/v1/meta", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Projects, nil
}

// CreateSchedule creates a cron schedule via POST /v1/schedules.
func (c *Client) CreateSchedule(req CreateScheduleRequest) (Schedule, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return Schedule{}, fmt.Errorf("encode schedule request: %w", err)
	}
	var out Schedule
	err = c.doJSON(http.MethodPost, "/v1/schedules", bytes.NewReader(body), &out)
	return out, err
}

// ListSchedules returns the unwrapped schedule array, optionally filtered by
// project key.
func (c *Client) ListSchedules(project string) ([]Schedule, error) {
	path := "/v1/schedules"
	if project != "" {
		path += "?project=" + url.QueryEscape(project)
	}
	var resp struct {
		Schedules []Schedule `json:"schedules"`
	}
	if err := c.doJSON(http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Schedules, nil
}

// GetSchedule fetches one schedule by id.
func (c *Client) GetSchedule(id string) (Schedule, error) {
	var out Schedule
	err := c.doJSON(http.MethodGet, "/v1/schedules/"+url.PathEscape(id), nil, &out)
	return out, err
}

// DeleteSchedule deletes one schedule by id.
func (c *Client) DeleteSchedule(id string) error {
	return c.doJSON(http.MethodDelete, "/v1/schedules/"+url.PathEscape(id), nil, nil)
}

// SetScheduleEnabled toggles a schedule through /enable or /disable.
func (c *Client) SetScheduleEnabled(id string, enable bool) (Schedule, error) {
	action := "disable"
	if enable {
		action = "enable"
	}
	var out Schedule
	err := c.doJSON(http.MethodPost, "/v1/schedules/"+url.PathEscape(id)+"/"+action, nil, &out)
	return out, err
}

// RunSchedule triggers a schedule immediately without changing its next_run_at.
func (c *Client) RunSchedule(id string) (job.JobResult, error) {
	var out job.JobResult
	err := c.doJSON(http.MethodPost, "/v1/schedules/"+url.PathEscape(id)+"/run-now", nil, &out)
	return out, err
}

// AgentMeta is one agent as the mcp bridge contract exposes it
// (name/type/available/detail), matching mcpserver.agentEntry so the client
// backend (E28 P3) can map it 1:1. The /v1/agents endpoint actually returns the
// httpapi.agentView wire shape (key/type/available/version/error); ListAgents
// folds that into this view (name=key, detail=version when available else the
// probe error), exactly like the local mcpserver handler does.
type AgentMeta struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Available bool   `json:"available"`
	Detail    string `json:"detail,omitempty"`
}

// ListAgents fetches the server's agents (GET /v1/agents) and returns them in
// the mcp bridge contract shape. It decodes the endpoint's agentView wire shape
// then folds version/error into a single Detail (mirroring the in-process
// mcpserver list-agents handler) so client mode and standalone mode surface an
// identical agent listing.
func (c *Client) ListAgents() ([]AgentMeta, error) {
	var resp struct {
		Agents []struct {
			Key       string `json:"key"`
			Type      string `json:"type"`
			Available bool   `json:"available"`
			Version   string `json:"version"`
			Error     string `json:"error"`
		} `json:"agents"`
	}
	if err := c.doJSON(http.MethodGet, "/v1/agents", nil, &resp); err != nil {
		return nil, err
	}
	out := make([]AgentMeta, 0, len(resp.Agents))
	for _, a := range resp.Agents {
		detail := a.Version
		if !a.Available {
			detail = a.Error
		}
		out = append(out, AgentMeta{
			Name:      a.Key,
			Type:      a.Type,
			Available: a.Available,
			Detail:    detail,
		})
	}
	return out, nil
}

// WorkerCaps is a worker's capability snapshot as reported by a successful reload.
type WorkerCaps struct {
	Labels        []string `json:"labels"`
	Projects      []string `json:"projects"`
	Agents        []string `json:"agents"`
	MaxConcurrent int      `json:"max_concurrent"`
}

// WorkerReload is the reload endpoint's answer. Applied=true means the worker is
// now running the new config and Caps is its resulting capability snapshot.
type WorkerReload struct {
	WorkerID string      `json:"worker_id"`
	Applied  bool        `json:"applied"`
	Caps     *WorkerCaps `json:"caps,omitempty"`
}

// ReloadWorker asks the server to make one worker re-read its config and WAITS for
// the worker's receipt (POST /v1/workers/{id}/reload). A worker that refuses the new
// config comes back as a non-2xx whose error text carries the worker's OWN reason
// (errorFor keeps the server's {error,detail} verbatim) — the caller must print it
// as-is: it names the thing to fix.
//
// wait is the server-side budget (0 = the server default). The HTTP timeout is set a
// little wider so the server's own timeout answer (504, with context) wins the race
// against a client-side abort, which would say nothing useful.
func (c *Client) ReloadWorker(workerID, reason string, wait time.Duration) (WorkerReload, error) {
	payload := struct {
		Reason     string `json:"reason,omitempty"`
		TimeoutSec int    `json:"timeout_sec,omitempty"`
	}{Reason: reason, TimeoutSec: int(wait / time.Second)}
	body, err := json.Marshal(payload)
	if err != nil {
		return WorkerReload{}, fmt.Errorf("encode reload request: %w", err)
	}

	path := "/v1/workers/" + url.PathEscape(workerID) + "/reload"
	req, err := http.NewRequest(http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return WorkerReload{}, fmt.Errorf("build request: %w", err)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Content-Type", "application/json")

	hc := &http.Client{Timeout: reloadClientTimeout(wait)}
	resp, err := hc.Do(req)
	if err != nil {
		return WorkerReload{}, fmt.Errorf("request POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return WorkerReload{}, fmt.Errorf("read response: %w", err)
	}
	if err := errorFor(resp.StatusCode, data); err != nil {
		return WorkerReload{}, err
	}
	var out WorkerReload
	if err := json.Unmarshal(data, &out); err != nil {
		return WorkerReload{}, fmt.Errorf("decode response: %w", err)
	}
	return out, nil
}

// reloadClientTimeout gives the server room to answer its own timeout before the
// client gives up on it (default budget when wait is unset).
func reloadClientTimeout(wait time.Duration) time.Duration {
	if wait <= 0 {
		wait = 10 * time.Second
	}
	return wait + 10*time.Second
}

// GetJobRequest fetches a job's ORIGINAL request, SECRET-STRIPPED (GET /v1/jobs/{id}/
// request, P5: the endpoint now redacts by default — env values and secret-looking
// prompt/cmd become ***REDACTED***). It is for audit/display; it is NO LONGER used to
// re-submit (that moved server-side to RebuildJob). Unknown id / no request → error.
func (c *Client) GetJobRequest(id string) (job.JobRequest, error) {
	var req job.JobRequest
	err := c.doJSON(http.MethodGet, "/v1/jobs/"+url.PathEscape(id)+"/request", nil, &req)
	return req, err
}

// RebuildJob re-runs a job from its stored request + edits (P5). Empty overrides == a
// faithful re-run (the old `job rerun`); env stays server-side (only EnvSet/EnvUnset carry
// new values). Returns the NEW job's JobResult (its source_job_id links back to the source).
func (c *Client) RebuildJob(id string, ov job.RebuildOverrides) (job.JobResult, error) {
	body, err := json.Marshal(ov)
	if err != nil {
		return job.JobResult{}, fmt.Errorf("encode rebuild request: %w", err)
	}
	var res job.JobResult
	err = c.doJSON(http.MethodPost, "/v1/jobs/"+url.PathEscape(id)+"/rebuild", bytes.NewReader(body), &res)
	return res, err
}

// ListArtifacts fetches a peer job's artifact manifest (GET
// /v1/jobs/{id}/artifacts) and returns the bare `[]ArtifactItem` array as raw
// JSON (the inner "artifacts" array, unwrapped from the {"artifacts":[...]}
// envelope). It is used by the peer-http runner to回传 the产物清单 metadata onto
// the host job (P4-b); the raw bytes flow straight into the jobs.artifacts_json
// column without re-marshalling, so the manifest round-trips byte-for-byte. An
// empty/absent manifest yields a nil slice (no error).
func (c *Client) ListArtifacts(id string) (json.RawMessage, error) {
	var resp struct {
		Artifacts json.RawMessage `json:"artifacts"`
	}
	if err := c.doJSON(http.MethodGet, "/v1/jobs/"+url.PathEscape(id)+"/artifacts", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Artifacts, nil
}

// GetLogs reads the byte tail of a job log stream as plain text.
func (c *Client) GetLogs(id, stream string) (string, error) {
	if stream != "stdout" && stream != "stderr" {
		return "", fmt.Errorf("invalid log stream %q (want stdout|stderr)", stream)
	}
	resp, err := c.do(http.MethodGet, "/v1/jobs/"+url.PathEscape(id)+"/logs/"+stream+"?bytes=262144", nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read log response: %w", err)
	}
	if err := errorFor(resp.StatusCode, data); err != nil {
		return "", err
	}
	return string(data), nil
}

// LogOpts controls a line-window log request.
type LogOpts struct {
	Stream string
	Lines  int
	Head   bool
}

// GetLogsWindow reads a log using a line window, or the legacy byte tail when Lines is zero.
func (c *Client) GetLogsWindow(id string, opts LogOpts) (string, error) {
	if opts.Stream != "stdout" && opts.Stream != "stderr" {
		return "", fmt.Errorf("invalid log stream %q (want stdout|stderr)", opts.Stream)
	}
	path := "/v1/jobs/" + url.PathEscape(id) + "/logs/" + opts.Stream
	if opts.Lines > 0 {
		if opts.Head {
			path += "?head=1&lines=" + strconv.Itoa(opts.Lines)
		} else {
			path += "?lines=" + strconv.Itoa(opts.Lines)
		}
	} else {
		path += "?bytes=262144"
	}
	resp, err := c.do(http.MethodGet, path, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read log response: %w", err)
	}
	if err := errorFor(resp.StatusCode, data); err != nil {
		return "", err
	}
	return string(data), nil
}

// CancelJob POSTs to /v1/jobs/{id}/cancel and returns the resulting snapshot.
// Cancelling a terminal job is a stable no-op server-side.
func (c *Client) CancelJob(id string) (job.JobResult, error) {
	var res job.JobResult
	err := c.doJSON(http.MethodPost, "/v1/jobs/"+url.PathEscape(id)+"/cancel", nil, &res)
	return res, err
}

// ResumeJob POSTs to /v1/jobs/{id}/resume to续接 the source job's底层 agent 会话
// (session-capture P2). It returns the NEW job's snapshot (its session_id links
// back to the source session). runner is optional; when set the server enforces
// it equals the source runner (同 runner 约束). The default is async — the caller
// watches the returned job id.
func (c *Client) ResumeJob(id, prompt, runner string) (job.JobResult, error) {
	body, err := json.Marshal(struct {
		Prompt string `json:"prompt"`
		Runner string `json:"runner,omitempty"`
	}{Prompt: prompt, Runner: runner})
	if err != nil {
		return job.JobResult{}, fmt.Errorf("encode resume request: %w", err)
	}
	var res job.JobResult
	err = c.doJSON(http.MethodPost, "/v1/jobs/"+url.PathEscape(id)+"/resume", bytes.NewReader(body), &res)
	return res, err
}

// WorkflowStep is one row of a workflow's step chain in the detail response,
// mirroring httpapi's workflow.Step JSON (snake_case). job_id/status are empty
// for a step not yet started (the chain is strictly serial). Attempt is the 1-based
// retry attempt of this step-job (P1) and FanIndex the 1-based fan-out parallel index
// (P2); both are 0/omitted for a v1 single-job step, so the CLI only renders them when
// a step actually fanned out / retried (T4.3 `workflow show`).
type WorkflowStep struct {
	StepIndex int    `json:"step_index"`
	Attempt   int    `json:"attempt,omitempty"`
	FanIndex  int    `json:"fan_index,omitempty"`
	Name      string `json:"name,omitempty"`
	JobID     string `json:"job_id,omitempty"`
	Status    string `json:"status,omitempty"`
	// Type=="workflow" + ChildWorkflowID mark a sub-workflow step (P3 UI fix): it runs
	// no step-job, so JobID is empty and the link target is the child workflow.
	Type            string `json:"type,omitempty"`
	ChildWorkflowID string `json:"child_workflow_id,omitempty"`
}

// WorkflowEvent mirrors jobstore.WorkflowEvent's JSON (P1 timeline): the monotonic
// seq cursor, the event type, an optional detail_json blob and the unix-second
// timestamp. Used by `workflow events` (T4.3).
type WorkflowEvent struct {
	Seq        int64  `json:"seq"`
	WorkflowID string `json:"workflow_id"`
	Type       string `json:"type"`
	Detail     string `json:"detail,omitempty"`
	At         int64  `json:"at"`
}

// Workflow is the client-side view of a job-chain. It carries the workflow header
// fields plus (for GetWorkflow) the per-step chain. List/Submit/Cancel return the
// header only, so Steps is nil there; GetWorkflow inlines the chain. Field tags
// match httpapi's workflowSummary / workflowDetail JSON so one struct decodes both.
type Workflow struct {
	ID          string         `json:"id"`
	Title       string         `json:"title,omitempty"`
	Status      string         `json:"status"`
	CurrentStep int            `json:"current_step"`
	TotalSteps  int            `json:"total_steps"`
	CallerID    string         `json:"caller_id,omitempty"`
	Error       string         `json:"error,omitempty"`
	CreatedAt   int64          `json:"created_at"`
	UpdatedAt   int64          `json:"updated_at"`
	Steps       []WorkflowStep `json:"steps,omitempty"`
}

// SubmitWorkflow POSTs a WorkflowSpec as JSON to /v1/workflows and returns the
// created workflow header (running, step 1 started). The caller id is stamped
// server-side, so the spec carries no caller field (design §5.7).
func (c *Client) SubmitWorkflow(spec workflow.Spec) (Workflow, error) {
	body, err := json.Marshal(spec)
	if err != nil {
		return Workflow{}, fmt.Errorf("encode workflow spec: %w", err)
	}
	var wf Workflow
	err = c.doJSON(http.MethodPost, "/v1/workflows", bytes.NewReader(body), &wf)
	return wf, err
}

// GetWorkflow fetches a workflow header + its step chain by id (GET
// /v1/workflows/{id}). An unknown id surfaces as a 404 error.
func (c *Client) GetWorkflow(id string) (Workflow, error) {
	var wf Workflow
	err := c.doJSON(http.MethodGet, "/v1/workflows/"+url.PathEscape(id), nil, &wf)
	return wf, err
}

// ListWorkflows queries GET /v1/workflows, optionally filtered by status, and
// returns the unwrapped header array (from the {"workflows":[...]} envelope).
func (c *Client) ListWorkflows(status string) ([]Workflow, error) {
	path := "/v1/workflows"
	if status != "" {
		path += "?status=" + url.QueryEscape(status)
	}
	var resp struct {
		Workflows []Workflow `json:"workflows"`
	}
	if err := c.doJSON(http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Workflows, nil
}

// CancelWorkflow POSTs to /v1/workflows/{id}/cancel and returns the resulting
// header snapshot. Cancelling a terminal workflow is a stable no-op server-side.
func (c *Client) CancelWorkflow(id string) (Workflow, error) {
	var wf Workflow
	err := c.doJSON(http.MethodPost, "/v1/workflows/"+url.PathEscape(id)+"/cancel", nil, &wf)
	return wf, err
}

// Plan is the client-side view of a plan header. GetPlan inlines its jobs,
// todos and decisions.
type Plan struct {
	PlanID      string                   `json:"plan_id"`
	Title       string                   `json:"title,omitempty"`
	Description string                   `json:"description,omitempty"`
	Status      string                   `json:"status"`
	Owner       string                   `json:"owner,omitempty"`
	Progress    int                      `json:"progress,omitempty"`
	CreatedAt   int64                    `json:"created_at"`
	UpdatedAt   int64                    `json:"updated_at"`
	Counts      *jobstore.PlanCounts     `json:"counts,omitempty"`
	TodoCounts  *jobstore.PlanTodoCounts `json:"todo_counts,omitempty"`
	Completion  *jobstore.PlanCompletion `json:"completion,omitempty"`
	Jobs        []job.JobResult          `json:"jobs,omitempty"`
	Todos       []Todo                   `json:"todos,omitempty"`
	Decisions   []Decision               `json:"decisions,omitempty"`
}

// Todo is the client-side view of a plan todo item. JobID "" is a plain todo.
type Todo struct {
	TodoID    string `json:"todo_id"`
	PlanID    string `json:"plan_id"`
	JobID     string `json:"job_id,omitempty"`
	Title     string `json:"title"`
	Done      bool   `json:"done"`
	Status    string `json:"status,omitempty"`
	StartedAt int64  `json:"started_at,omitempty"`
	DoneAt    int64  `json:"done_at,omitempty"`
	Note      string `json:"note,omitempty"`
	Sort      int    `json:"sort,omitempty"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// CreatePlan POSTs /v1/plans and returns the created header.
func (c *Client) CreatePlan(planID, title, description string) (Plan, error) {
	body, err := json.Marshal(map[string]string{
		"plan_id": planID, "title": title, "description": description,
	})
	if err != nil {
		return Plan{}, fmt.Errorf("encode create plan: %w", err)
	}
	var p Plan
	err = c.doJSON(http.MethodPost, "/v1/plans", bytes.NewReader(body), &p)
	return p, err
}

// ListPlans queries GET /v1/plans, optionally filtered by status.
func (c *Client) ListPlans(status string) ([]Plan, error) {
	path := "/v1/plans"
	if status != "" {
		path += "?status=" + url.QueryEscape(status)
	}
	var resp struct {
		Plans []Plan `json:"plans"`
	}
	if err := c.doJSON(http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Plans, nil
}

// GetPlan fetches a plan header plus jobs by id.
func (c *Client) GetPlan(id string) (Plan, error) {
	var p Plan
	err := c.doJSON(http.MethodGet, "/v1/plans/"+url.PathEscape(id), nil, &p)
	return p, err
}

// UpdatePlan moves a plan along its lifecycle (PATCH /v1/plans/{id}, P6). status must be
// one of open/active/done/archived. A nil progress keeps the plan's current progress.
func (c *Client) UpdatePlan(planID, status string, progress *int) (Plan, error) {
	payload := map[string]any{"status": status}
	if progress != nil {
		payload["progress"] = *progress
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Plan{}, fmt.Errorf("encode update plan: %w", err)
	}
	var p Plan
	err = c.doJSON(http.MethodPatch, "/v1/plans/"+url.PathEscape(planID), bytes.NewReader(body), &p)
	return p, err
}

// AttachJob binds an existing job to a plan.
func (c *Client) AttachJob(planID, jobID string) (Plan, error) {
	body, err := json.Marshal(map[string]string{"job_id": jobID})
	if err != nil {
		return Plan{}, fmt.Errorf("encode attach job: %w", err)
	}
	var p Plan
	err = c.doJSON(http.MethodPost, "/v1/plans/"+url.PathEscape(planID)+"/jobs", bytes.NewReader(body), &p)
	return p, err
}

// AddTodo creates a plan todo. jobID may be empty for a plain checklist item;
// note is an optional short remark.
func (c *Client) AddTodo(planID, title, jobID, note string) (Todo, error) {
	body, err := json.Marshal(map[string]any{"title": title, "job_id": jobID, "note": note})
	if err != nil {
		return Todo{}, fmt.Errorf("encode add todo: %w", err)
	}
	var t Todo
	err = c.doJSON(http.MethodPost, "/v1/plans/"+url.PathEscape(planID)+"/todos", bytes.NewReader(body), &t)
	return t, err
}

// UpdateTodo sets a todo's manual done flag (legacy二态 wrapper).
func (c *Client) UpdateTodo(todoID string, done bool) (Todo, error) {
	status := "pending"
	if done {
		status = "done"
	}
	return c.UpdateTodoStatus(todoID, status, nil)
}

// UpdateTodoStatus moves a todo along its lifecycle and/or updates its note
// (Part C §C2). status "" = keep current (note-only); note nil = keep current.
func (c *Client) UpdateTodoStatus(todoID, status string, note *string) (Todo, error) {
	payload := map[string]any{}
	if status != "" {
		payload["status"] = status
	}
	if note != nil {
		payload["note"] = *note
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Todo{}, fmt.Errorf("encode update todo: %w", err)
	}
	var t Todo
	err = c.doJSON(http.MethodPatch, "/v1/todos/"+url.PathEscape(todoID), bytes.NewReader(body), &t)
	return t, err
}

// UpdateTodoStatusAppend updates status and appends a note in one PATCH request.
func (c *Client) UpdateTodoStatusAppend(todoID, status, note string) (Todo, error) {
	body, err := json.Marshal(map[string]any{"status": status, "append_note": note})
	if err != nil {
		return Todo{}, fmt.Errorf("encode update todo: %w", err)
	}
	var t Todo
	err = c.doJSON(http.MethodPatch, "/v1/todos/"+url.PathEscape(todoID), bytes.NewReader(body), &t)
	return t, err
}

// AppendTodoNote appends a line to the todo's note (server-side atomic append,
// newline-separated). Mutually exclusive with UpdateTodoStatus's note
// (overwrite) — the server rejects a body carrying both.
func (c *Client) AppendTodoNote(todoID, note string) (Todo, error) {
	body, err := json.Marshal(map[string]any{"append_note": note})
	if err != nil {
		return Todo{}, fmt.Errorf("encode append todo note: %w", err)
	}
	var t Todo
	err = c.doJSON(http.MethodPatch, "/v1/todos/"+url.PathEscape(todoID), bytes.NewReader(body), &t)
	return t, err
}

// Decision is the client-side view of a plan_decisions row (decision channel,
// Part C §C3). PlanID "" is a global question; Options empty = free-text
// answer. State is OPEN|ANSWERED|EXPIRED; timestamps are unix seconds.
type Decision struct {
	ID         string   `json:"id"`
	PlanID     string   `json:"plan_id,omitempty"`
	Title      string   `json:"title"`
	Question   string   `json:"question"`
	Options    []string `json:"options,omitempty"`
	Answer     string   `json:"answer,omitempty"`
	State      string   `json:"state"`
	TimeoutSec int64    `json:"timeout_sec"`
	AskedAt    int64    `json:"asked_at"`
	AnsweredAt int64    `json:"answered_at,omitempty"`
	AnsweredBy string   `json:"answered_by,omitempty"`
	// SessionID / Kind identify a session-relay turn (SESS-01); empty otherwise.
	SessionID string `json:"session_id,omitempty"`
	Kind      string `json:"kind,omitempty"`
}

// AskDecision POSTs /v1/decisions and returns the created OPEN decision.
// planID may be "" (global question); options empty = free-text answer.
// timeoutSec <= 0 / out-of-range values are clamped server-side (the
// authoritative clamp lives in jobstore.InsertDecision, plan HIGH-2).
func (c *Client) AskDecision(planID, title, question string, options []string, timeoutSec int64) (Decision, error) {
	body, err := json.Marshal(map[string]any{
		"plan_id": planID, "title": title, "question": question,
		"options": options, "timeout_sec": timeoutSec,
	})
	if err != nil {
		return Decision{}, fmt.Errorf("encode ask decision: %w", err)
	}
	var d Decision
	err = c.doJSON(http.MethodPost, "/v1/decisions", bytes.NewReader(body), &d)
	return d, err
}

// GetDecision fetches one decision (GET /v1/decisions/{id}); the read path
// applies lazy expiry, so a past-deadline decision comes back EXPIRED.
func (c *Client) GetDecision(id string) (Decision, error) {
	var d Decision
	err := c.doJSON(http.MethodGet, "/v1/decisions/"+url.PathEscape(id), nil, &d)
	return d, err
}

// ListDecisions queries GET /v1/decisions, optionally filtered by state and/or
// plan id ("" = no filter).
func (c *Client) ListDecisions(state, planID string) ([]Decision, error) {
	q := url.Values{}
	if state != "" {
		q.Set("state", state)
	}
	if planID != "" {
		q.Set("plan_id", planID)
	}
	path := "/v1/decisions"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out struct {
		Decisions []Decision `json:"decisions"`
	}
	if err := c.doJSON(http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Decisions, nil
}

// AnswerDecision POSTs /v1/decisions/{id}/answer. A non-2xx surfaces via
// errorFor: unknown id → 404 error, already answered/expired → 409 error.
func (c *Client) AnswerDecision(id, answer string) (Decision, error) {
	body, err := json.Marshal(map[string]string{"answer": answer})
	if err != nil {
		return Decision{}, fmt.Errorf("encode answer decision: %w", err)
	}
	var d Decision
	err = c.doJSON(http.MethodPost, "/v1/decisions/"+url.PathEscape(id)+"/answer", bytes.NewReader(body), &d)
	return d, err
}

// ExportWorkflow fetches a workflow's reconstructed WorkflowSpec (GET
// /v1/workflows/{id}/export, T4.1). The server strips credential-looking values
// (SR403); the returned bool reports whether anything was redacted (from the
// X-Gofer-Redacted response header) so `workflow export` can warn that a placeholder
// must be filled in before re-running. An unknown id surfaces as a 404 error.
func (c *Client) ExportWorkflow(id string) (workflow.Spec, bool, error) {
	resp, err := c.do(http.MethodGet, "/v1/workflows/"+url.PathEscape(id)+"/export", nil)
	if err != nil {
		return workflow.Spec{}, false, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return workflow.Spec{}, false, fmt.Errorf("read export response: %w", err)
	}
	if err := errorFor(resp.StatusCode, data); err != nil {
		return workflow.Spec{}, false, err
	}
	var spec workflow.Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		return workflow.Spec{}, false, fmt.Errorf("decode export response: %w", err)
	}
	redacted := resp.Header.Get("X-Gofer-Redacted") == "1"
	return spec, redacted, nil
}

// ListWorkflowEvents fetches a workflow's append-only lifecycle events (GET
// /v1/workflows/{id}/events, T4.3). sinceSeq>0 returns only events strictly after that
// cursor. The {"events":[...]} envelope is unwrapped to the bare slice.
func (c *Client) ListWorkflowEvents(id string, sinceSeq int64) ([]WorkflowEvent, error) {
	path := "/v1/workflows/" + url.PathEscape(id) + "/events"
	if sinceSeq > 0 {
		path += "?since=" + url.QueryEscape(strconv.FormatInt(sinceSeq, 10))
	}
	var resp struct {
		Events []WorkflowEvent `json:"events"`
	}
	if err := c.doJSON(http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Events, nil
}

// GetInteractions lists a job's running-time interactions (GET
// /v1/jobs/{id}/interactions), unwrapping the {"interactions":[...]} envelope.
// job.Interaction's JSON tags match the endpoint's element shape, so the slice
// decodes directly. Used by the mcp client backend (E28) to surface a peer
// job's pending/answered interactions. An unknown id surfaces as a 404 error.
func (c *Client) GetInteractions(id string) ([]job.Interaction, error) {
	var resp struct {
		Interactions []job.Interaction `json:"interactions"`
	}
	if err := c.doJSON(http.MethodGet, "/v1/jobs/"+url.PathEscape(id)+"/interactions", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Interactions, nil
}

// ListPendingInteractions fetches the pending interactions across all active jobs
// (E25 监督, GET /v1/interactions?status=pending), unwrapping the
// {"interactions":[...]} envelope. Used by the mcp client backend's
// gofer_list_pending_interactions tool (supervisor discovery in client mode).
func (c *Client) ListPendingInteractions() ([]job.Interaction, error) {
	var resp struct {
		Interactions []job.Interaction `json:"interactions"`
	}
	if err := c.doJSON(http.MethodGet, "/v1/interactions?status=pending", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Interactions, nil
}

// AnswerInteraction POSTs an answer to a peer interaction (P9 passthrough / E28
// client mode) and returns the updated job.Interaction the server echoes back
// (the answer endpoint returns a bare job.Interaction). Fire-and-forget callers
// may ignore the returned Interaction. responder is the answering driver's agent_id
// (监督派生作答闸 P3.1) forwarded so the central serve grades the source; "" = unattributed
// (human/relay), omitted from the body.
func (c *Client) AnswerInteraction(jobID, interactionID, answer, responder string) (job.Interaction, error) {
	payload := map[string]string{"answer": answer}
	if responder != "" {
		payload["responder"] = responder
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return job.Interaction{}, fmt.Errorf("encode answer: %w", err)
	}
	var it job.Interaction
	err = c.doJSON(http.MethodPost,
		"/v1/jobs/"+url.PathEscape(jobID)+"/interactions/"+url.PathEscape(interactionID)+"/answer",
		bytes.NewReader(body), &it)
	return it, err
}

// PuntInteraction POSTs to the punt endpoint (E28 client mode): the通用 sup marks a pending
// interaction as needs_human ("高危/拿不准, 留给人", y5wt). No body, no useful response — the
// central serve flips needs_human and leaves the interaction pending for a human. Returns the
// transport error only.
func (c *Client) PuntInteraction(jobID, interactionID string) error {
	return c.doJSON(http.MethodPost,
		"/v1/jobs/"+url.PathEscape(jobID)+"/interactions/"+url.PathEscape(interactionID)+"/punt",
		nil, nil)
}

// RegisterAgent registers/renews a driver agent with the central serve (POST
// /v1/agents/register) and returns its public address + private capability handle
// (E36 client mode). caller_id/client are stamped server-side from the bearer
// token + connection (provenance), so only name/role/project go over the wire.
func (c *Client) RegisterAgent(name, role, project string) (agentID, token string, err error) {
	body, err := json.Marshal(map[string]string{"name": name, "role": role, "project": project})
	if err != nil {
		return "", "", fmt.Errorf("encode register: %w", err)
	}
	var res presence.RegisterResult
	if err := c.doJSON(http.MethodPost, "/v1/agents/register", bytes.NewReader(body), &res); err != nil {
		return "", "", err
	}
	return res.AgentID, res.AgentToken, nil
}

// PollInbox polls an agent's inbox (POST /v1/agents/{id}/inbox/poll), unwrapping
// the {"messages":[...]} envelope. ack=false peeks without consuming (?ack=false).
// The agent_token is verified server-side (403 mismatch / 404 unknown surface as
// errors).
func (c *Client) PollInbox(agentID, token string, ack bool) ([]presence.Message, error) {
	body, err := json.Marshal(map[string]string{"agent_token": token})
	if err != nil {
		return nil, fmt.Errorf("encode poll: %w", err)
	}
	path := "/v1/agents/" + url.PathEscape(agentID) + "/inbox/poll"
	if !ack {
		path += "?ack=false"
	}
	var resp struct {
		Messages []presence.Message `json:"messages"`
	}
	if err := c.doJSON(http.MethodPost, path, bytes.NewReader(body), &resp); err != nil {
		return nil, err
	}
	return resp.Messages, nil
}

// PostMessage delivers a message (POST /v1/messages) addressed by to (agent_id /
// role:<name> / role-one:<name> / broadcast) and returns the fan-out delivered count.
func (c *Client) PostMessage(from, to, kind, body, ref string) (int, error) {
	payload, err := json.Marshal(map[string]string{
		"from_agent": from, "to": to, "kind": kind, "body": body, "ref": ref,
	})
	if err != nil {
		return 0, fmt.Errorf("encode message: %w", err)
	}
	var resp struct {
		Delivered int `json:"delivered"`
	}
	if err := c.doJSON(http.MethodPost, "/v1/messages", bytes.NewReader(payload), &resp); err != nil {
		return 0, err
	}
	return resp.Delivered, nil
}

// ListPresence fetches the online registry (GET /v1/agents/presence), optionally
// filtered by role/project, unwrapping the {"agents":[...]} envelope. agent_token
// is never returned (presence.Agent has no token field).
func (c *Client) ListPresence(role, project string) ([]presence.Agent, error) {
	q := url.Values{}
	if role != "" {
		q.Set("role", role)
	}
	if project != "" {
		q.Set("project", project)
	}
	path := "/v1/agents/presence"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	var resp struct {
		Agents []presence.Agent `json:"agents"`
	}
	if err := c.doJSON(http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Agents, nil
}

// DeregisterAgent removes an agent from the central registry (POST
// /v1/agents/{id}/deregister), token-checked + idempotent server-side.
func (c *Client) DeregisterAgent(agentID, token string) error {
	body, err := json.Marshal(map[string]string{"agent_token": token})
	if err != nil {
		return fmt.Errorf("encode deregister: %w", err)
	}
	return c.doJSON(http.MethodPost, "/v1/agents/"+url.PathEscape(agentID)+"/deregister", bytes.NewReader(body), nil)
}

// doJSON performs the request and decodes a JSON body into out on 2xx; non-2xx
// responses are turned into a friendly error carrying the server's error+detail.
func (c *Client) doJSON(method, path string, body io.Reader, out any) error {
	resp, err := c.do(method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if err := errorFor(resp.StatusCode, data); err != nil {
		return err
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// do builds and sends the HTTP request, attaching the bearer token (when set)
// and a JSON content type for bodies.
func (c *Client) do(method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s %s: %w", method, path, err)
	}
	return resp, nil
}

// serverError is the uniform error shape the server returns (plan §7):
// {"error":"...","detail":"..."}.
type serverError struct {
	ErrMsg string `json:"error"`
	Detail string `json:"detail"`
}

// errorFor returns a friendly Go error for a non-2xx response, preferring the
// server's {error,detail} body and falling back to the raw payload / status
// text. It returns nil for 2xx.
func errorFor(status int, body []byte) error {
	if status >= 200 && status < 300 {
		return nil
	}
	var se serverError
	if json.Unmarshal(body, &se) == nil && se.ErrMsg != "" {
		if se.Detail != "" {
			return &StatusError{Status: status, Msg: fmt.Sprintf("server %d: %s: %s", status, se.ErrMsg, se.Detail)}
		}
		return &StatusError{Status: status, Msg: fmt.Sprintf("server %d: %s", status, se.ErrMsg)}
	}
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = http.StatusText(status)
	}
	return &StatusError{Status: status, Msg: fmt.Sprintf("server %d: %s", status, msg)}
}

// StatusError is the error doJSON returns for a non-2xx response. Its message
// is the same friendly text as before; Status lets callers branch on 404/409
// without parsing strings (session relay hook: unknown session → re-register).
type StatusError struct {
	Status int
	Msg    string
}

func (e *StatusError) Error() string { return e.Msg }

// StatusOf returns the HTTP status carried by err (0 when err is not a
// StatusError, e.g. a transport failure).
func StatusOf(err error) int {
	var se *StatusError
	if errors.As(err, &se) {
		return se.Status
	}
	return 0
}

// ---- session relay (SESS-01) ----

// AgentSession mirrors httpapi's sessionView (a terminal agent-CLI session
// registered through its hooks). Timestamps are unix seconds.
type AgentSession struct {
	SessionID   string `json:"session_id"`
	Agent       string `json:"agent"`
	ProjectKey  string `json:"project_key,omitempty"`
	Runner      string `json:"runner,omitempty"`
	Cwd         string `json:"cwd,omitempty"`
	Title       string `json:"title,omitempty"`
	Transcript  string `json:"transcript,omitempty"`
	TmuxPane    string `json:"tmux_pane,omitempty"`
	State       string `json:"state"`
	Relay       bool   `json:"relay"`
	TurnNo      int64  `json:"turn_no"`
	LastMessage string `json:"last_message,omitempty"`
	LastEvent   string `json:"last_event,omitempty"`
	LastSeenAt  int64  `json:"last_seen_at"`
	StartedAt   int64  `json:"started_at"`
	EndedAt     int64  `json:"ended_at,omitempty"`
}

// SessionRegister is the POST /v1/sessions body.
type SessionRegister struct {
	SessionID  string `json:"session_id"`
	Agent      string `json:"agent"`
	ProjectKey string `json:"project_key,omitempty"`
	Runner     string `json:"runner,omitempty"`
	Cwd        string `json:"cwd,omitempty"`
	Title      string `json:"title,omitempty"`
	Transcript string `json:"transcript,omitempty"`
	TmuxPane   string `json:"tmux_pane,omitempty"`
	Event      string `json:"event,omitempty"`
}

// SessionHeartbeat is the POST /v1/sessions/{sid}/heartbeat body.
type SessionHeartbeat struct {
	Event       string `json:"event"`
	State       string `json:"state,omitempty"`
	LastMessage string `json:"last_message,omitempty"`
	Title       string `json:"title,omitempty"`
	// Injected: this UserPromptSubmit is the relay's own continuation (no auto-off).
	Injected bool `json:"injected,omitempty"`
}

// SessionDetail is GET /v1/sessions/{sid}: the session + recent turns (newest first).
type SessionDetail struct {
	Session AgentSession `json:"session"`
	Turns   []Decision   `json:"turns"`
}

// TurnStatus is GET /v1/sessions/{sid}/turns/{id}: outcome is one of
// open|answered|expired|relay_off; Decision.Answer holds the reply when answered.
type TurnStatus struct {
	Outcome  string   `json:"outcome"`
	Relay    bool     `json:"relay"`
	Decision Decision `json:"decision"`
}

// SessionListOpts filters ListSessions.
type SessionListOpts struct {
	Project, State, Agent, Cwd string
	IncludeEnded               bool
	Limit                      int
}

// RegisterSession upserts an agent session (hook SessionStart / first contact).
func (c *Client) RegisterSession(in SessionRegister) (AgentSession, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return AgentSession{}, fmt.Errorf("encode register session: %w", err)
	}
	var a AgentSession
	err = c.doJSON(http.MethodPost, "/v1/sessions", bytes.NewReader(body), &a)
	return a, err
}

// HeartbeatSession applies a hook event; the returned session's Relay is the
// switch the Stop hook keys on. A 404 error means the session is unknown.
func (c *Client) HeartbeatSession(sid string, hb SessionHeartbeat) (AgentSession, error) {
	body, err := json.Marshal(hb)
	if err != nil {
		return AgentSession{}, fmt.Errorf("encode heartbeat: %w", err)
	}
	var a AgentSession
	err = c.doJSON(http.MethodPost, "/v1/sessions/"+url.PathEscape(sid)+"/heartbeat", bytes.NewReader(body), &a)
	return a, err
}

// ListSessions lists agent sessions (GET /v1/sessions).
func (c *Client) ListSessions(opts SessionListOpts) ([]AgentSession, error) {
	q := url.Values{}
	if opts.Project != "" {
		q.Set("project", opts.Project)
	}
	if opts.State != "" {
		q.Set("state", opts.State)
	}
	if opts.Agent != "" {
		q.Set("agent", opts.Agent)
	}
	if opts.Cwd != "" {
		q.Set("cwd", opts.Cwd)
	}
	if opts.IncludeEnded {
		q.Set("all", "1")
	}
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	path := "/v1/sessions"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out struct {
		Sessions []AgentSession `json:"sessions"`
	}
	if err := c.doJSON(http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Sessions, nil
}

// GetSession fetches one session with its recent turns.
func (c *Client) GetSession(sid string) (SessionDetail, error) {
	var d SessionDetail
	err := c.doJSON(http.MethodGet, "/v1/sessions/"+url.PathEscape(sid), nil, &d)
	return d, err
}

// SetSessionRelay flips the relay switch.
func (c *Client) SetSessionRelay(sid string, on bool) (AgentSession, error) {
	body, _ := json.Marshal(map[string]bool{"relay": on})
	var a AgentSession
	err := c.doJSON(http.MethodPost, "/v1/sessions/"+url.PathEscape(sid)+"/relay", bytes.NewReader(body), &a)
	return a, err
}

// OpenSessionTurn posts the agent's last message as a relay turn (hook Stop).
// A 409 error means relay is off.
func (c *Client) OpenSessionTurn(sid, msg string, timeoutSec int64) (Decision, error) {
	body, err := json.Marshal(map[string]any{"body": msg, "timeout_sec": timeoutSec})
	if err != nil {
		return Decision{}, fmt.Errorf("encode open turn: %w", err)
	}
	var d Decision
	err = c.doJSON(http.MethodPost, "/v1/sessions/"+url.PathEscape(sid)+"/turns", bytes.NewReader(body), &d)
	return d, err
}

// WaitSessionTurn long-polls a turn for up to waitSec seconds (server caps at 25).
func (c *Client) WaitSessionTurn(sid, decisionID string, waitSec int) (TurnStatus, error) {
	path := "/v1/sessions/" + url.PathEscape(sid) + "/turns/" + url.PathEscape(decisionID)
	if waitSec > 0 {
		path += "?wait=" + strconv.Itoa(waitSec)
	}
	var st TurnStatus
	err := c.doJSON(http.MethodGet, path, nil, &st)
	return st, err
}

// SaySession answers the session's newest OPEN turn. A 409 error means no turn is waiting.
func (c *Client) SaySession(sid, answer string) (Decision, error) {
	body, err := json.Marshal(map[string]string{"answer": answer})
	if err != nil {
		return Decision{}, fmt.Errorf("encode say: %w", err)
	}
	var d Decision
	err = c.doJSON(http.MethodPost, "/v1/sessions/"+url.PathEscape(sid)+"/say", bytes.NewReader(body), &d)
	return d, err
}

// DeleteSession removes a session registration.
func (c *Client) DeleteSession(sid string) error {
	return c.doJSON(http.MethodDelete, "/v1/sessions/"+url.PathEscape(sid), nil, nil)
}
