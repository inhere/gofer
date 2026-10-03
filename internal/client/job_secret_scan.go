package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/inhere/gofer/internal/jobstore"
)

type JobSecretScanRequest struct {
	Literals []string `json:"literals,omitempty"`
	Patterns []string `json:"patterns,omitempty"`
	Project  string   `json:"project,omitempty"`
	Since    int64    `json:"since,omitempty"`
}

func (c *Client) ScanJobSecrets(req JobSecretScanRequest) (jobstore.SecretScanReport, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return jobstore.SecretScanReport{}, fmt.Errorf("encode secret scan request: %w", err)
	}
	var out jobstore.SecretScanReport
	if err := c.doJSON(http.MethodPost, "/v1/jobs/secret-scan", bytes.NewReader(raw), &out); err != nil {
		return jobstore.SecretScanReport{}, err
	}
	return out, nil
}
