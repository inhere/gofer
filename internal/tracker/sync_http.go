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
	baseIssues := indexIssues(base.Issues)
	for _, issue := range issues {
		if old, ok := baseIssues[issue.ID]; ok && string(mustJSON(old)) == string(mustJSON(issue)) {
			continue
		}
		b, _ := json.Marshal(issue)
		issueRecords = append(issueRecords, map[string]any{"id": issue.ID, "body": json.RawMessage(b), "rev": 1, "updated_at": issue.UpdatedAt})
	}
	baseMem := indexMemories(base.Memories)
	memoryRecords := make([]map[string]any, 0, len(memories))
	for _, memory := range memories {
		if old, ok := baseMem[memory.Key]; ok && string(mustJSON(old)) == string(mustJSON(memory)) {
			continue
		}
		b, _ := json.Marshal(memory)
		memoryRecords = append(memoryRecords, map[string]any{"id": memory.Key, "body": json.RawMessage(b), "rev": 1, "updated_at": memory.UpdatedAt})
	}
	localMem := indexMemories(memories)
	for key := range baseMem {
		if _, ok := localMem[key]; !ok {
			memoryRecords = append(memoryRecords, map[string]any{"id": key, "body": json.RawMessage("{}"), "rev": 1, "updated_at": Now(), "deleted": true, "deleted_at": Now(), "deleted_by": "local"})
		}
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
	var remoteMemories []ServerMemory
	for _, item := range wire.Issues {
		var issue Issue
		if err := json.Unmarshal(item.Body, &issue); err == nil {
			remote.Issues = append(remote.Issues, issue)
		}
	}
	for _, item := range wire.Memories {
		var memory Memory
		_ = json.Unmarshal(item.Body, &memory)
		remoteMemories = append(remoteMemories, ServerMemory{Memory: memory, Deleted: item.Deleted, DeletedAt: item.DeletedAt, DeletedBy: item.DeletedBy})
		if item.Deleted {
			continue
		}
		if err := json.Unmarshal(item.Body, &memory); err == nil {
			remote.Memories = append(remote.Memories, memory)
		}
	}
	if len(base.Issues) == 0 && len(base.Memories) == 0 {
		base = local
	}
	merged, report := ThreeWayMerge(base, local, remote)
	serverMemBase := make([]ServerMemory, 0, len(base.Memories))
	for _, m := range base.Memories {
		serverMemBase = append(serverMemBase, ServerMemory{Memory: m})
	}
	serverMemLocal := make([]ServerMemory, 0, len(local.Memories))
	for _, m := range local.Memories {
		serverMemLocal = append(serverMemLocal, ServerMemory{Memory: m})
	}
	serverMemMerged, memReport := MergeServerMemories(serverMemBase, serverMemLocal, remoteMemories)
	report.Conflicts = append(report.Conflicts, memReport.Conflicts...)
	merged.Memories = nil
	for _, m := range serverMemMerged {
		if !m.Deleted {
			merged.Memories = append(merged.Memories, m.Memory)
		}
	}
	deletedRemote := map[string]bool{}
	for _, item := range wire.Memories {
		if item.Deleted {
			deletedRemote[item.ID] = true
		}
	}
	if len(deletedRemote) > 0 {
		kept := merged.Memories[:0]
		for _, m := range merged.Memories {
			if !deletedRemote[m.Key] {
				kept = append(kept, m)
			}
		}
		merged.Memories = kept
	}
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
