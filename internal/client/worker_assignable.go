package client

import (
	"net/http"
	"net/url"
)

// AssignableProject is one project a worker may be dispatched to, as returned by
// GET /v1/workers/{id}/assignable. HostPath is the SERVER's logical path — what a
// worker's `roots` must map onto a local tree.
type AssignableProject struct {
	Key      string `json:"key"`
	HostPath string `json:"host_path,omitempty"`
}

// WorkerAssignable is the wizard's server-side briefing (CFG-05): the projects whose
// allowed_runners may route to this worker, plus the server and wire protocol
// versions it is onboarding against.
type WorkerAssignable struct {
	WorkerID        string              `json:"worker_id"`
	Projects        []AssignableProject `json:"projects"`
	ServerVersion   string              `json:"server_version,omitempty"`
	ProtocolVersion int                 `json:"protocol_version"`
}

// WorkerAssignable fetches GET /v1/workers/{id}/assignable — the read-only call
// `gofer worker init` makes before it can infer roots or probe agents. The endpoint
// requires a user caller or the worker's own token; a worker init runs with the
// latter (the token it is being handed).
func (c *Client) WorkerAssignable(workerID string) (WorkerAssignable, error) {
	var out WorkerAssignable
	path := "/v1/workers/" + url.PathEscape(workerID) + "/assignable"
	if err := c.doJSON(http.MethodGet, path, nil, &out); err != nil {
		return WorkerAssignable{}, err
	}
	return out, nil
}
