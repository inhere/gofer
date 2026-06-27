// Package client is a thin HTTP client for the gofer control plane.
// It mirrors the /v1/jobs API (plan §7) and reuses the job package's
// JobRequest/JobResult structs as the wire types so the CLI (P6) and the MCP
// server (P8) share one transport.
package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
)

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

// GetJobRequest fetches the original JobRequest a job was created from
// (GET /v1/jobs/{id}/request, P2-b). It is used by `job rerun` to re-submit the
// same request. An unknown id or a job with no recorded request yields a 404
// surfaced as an error.
func (c *Client) GetJobRequest(id string) (job.JobRequest, error) {
	var req job.JobRequest
	err := c.doJSON(http.MethodGet, "/v1/jobs/"+url.PathEscape(id)+"/request", nil, &req)
	return req, err
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

// GetLogs reads the tail of a job's "stdout" or "stderr" stream as plain text.
func (c *Client) GetLogs(id, stream string) (string, error) {
	if stream != "stdout" && stream != "stderr" {
		return "", fmt.Errorf("invalid log stream %q (want stdout|stderr)", stream)
	}
	resp, err := c.do(http.MethodGet, "/v1/jobs/"+url.PathEscape(id)+"/logs/"+stream, nil)
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

// AnswerInteraction POSTs an answer to a peer interaction (P9 passthrough / E28
// client mode) and returns the updated job.Interaction the server echoes back
// (the answer endpoint returns a bare job.Interaction). Fire-and-forget callers
// may ignore the returned Interaction.
func (c *Client) AnswerInteraction(jobID, interactionID, answer string) (job.Interaction, error) {
	body, err := json.Marshal(map[string]string{"answer": answer})
	if err != nil {
		return job.Interaction{}, fmt.Errorf("encode answer: %w", err)
	}
	var it job.Interaction
	err = c.doJSON(http.MethodPost,
		"/v1/jobs/"+url.PathEscape(jobID)+"/interactions/"+url.PathEscape(interactionID)+"/answer",
		bytes.NewReader(body), &it)
	return it, err
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
			return fmt.Errorf("server %d: %s: %s", status, se.ErrMsg, se.Detail)
		}
		return fmt.Errorf("server %d: %s", status, se.ErrMsg)
	}
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = http.StatusText(status)
	}
	return fmt.Errorf("server %d: %s", status, msg)
}
