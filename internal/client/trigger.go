package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/inhere/gofer/internal/trigger"
)

// Inbound trigger client: the redacted read views (trigger.read) and the operator's
// self-test of the external invoke endpoint. The view shapes are the trigger package's,
// shared with the server.

// ListTriggers is GET /v1/triggers.
func (c *Client) ListTriggers() ([]trigger.View, error) {
	var out struct {
		Triggers []trigger.View `json:"triggers"`
	}
	if err := c.doJSON(http.MethodGet, "/v1/triggers", nil, &out); err != nil {
		return nil, err
	}
	return out.Triggers, nil
}

// GetTrigger is GET /v1/triggers/{name}.
func (c *Client) GetTrigger(name string) (trigger.View, error) {
	var out trigger.View
	err := c.doJSON(http.MethodGet, "/v1/triggers/"+url.PathEscape(name), nil, &out)
	return out, err
}

// TriggerDeliveries is GET /v1/triggers/{name}/deliveries (limit <= 0: the server default).
func (c *Client) TriggerDeliveries(name string, limit int) ([]trigger.DeliveryView, error) {
	path := "/v1/triggers/" + url.PathEscape(name) + "/deliveries"
	if limit > 0 {
		path += "?limit=" + strconv.Itoa(limit)
	}
	var out struct {
		Deliveries []trigger.DeliveryView `json:"deliveries"`
	}
	if err := c.doJSON(http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Deliveries, nil
}

// TriggerInvokeResult is the answer of POST /v1/triggers/{name}/invoke.
type TriggerInvokeResult struct {
	Trigger   string `json:"trigger"`
	RequestID string `json:"request_id"`
	JobID     string `json:"job_id,omitempty"`
	Duplicate bool   `json:"duplicate"`
	Status    string `json:"status,omitempty"`
}

// InvokeTrigger posts body to the trigger's invoke endpoint with exactly the given headers.
// The endpoint is authenticated by the signature in those headers, not by a gofer token, so
// this request deliberately carries no Authorization header.
func (c *Client) InvokeTrigger(name string, body []byte, headers map[string]string) (TriggerInvokeResult, error) {
	var out TriggerInvokeResult
	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/v1/triggers/"+url.PathEscape(name)+"/invoke", bytes.NewReader(body))
	if err != nil {
		return out, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		c.logRequest(http.MethodPost, "/v1/triggers/"+url.PathEscape(name)+"/invoke", 0, start, err)
		return out, fmt.Errorf("request POST /v1/triggers/%s/invoke: %w", name, err)
	}
	// The signature (not a gofer token) authenticates this request, so a 401 here is
	// a real incident: leave it in the client request log like any other call.
	c.logRequest(http.MethodPost, "/v1/triggers/"+url.PathEscape(name)+"/invoke", resp.StatusCode, start, nil)
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return out, fmt.Errorf("read response: %w", err)
	}
	if err := errorFor(resp.StatusCode, data); err != nil {
		return out, err
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, fmt.Errorf("decode response: %w", err)
	}
	return out, nil
}
