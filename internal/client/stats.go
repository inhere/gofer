package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

// StatsBackfillRequest is one POST /v1/stats/backfill batch.
type StatsBackfillRequest struct {
	Since      int64  `json:"since,omitempty"`
	AfterEnded int64  `json:"after_ended,omitempty"`
	AfterID    string `json:"after_id,omitempty"`
	Limit      int    `json:"limit,omitempty"`
	Force      bool   `json:"force,omitempty"`
}

// StatsBackfillResult is one batch's outcome; Done=false means call again with the
// returned cursor (AfterEnded / AfterID).
type StatsBackfillResult struct {
	Scanned    int    `json:"scanned"`
	Written    int    `json:"written"`
	Failed     int    `json:"failed"`
	WithSignal int    `json:"with_signal"`
	WithGit    int    `json:"with_git"`
	AfterEnded int64  `json:"after_ended"`
	AfterID    string `json:"after_id"`
	Done       bool   `json:"done"`
}

// StatsBackfill runs one dashboard-metrics backfill batch on the server.
func (c *Client) StatsBackfill(req StatsBackfillRequest) (StatsBackfillResult, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return StatsBackfillResult{}, fmt.Errorf("encode backfill: %w", err)
	}
	var out StatsBackfillResult
	err = c.doJSON(http.MethodPost, "/v1/stats/backfill", bytes.NewReader(body), &out)
	return out, err
}
