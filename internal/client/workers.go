package client

import (
	"net/http"
	"net/url"
)

// WorkerMeta is one worker returned by GET /v1/meta. It is deliberately a
// client-side wire type so commands can inspect the server without importing
// the HTTP API package.
type WorkerMeta struct {
	ID        string            `json:"id"`
	Labels    []string          `json:"labels,omitempty"`
	Projects  []string          `json:"projects,omitempty"`
	Agents    []string          `json:"agents,omitempty"`
	AgentCaps []RunnerAgentMeta `json:"agent_caps,omitempty"`
	Connected bool              `json:"connected"`
}

type WorkerDetail struct {
	WorkerID  string              `json:"worker_id"`
	Connected bool                `json:"connected"`
	Worker    *RunnerWorkerDetail `json:"worker,omitempty"`
}

type RunnerWorkerDetail struct {
	LastHeartbeat   int64    `json:"last_heartbeat"`
	InFlight        int      `json:"in_flight"`
	Labels          []string `json:"labels,omitempty"`
	Projects        []string `json:"projects,omitempty"`
	Agents          []string `json:"agents,omitempty"`
	ProtocolVersion int      `json:"protocol_version,omitempty"`
	GoferVersion    string   `json:"gofer_version,omitempty"`
	PolicyPending   bool     `json:"policy_pending,omitempty"`
	PolicyRev       int64    `json:"policy_rev,omitempty"`
	AppliedRev      int64    `json:"applied_rev,omitempty"`
	PolicyRejected  []struct {
		Key    string `json:"key"`
		Reason string `json:"reason"`
	} `json:"policy_rejected,omitempty"`
	PolicyDegraded []struct {
		Key  string `json:"key"`
		Gate string `json:"gate"`
	} `json:"policy_degraded,omitempty"`
}

func (c *Client) GetWorker(id string) (WorkerDetail, error) {
	var out WorkerDetail
	err := c.doJSON(http.MethodGet, "/v1/workers/"+url.PathEscape(id), nil, &out)
	return out, err
}

func (c *Client) GetWorkerProjects(id string) (WorkerDetail, error) {
	var out WorkerDetail
	err := c.doJSON(http.MethodGet, "/v1/workers/"+url.PathEscape(id)+"/projects", nil, &out)
	return out, err
}

// ListWorkers fetches the server's worker registry from GET /v1/meta. This
// includes configured workers even when they are currently disconnected.
func (c *Client) ListWorkers() ([]WorkerMeta, error) {
	var resp struct {
		Workers []WorkerMeta `json:"workers"`
	}
	if err := c.doJSON(http.MethodGet, "/v1/meta", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Workers, nil
}
