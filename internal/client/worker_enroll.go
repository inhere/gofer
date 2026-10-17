package client

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// enrollSecretHeader carries the raw enroll secret on poll / confirm / cancel.
const enrollSecretHeader = "X-Gofer-Enroll-Secret"

// EnrollHost is the host description a joining worker sends (display only).
type EnrollHost struct {
	Hostname        string   `json:"hostname,omitempty"`
	OS              string   `json:"os,omitempty"`
	Arch            string   `json:"arch,omitempty"`
	GoferVersion    string   `json:"gofer_version,omitempty"`
	ProtocolVersion string   `json:"protocol_version,omitempty"`
	Agents          []string `json:"agents,omitempty"`
	Roots           []string `json:"roots,omitempty"`
}

// EnrollRequest is the body of POST /v1/worker-enroll. SecretHash is the SHA-256 hex of
// the enroll secret (see NewEnrollSecret); the raw secret never leaves the worker except
// in the X-Gofer-Enroll-Secret header of later calls.
type EnrollRequest struct {
	RequestedID string     `json:"requested_id,omitempty"`
	Labels      []string   `json:"labels,omitempty"`
	Host        EnrollHost `json:"host"`
	SecretHash  string     `json:"secret_hash"`
}

// EnrollTicket is the server's answer to a new request.
type EnrollTicket struct {
	EnrollID    string `json:"enroll_id"`
	PairingCode string `json:"pairing_code"`
	DecideUntil int64  `json:"decide_until"`
}

// EnrollPoll is one poll result. WorkerToken is set only for an accepted request.
type EnrollPoll struct {
	State        string `json:"state"`
	WorkerID     string `json:"worker_id,omitempty"`
	WorkerToken  string `json:"worker_token,omitempty"`
	CollectUntil int64  `json:"collect_until,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

// Enrollment is one row of GET /v1/worker-enrollments (never carries a secret or token).
type Enrollment struct {
	ID           string     `json:"id"`
	State        string     `json:"state"`
	RequestedID  string     `json:"requested_id,omitempty"`
	WorkerID     string     `json:"worker_id,omitempty"`
	Labels       []string   `json:"labels"`
	Projects     []string   `json:"projects"`
	Host         EnrollHost `json:"host"`
	Source       string     `json:"source"`
	CreatedAt    int64      `json:"created_at"`
	DecideUntil  int64      `json:"decide_until"`
	CollectUntil int64      `json:"collect_until,omitempty"`
	DecidedAt    int64      `json:"decided_at,omitempty"`
	DecidedBy    string     `json:"decided_by,omitempty"`
	Reason       string     `json:"reason,omitempty"`
	CollectedAt  int64      `json:"collected_at,omitempty"`
}

// EnrollmentList is the body of GET /v1/worker-enrollments.
type EnrollmentList struct {
	Enabled      bool         `json:"enabled"`
	PendingCount int          `json:"pending_count"`
	Items        []Enrollment `json:"items"`
}

// EnrollAccepted is the answer to an accept call (no token).
type EnrollAccepted struct {
	WorkerID     string `json:"worker_id"`
	CollectUntil int64  `json:"collect_until"`
}

// NewEnrollSecret returns a fresh random enroll secret and its SHA-256 hex hash.
func NewEnrollSecret() (secret, hash string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", fmt.Errorf("generate enroll secret: %w", err)
	}
	secret = hex.EncodeToString(b)
	return secret, EnrollSecretHash(secret), nil
}

// EnrollSecretHash is the SHA-256 hex digest the server stores for a secret.
func EnrollSecretHash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// anonJSON performs a request to an anonymous enrollment route: no bearer token and no
// session header are attached, so a join never hands a credential to the server it
// is asking for one. timeout > the client default lengthens the deadline (long poll).
func (c *Client) anonJSON(method, path, secret string, body any, timeout time.Duration, out any) error {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.baseURL+path, rd)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if secret != "" {
		req.Header.Set(enrollSecretHeader, secret)
	}
	hc := c.http
	if timeout > hc.Timeout {
		clone := *hc
		clone.Timeout = timeout
		hc = &clone
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("request %s %s: %w", method, path, err)
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

// Enroll starts a self-enrollment (POST /v1/worker-enroll, anonymous).
func (c *Client) Enroll(req EnrollRequest) (EnrollTicket, error) {
	var out EnrollTicket
	if err := c.anonJSON(http.MethodPost, "/v1/worker-enroll", "", req, 0, &out); err != nil {
		return EnrollTicket{}, err
	}
	return out, nil
}

// PollEnroll long-polls the request for up to wait (the server caps one poll at 25 s).
func (c *Client) PollEnroll(id, secret string, wait time.Duration) (EnrollPoll, error) {
	secs := int(wait / time.Second)
	path := fmt.Sprintf("/v1/worker-enroll/%s?wait=%d", url.PathEscape(id), max(secs, 0))
	var out EnrollPoll
	if err := c.anonJSON(http.MethodGet, path, secret, nil, wait+15*time.Second, &out); err != nil {
		return EnrollPoll{}, err
	}
	return out, nil
}

// ConfirmEnroll acknowledges that the token has been written locally.
func (c *Client) ConfirmEnroll(id, secret string) error {
	return c.anonJSON(http.MethodPost, "/v1/worker-enroll/"+url.PathEscape(id)+"/confirm", secret, struct{}{}, 0, nil)
}

// CancelEnroll gives the request up.
func (c *Client) CancelEnroll(id, secret string) error {
	return c.anonJSON(http.MethodPost, "/v1/worker-enroll/"+url.PathEscape(id)+"/cancel", secret, struct{}{}, 0, nil)
}

// ListEnrollments fetches GET /v1/worker-enrollments (needs worker.manage).
func (c *Client) ListEnrollments() (EnrollmentList, error) {
	var out EnrollmentList
	if err := c.doJSON(http.MethodGet, "/v1/worker-enrollments", nil, &out); err != nil {
		return EnrollmentList{}, err
	}
	return out, nil
}

// AcceptEnrollment accepts a pending request with the pairing code read off the worker's
// terminal.
func (c *Client) AcceptEnrollment(id, pairingCode, workerID string, labels, projects []string) (EnrollAccepted, error) {
	raw, err := json.Marshal(struct {
		PairingCode string   `json:"pairing_code"`
		WorkerID    string   `json:"worker_id"`
		Labels      []string `json:"labels,omitempty"`
		Projects    []string `json:"projects,omitempty"`
	}{pairingCode, workerID, labels, projects})
	if err != nil {
		return EnrollAccepted{}, err
	}
	var out EnrollAccepted
	if err := c.doJSON(http.MethodPost, "/v1/worker-enrollments/"+url.PathEscape(id)+"/accept", bytes.NewReader(raw), &out); err != nil {
		return EnrollAccepted{}, err
	}
	return out, nil
}

// RejectEnrollment rejects a pending request, optionally blocking its source for
// blockSourceMin minutes.
func (c *Client) RejectEnrollment(id, reason string, blockSourceMin int) error {
	raw, err := json.Marshal(struct {
		Reason         string `json:"reason,omitempty"`
		BlockSourceMin int    `json:"block_source_min,omitempty"`
	}{reason, blockSourceMin})
	if err != nil {
		return err
	}
	return c.doJSON(http.MethodPost, "/v1/worker-enrollments/"+url.PathEscape(id)+"/reject", bytes.NewReader(raw), nil)
}
