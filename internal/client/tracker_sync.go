package client

import (
	"bytes"
	"encoding/json"
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

// TrackerRenameResult is the server's answer to a tracker_id rename.
type TrackerRenameResult struct {
	TrackerID    string `json:"tracker_id"`
	OldTrackerID string `json:"old_tracker_id"`
	Renamed      bool   `json:"renamed"`
}

// RenameTrackerRepo asks the server to move a mirrored repository from a legacy UUID
// tracker_id to its derived short id (POST /v1/tracker/repos/{tracker_id}/rename).
//
// DEPRECATED(v0.126): remove in v0.129 (legacy UUID tracker_id migration).
func (c *Client) RenameTrackerRepo(oldID, newID string) (TrackerRenameResult, error) {
	var res TrackerRenameResult
	body, err := json.Marshal(map[string]string{"new_tracker_id": newID})
	if err != nil {
		return res, err
	}
	err = c.doJSON(http.MethodPost, "/v1/tracker/repos/"+url.PathEscape(oldID)+"/rename", bytes.NewReader(body), &res)
	return res, err
}
