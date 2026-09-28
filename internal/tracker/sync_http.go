package tracker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type syncHTTPResponse struct {
	Issues []struct {
		ID        string          `json:"id"`
		Body      json.RawMessage `json:"body"`
		Rev       int64           `json:"rev"`
		UpdatedAt string          `json:"updated_at"`
	} `json:"issues"`
	Memories []struct {
		ID        string          `json:"id"`
		Body      json.RawMessage `json:"body"`
		Rev       int64           `json:"rev"`
		UpdatedAt string          `json:"updated_at"`
		Deleted   bool            `json:"deleted"`
		DeletedAt string          `json:"deleted_at"`
		DeletedBy string          `json:"deleted_by"`
	} `json:"memories"`
}

type syncMeta struct {
	LastSyncAt string `json:"last_sync_at"`
	Summary    string `json:"summary"`
}

// SyncHTTP is the command/runtime sync path. It persists the last successful
// base under tracker/.local and never changes the repository files on an HTTP
// failure.
func SyncHTTP(ctx context.Context, s *Store, endpoint string) (SyncReport, error) {
	return SyncHTTPWithToken(ctx, s, endpoint, "")
}

func SyncHTTPWithToken(ctx context.Context, s *Store, endpoint, token string) (SyncReport, error) {
	if s == nil || endpoint == "" {
		return SyncReport{}, fmt.Errorf("tracker store and endpoint are required")
	}
	cfg, err := s.ReadConfig()
	if err != nil {
		return SyncReport{}, err
	}
	issues, err := s.ReadIssues()
	if err != nil {
		return SyncReport{}, err
	}
	memories, err := s.ReadMemories()
	if err != nil {
		return SyncReport{}, err
	}
	base, _ := readSyncBase(s.Dir)
	local := SyncSnapshot{Issues: issues, Memories: memories}
	issueRecords := make([]map[string]any, 0, len(issues))
	for _, issue := range issues {
		b, _ := json.Marshal(issue)
		issueRecords = append(issueRecords, map[string]any{"id": issue.ID, "body": json.RawMessage(b), "rev": 1, "updated_at": issue.UpdatedAt})
	}
	memoryRecords := make([]map[string]any, 0, len(memories))
	for _, memory := range memories {
		b, _ := json.Marshal(memory)
		memoryRecords = append(memoryRecords, map[string]any{"id": memory.Key, "body": json.RawMessage(b), "rev": 1, "updated_at": memory.UpdatedAt})
	}
	payload := map[string]any{"tracker_id": cfg.TrackerID, "project_key": cfg.ProjectKey, "prefix": cfg.Prefix, "issues": issueRecords, "memories": memoryRecords}
	body, err := json.Marshal(payload)
	if err != nil {
		return SyncReport{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/v1/tracker/sync", bytes.NewReader(body))
	if err != nil {
		return SyncReport{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return SyncReport{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return SyncReport{}, fmt.Errorf("sync server %s: %s", resp.Status, string(b))
	}
	var wire syncHTTPResponse
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return SyncReport{}, err
	}
	remote := SyncSnapshot{}
	for _, item := range wire.Issues {
		var issue Issue
		if err := json.Unmarshal(item.Body, &issue); err == nil {
			remote.Issues = append(remote.Issues, issue)
		}
	}
	for _, item := range wire.Memories {
		if item.Deleted {
			continue
		}
		var memory Memory
		if err := json.Unmarshal(item.Body, &memory); err == nil {
			remote.Memories = append(remote.Memories, memory)
		}
	}
	if len(base.Issues) == 0 && len(base.Memories) == 0 {
		base = local
	}
	merged, report := ThreeWayMerge(base, local, remote)
	if err := s.WriteIssues(merged.Issues); err != nil {
		return report, err
	}
	if err := s.UpdateMemories(func([]Memory) ([]Memory, error) { return merged.Memories, nil }); err != nil {
		return report, err
	}
	if err := writeSyncBase(s.Dir, merged); err != nil {
		return report, err
	}
	meta, _ := json.Marshal(syncMeta{LastSyncAt: Now(), Summary: report.Summary})
	_ = atomicWrite(filepath.Join(s.Dir, ".local", "sync-status.json"), append(meta, '\n'))
	return report, nil
}

func readSyncBase(dir string) (SyncSnapshot, error) {
	b, err := os.ReadFile(filepath.Join(dir, ".local", "sync-base.jsonl"))
	if err != nil {
		return SyncSnapshot{}, err
	}
	var out SyncSnapshot
	err = json.Unmarshal(b, &out)
	return out, err
}
func writeSyncBase(dir string, snapshot SyncSnapshot) error {
	path := filepath.Join(dir, ".local", "sync-base.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, _ := json.Marshal(snapshot)
	return atomicWrite(path, append(b, '\n'))
}

func SyncContext(endpoint string) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 2*time.Second)
}
