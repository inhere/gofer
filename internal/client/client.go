// Package client is a thin HTTP client for the gofer control plane.
// It mirrors the /v1/jobs API (plan §7) and reuses the job package's
// JobRequest/JobResult structs as the wire types so the CLI (P6) and the MCP
// server (P8) share one transport.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/job"
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

// SubmitJob POSTs a JobRequest to /v1/jobs and returns the initial JobResult
// (with the assigned id and queued/running status).
func (c *Client) SubmitJob(req job.JobRequest) (job.JobResult, error) {
	var res job.JobResult
	body, err := json.Marshal(req)
	if err != nil {
		return res, fmt.Errorf("encode job request: %w", err)
	}
	err = c.doJSON(http.MethodPost, "/v1/jobs", bytes.NewReader(body), &res)
	return res, err
}

// GetJob fetches the current snapshot of a job by id.
func (c *Client) GetJob(id string) (job.JobResult, error) {
	var res job.JobResult
	err := c.doJSON(http.MethodGet, "/v1/jobs/"+url.PathEscape(id), nil, &res)
	return res, err
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

// AnswerInteraction POSTs an answer to a peer interaction (P9 passthrough). The
// updated Interaction body is not needed by the caller, so it is discarded.
func (c *Client) AnswerInteraction(jobID, interactionID, answer string) error {
	body, err := json.Marshal(map[string]string{"answer": answer})
	if err != nil {
		return fmt.Errorf("encode answer: %w", err)
	}
	return c.doJSON(http.MethodPost,
		"/v1/jobs/"+url.PathEscape(jobID)+"/interactions/"+url.PathEscape(interactionID)+"/answer",
		bytes.NewReader(body), nil)
}

// OpenStream issues a GET against /v1/jobs/{id}/stream under ctx, attaching the
// bearer token, and returns the live SSE response. The caller owns closing the
// body and parsing the SSE frames. Unlike the other helpers it streams (no
// per-request timeout) so a long-lived job stays connected; cancel via ctx.
//
// It exists so a remote runner (internal/runner/peerhttp) can consume a peer's
// log stream without re-deriving the base URL / auth wiring.
func (c *Client) OpenStream(ctx context.Context, id string) (*http.Response, error) {
	streamURL := c.baseURL + "/v1/jobs/" + url.PathEscape(id) + "/stream"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build stream request: %w", err)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Accept", "text/event-stream")
	// A dedicated client with no read timeout so an open SSE stream is not cut by
	// the 30s control-plane timeout; lifetime is bound to ctx.
	sc := &http.Client{}
	resp, err := sc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("open stream %s: %w", id, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err := errorFor(resp.StatusCode, data); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("stream %s: unexpected status %d", id, resp.StatusCode)
	}
	return resp, nil
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
