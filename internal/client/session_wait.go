package client

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// SessionWaitResult is one answer of GET /v1/sessions/{sid}/wait: the session as
// it stands plus whether its state is one of the wanted ones. Reached=false means
// the request's own budget ran out first.
type SessionWaitResult struct {
	Session AgentSession `json:"session"`
	// State is the session's state at the moment of the answer.
	State string `json:"state"`
	// Until is the set of states the wait asked for.
	Until []string `json:"until"`
	// Reached is true when State is one of Until.
	Reached bool `json:"reached"`
	// WaitedSec is how long the server held the request.
	WaitedSec int `json:"waited_sec"`
}

// SessionScreen is the read-only screen of one session (GET
// /v1/sessions/{sid}/screen): its recent relay turns, or the pty transcript tail of
// the job that took it over, as plain text with no escape sequences.
type SessionScreen struct {
	SessionID string `json:"session_id"`
	Agent     string `json:"agent,omitempty"`
	State     string `json:"state,omitempty"`
	// Source is "pty" (a takeover pty job's transcript), "turns" (the session's
	// recent relay turns) or "message" (no turns: its last message stands in).
	Source string `json:"source"`
	JobID  string `json:"job_id,omitempty"`
	Text   string `json:"text"`
}

// WaitSession long-polls a session's state (GET /v1/sessions/{sid}/wait): it
// returns as soon as the state is one of until, or when timeout runs out
// (Reached=false). timeout <= 0 asks for the current state only.
//
// The HTTP timeout is a little wider than the server-side budget, so the server's
// own answer wins the race against a client-side abort.
func (c *Client) WaitSession(sid string, until []string, timeout time.Duration) (SessionWaitResult, error) {
	q := url.Values{}
	states := make([]string, 0, len(until))
	for _, st := range until {
		if st = strings.TrimSpace(st); st != "" {
			states = append(states, st)
		}
	}
	q.Set("until", strings.Join(states, ","))
	if timeout > 0 {
		q.Set("timeout", strconv.Itoa(int(timeout/time.Second)))
	}
	path := "/v1/sessions/" + url.PathEscape(sid) + "/wait?" + q.Encode()

	req, err := http.NewRequest(http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return SessionWaitResult{}, fmt.Errorf("build request: %w", err)
	}
	c.applyAuth(req.Header)

	hc := &http.Client{Timeout: timeout + 30*time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		return SessionWaitResult{}, fmt.Errorf("request GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return SessionWaitResult{}, fmt.Errorf("read response: %w", err)
	}
	if err := errorFor(resp.StatusCode, data); err != nil {
		return SessionWaitResult{}, err
	}
	var out SessionWaitResult
	if err := json.Unmarshal(data, &out); err != nil {
		return SessionWaitResult{}, fmt.Errorf("decode response: %w", err)
	}
	return out, nil
}

// SessionScreen reads the tail of what a session shows (GET
// /v1/sessions/{sid}/screen): n bounds the returned lines (0 = the server default).
func (c *Client) SessionScreen(sid string, n int) (SessionScreen, error) {
	q := url.Values{}
	if n > 0 {
		q.Set("n", strconv.Itoa(n))
	}
	var out SessionScreen
	err := c.doJSON(http.MethodGet, "/v1/sessions/"+url.PathEscape(sid)+"/screen?"+q.Encode(), nil, &out)
	return out, err
}
