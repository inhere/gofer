package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ReloadResult describes one successful configuration generation swap. Changed
// contains top-level configuration partitions whose values differ from the
// previous generation. RestartRequired contains the dotted keys classified by
// the editable policy as startup-only and changed by this reload.
type ReloadResult struct {
	Rev             int64    `json:"rev"`
	Path            string   `json:"path"`
	Changed         []string `json:"changed"`
	RestartRequired []string `json:"restart_required"`
	Error           string   `json:"error,omitempty"`
	// ReloadedAt is when this receipt was written (RFC3339Nano). Waiters compare
	// it with the time they signalled the process, so a server restart that
	// resets Rev cannot make a fresh receipt look stale.
	ReloadedAt time.Time `json:"reloaded_at"`
}

// WriteReloadResult atomically publishes the latest reload receipt. The caller
// chooses the component-specific run path; readers can therefore wait without
// scraping transient stdout/stderr.
func WriteReloadResult(path string, result ReloadResult) error {
	if path == "" {
		return fmt.Errorf("write reload result: empty path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create reload result directory: %w", err)
	}
	if result.ReloadedAt.IsZero() {
		result.ReloadedAt = time.Now()
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("encode reload result: %w", err)
	}
	tmp := path + fmt.Sprintf(".tmp-%d", os.Getpid())
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write reload result: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("publish reload result: %w", err)
	}
	return nil
}

func ReadReloadResult(path string) (ReloadResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ReloadResult{}, err
	}
	var result ReloadResult
	if err := json.Unmarshal(data, &result); err != nil {
		return ReloadResult{}, fmt.Errorf("decode reload result: %w", err)
	}
	return result, nil
}
