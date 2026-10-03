package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

// Worker upgrade states (mirror of workerupgrade.State*; the client keeps its own wire
// types so commands never import server packages).
const (
	WorkerUpgradePending    = "pending"
	WorkerUpgradeSucceeded  = "succeeded"
	WorkerUpgradeRolledBack = "rolled_back"
	WorkerUpgradeFailed     = "failed"
)

// WorkerUpgradeRecord is the latest remote-upgrade attempt of one worker. Times are
// unix milliseconds.
type WorkerUpgradeRecord struct {
	WorkerID      string `json:"worker_id"`
	UpgradeID     string `json:"upgrade_id"`
	State         string `json:"state"`
	FromVersion   string `json:"from_version,omitempty"`
	TargetVersion string `json:"target_version,omitempty"`
	TargetSHA256  string `json:"target_sha256,omitempty"`
	Force         bool   `json:"force,omitempty"`
	Error         string `json:"error,omitempty"`
	StartedAt     int64  `json:"started_at"`
	FinishedAt    int64  `json:"finished_at,omitempty"`
	DurationMS    int64  `json:"duration_ms,omitempty"`
}

// WorkerUpgradeOptions is the body of POST /v1/workers/{id}/upgrade. Source is
// "staged" (the binary uploaded with StageWorkerUpgrade) or "server" (the server's own
// executable; the default).
type WorkerUpgradeOptions struct {
	Source          string `json:"source,omitempty"`
	Force           bool   `json:"force,omitempty"`
	DrainTimeoutSec int    `json:"drain_timeout_sec,omitempty"`
	ReadyTimeoutSec int    `json:"ready_timeout_sec,omitempty"`
}

// workerUpgradeUploadTimeout bounds the binary upload (a gofer binary is tens of MB).
const workerUpgradeUploadTimeout = 10 * time.Minute

// workerUpgradeAcceptTimeout covers the server waiting for the worker's download +
// verification (the server's own wait is 5 minutes).
const workerUpgradeAcceptTimeout = 6 * time.Minute

// StageWorkerUpgrade uploads the file as worker id's upgrade binary. The server
// computes the digest itself; the returned values are what it stored.
func (c *Client) StageWorkerUpgrade(id, path string) (sha256 string, size int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	api := "/v1/workers/" + url.PathEscape(id) + "/upgrade/file"
	req, err := http.NewRequest(http.MethodPut, c.baseURL+api, f)
	if err != nil {
		return "", 0, fmt.Errorf("build request: %w", err)
	}
	req.ContentLength = st.Size()
	req.Header.Set("Content-Type", "application/octet-stream")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	hc := &http.Client{Timeout: workerUpgradeUploadTimeout}
	resp, err := hc.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("request PUT %s: %w", api, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, fmt.Errorf("read response: %w", err)
	}
	if err := errorFor(resp.StatusCode, data); err != nil {
		return "", 0, err
	}
	var out struct {
		SHA256 string `json:"sha256"`
		Size   int64  `json:"size"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", 0, fmt.Errorf("decode response: %w", err)
	}
	return out.SHA256, out.Size, nil
}

// StartWorkerUpgrade asks the server to upgrade worker id. It returns once the worker
// has accepted the new binary (HTTP 202) with the pending upgrade record; the final
// outcome is read from GetWorker(id).Upgrade.
func (c *Client) StartWorkerUpgrade(id string, opt WorkerUpgradeOptions) (WorkerUpgradeRecord, error) {
	body, err := json.Marshal(opt)
	if err != nil {
		return WorkerUpgradeRecord{}, err
	}
	api := "/v1/workers/" + url.PathEscape(id) + "/upgrade"
	req, err := http.NewRequest(http.MethodPost, c.baseURL+api, bytes.NewReader(body))
	if err != nil {
		return WorkerUpgradeRecord{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	hc := &http.Client{Timeout: workerUpgradeAcceptTimeout}
	resp, err := hc.Do(req)
	if err != nil {
		return WorkerUpgradeRecord{}, fmt.Errorf("request POST %s: %w", api, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return WorkerUpgradeRecord{}, fmt.Errorf("read response: %w", err)
	}
	if err := errorFor(resp.StatusCode, data); err != nil {
		return WorkerUpgradeRecord{}, err
	}
	var out struct {
		Upgrade WorkerUpgradeRecord `json:"upgrade"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return WorkerUpgradeRecord{}, fmt.Errorf("decode response: %w", err)
	}
	return out.Upgrade, nil
}
