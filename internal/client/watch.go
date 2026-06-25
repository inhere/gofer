package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// OpenStream issues a GET against /v1/jobs/{id}/stream under ctx, attaching the
// bearer token, and returns the live SSE response. The caller owns closing the
// body and parsing the SSE frames. Unlike the other helpers it streams (no
// per-request timeout) so a long-lived job stays connected; cancel via ctx.
//
// It exists so a remote runner (internal/runner/peerhttp) can consume a peer's
// log stream without re-deriving the base URL / auth wiring.
func (c *Client) OpenStream(ctx context.Context, id string) (*http.Response, error) {
	return c.openStream(ctx, id, 0)
}

// openStream is the shared stream opener behind OpenStream / StreamJob. from > 0
// resumes stdout from a byte offset via the ?from= query (the path id is escaped
// as a path segment, the offset as a query param).
func (c *Client) openStream(ctx context.Context, id string, from int) (*http.Response, error) {
	streamURL := c.baseURL + "/v1/jobs/" + url.PathEscape(id) + "/stream"
	if from > 0 {
		streamURL += "?from=" + strconv.Itoa(from)
	}
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

// StreamJob opens the job's SSE stream (GET /v1/jobs/{id}/stream?from=) under
// ctx and invokes onEvent for each parsed frame in order. It returns when the
// stream emits an `end` event, the connection closes (EOF), or ctx is cancelled.
// The from offset resumes the stdout stream from a byte position (<=0 starts at
// the beginning). It reuses ParseSSE (the same Go-side frame parser the
// peer-http runner consumes), buffering across reads so a frame split over two
// reads is still parsed once whole. A transport error other than EOF is
// returned; ctx cancellation returns nil (a clean caller-driven stop).
func (c *Client) StreamJob(ctx context.Context, id string, from int, onEvent func(SSEEvent)) error {
	resp, err := c.openStream(ctx, id, from)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var buf []byte
	tmp := make([]byte, 32*1024)
	for {
		if ctx.Err() != nil {
			return nil
		}
		n, readErr := reader.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			frames, rest := ParseSSE(string(buf))
			buf = []byte(rest)
			for _, fr := range frames {
				onEvent(fr)
				if fr.Event == "end" {
					return nil
				}
			}
		}
		if readErr != nil {
			// EOF or transport error: drain any complete trailing frame, then stop.
			if len(buf) > 0 {
				frames, _ := ParseSSE(string(buf) + "\n\n")
				for _, fr := range frames {
					onEvent(fr)
					if fr.Event == "end" {
						return nil
					}
				}
			}
			if errors.Is(readErr, io.EOF) || ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("read stream %s: %w", id, readErr)
		}
	}
}

// WatchHandlers carries the presentation callbacks for WatchJob. The watch
// state-machine (SSE consumption + status/EOF tracking) lives here in client;
// the command layer supplies these callbacks to print and to derive a process
// exit code (the exit-code mapping is a command concern, kept in commands).
//   - OnStatus fires once per status CHANGE (the first time a new status is seen).
//   - OnLog fires for each non-empty log frame's raw text.
// Both may be nil.
type WatchHandlers struct {
	OnStatus func(status string)
	OnLog    func(text string)
}

// WatchJob streams a job's SSE (status + incremental logs) under ctx until the
// stream ends (terminal `end` frame / EOF) or ctx is cancelled, invoking the
// handlers on status changes and log text. It returns the terminal status it
// observed; when the stream ended without a terminal status frame it fetches the
// authoritative status via GetJob (final == "" only if that fetch also fails).
// ctx cancellation (e.g. Ctrl-C) returns the status seen so far (often ""), so
// the caller can detect an interrupt via ctx.Err(). The state-machine is the
// shared watch logic behind `job watch` and `job rerun --watch`.
func (c *Client) WatchJob(ctx context.Context, id string, from int, h WatchHandlers) (final string, err error) {
	var lastStatus, finalStatus string
	streamErr := c.StreamJob(ctx, id, from, func(ev SSEEvent) {
		switch ev.Event {
		case "status":
			var jr job.JobResult
			if err := json.Unmarshal(ev.Data, &jr); err != nil {
				return
			}
			if jr.Status != lastStatus {
				lastStatus = jr.Status
				if h.OnStatus != nil {
					h.OnStatus(jr.Status)
				}
			}
			if job.IsTerminal(jr.Status) {
				finalStatus = jr.Status
			}
		case "log":
			var lf struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(ev.Data, &lf); err == nil && lf.Text != "" {
				if h.OnLog != nil {
					h.OnLog(lf.Text)
				}
			}
		}
	})
	if streamErr != nil {
		return "", streamErr
	}

	// ctx cancelled before any terminal status: report what we have (caller checks
	// ctx.Err() to distinguish an interrupt from a clean finish).
	if ctx.Err() != nil && finalStatus == "" {
		return "", nil
	}

	if finalStatus == "" {
		// Stream ended (EOF) without a terminal status frame: fetch authoritative.
		if res, gErr := c.GetJob(id); gErr == nil {
			finalStatus = res.Status
		}
	}
	return finalStatus, nil
}

// WatchWorkflow polls GetWorkflow until the workflow reaches a terminal state,
// invoking onStep on each step's status CHANGE (per step index). It returns the
// terminal workflow. Workflow-level SSE is a follow-up; v1 uses a simple poll
// loop (plan §P3-a range note). onStep may be nil.
func (c *Client) WatchWorkflow(ctx context.Context, id string, onStep func(st WorkflowStep)) (Workflow, error) {
	deadline := time.Now().Add(2 * time.Hour)
	// lastStep tracks the last seen status per step index so onStep fires on change.
	lastStep := map[int]string{}
	for time.Now().Before(deadline) {
		wf, err := c.GetWorkflow(id)
		if err != nil {
			return Workflow{}, err
		}
		for _, st := range wf.Steps {
			if lastStep[st.StepIndex] != st.Status {
				lastStep[st.StepIndex] = st.Status
				if onStep != nil {
					onStep(st)
				}
			}
		}
		if isWorkflowTerminal(wf.Status) {
			return wf, nil
		}
		time.Sleep(2 * time.Second)
	}
	return Workflow{}, fmt.Errorf("workflow %s did not finish within the watch window", id)
}

// isWorkflowTerminal reports whether a workflow status is terminal (not running).
func isWorkflowTerminal(status string) bool {
	switch status {
	case jobstore.WorkflowDone, jobstore.WorkflowFailed, jobstore.WorkflowCancelled:
		return true
	default:
		return false
	}
}
