package client

import (
	"net/http"
	"net/url"
)

// TrackerRepoSyncResult is the server's answer to a dispatched `repo sync` (TRK-05).
type TrackerRepoSyncResult struct {
	JobID      string `json:"job_id"`
	TrackerID  string `json:"tracker_id"`
	ProjectKey string `json:"project_key"`
	Runner     string `json:"runner"`
	Cwd        string `json:"cwd"`
}

// SyncTrackerRepo asks the server to run `gofer repo sync` in the mirrored repository
// trackerID (POST /v1/tracker/repos/{tracker_id}/sync; person credentials only).
func (c *Client) SyncTrackerRepo(trackerID string) (TrackerRepoSyncResult, error) {
	var res TrackerRepoSyncResult
	err := c.doJSON(http.MethodPost, "/v1/tracker/repos/"+url.PathEscape(trackerID)+"/sync", nil, &res)
	return res, err
}
