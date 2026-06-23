package config

import (
	"fmt"
	"os"
	"path/filepath"

	yaml "github.com/goccy/go-yaml"
)

// ProjectOverlayName is the per-project thin config file (E29/D1). It lives in
// the project dir and carries ONLY preference fields — server/storage/host_path/
// container_path 与准入字段 (allowed_agents/allowed_runners/allow_exec) 一律不在此
// (D2: 准入真源在全局 config, overlay 不得放权).
const ProjectOverlayName = ".gofer.project.yaml"

// ProjectOverlay is the decoded .gofer.project.yaml (D5 白名单). Every field is a
// pointer so "absent" (nil) is distinguishable from a zero value — only non-nil
// fields override the global ProjectConfig (D8).
type ProjectOverlay struct {
	ExchangeSubdir    *string `yaml:"exchange_subdir"`
	ResultSubdir      *string `yaml:"result_subdir"`
	DefaultAgent      *string `yaml:"default_agent"`
	MaxConcurrentJobs *int    `yaml:"max_concurrent_jobs"`
	CaptureDiff       *bool   `yaml:"capture_diff"`
	NotifyEnabled     *bool   `yaml:"notify_enabled"`
}

// forbiddenOverlayKeys are top-level keys that must NOT appear in an overlay
// (D2/D5). Presence is a config mistake → warn (not fatal): a project author
// cannot self-grant 准入 nor redefine the注册锚/server.
var forbiddenOverlayKeys = []string{
	"server", "storage", "projects", "agents", "runners",
	"host_path", "container_path",
	"allowed_agents", "allowed_runners", "allow_exec",
}

// MergeProjectConfig returns base with every non-nil overlay field applied (D8).
// Slice/准入/注册锚 fields are never touched (they are absent from ProjectOverlay).
func MergeProjectConfig(base ProjectConfig, ov ProjectOverlay) ProjectConfig {
	if ov.ExchangeSubdir != nil {
		base.ExchangeSubdir = *ov.ExchangeSubdir
	}
	if ov.ResultSubdir != nil {
		base.ResultSubdir = *ov.ResultSubdir
	}
	if ov.DefaultAgent != nil {
		base.DefaultAgent = *ov.DefaultAgent
	}
	if ov.MaxConcurrentJobs != nil {
		base.MaxConcurrentJobs = *ov.MaxConcurrentJobs
	}
	if ov.CaptureDiff != nil {
		base.CaptureDiff = ov.CaptureDiff
	}
	if ov.NotifyEnabled != nil {
		base.NotifyEnabled = ov.NotifyEnabled
	}
	return base
}

// ApplyProjectOverlays merges each project's .gofer.project.yaml into
// cfg.Projects IN PLACE (D6). Read dir = ContainerPath || HostPath (D4):
// gofer runs in-container, so the container path is read first. A missing file
// is skipped silently; a decode error or forbidden key appends a warning but
// never aborts (one bad project overlay must not take down serve). Returns the
// warnings for the caller to log.
func ApplyProjectOverlays(cfg *Config) []string {
	var warns []string
	for key, p := range cfg.Projects {
		dir := p.ContainerPath
		if dir == "" {
			dir = p.HostPath
		}
		if dir == "" {
			continue
		}
		path := filepath.Join(dir, ProjectOverlayName)
		data, err := os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				warns = append(warns, fmt.Sprintf("project %q: read overlay %s: %v (using global)", key, path, err))
			}
			continue // 不存在 → 纯走全局定义 (D9 向后兼容)
		}
		warns = append(warns, detectForbiddenOverlayKeys(key, data)...)
		var ov ProjectOverlay
		if err := yaml.Unmarshal(data, &ov); err != nil {
			warns = append(warns, fmt.Sprintf("project %q: decode overlay %s: %v (skipped)", key, path, err))
			continue
		}
		cfg.Projects[key] = MergeProjectConfig(p, ov)
	}
	return warns
}

// detectForbiddenOverlayKeys decodes the overlay loosely and warns on any
// forbidden top-level key (D2/D5). The forbidden key is otherwise ignored
// (ProjectOverlay has no field for it), so this only surfaces the mistake.
func detectForbiddenOverlayKeys(key string, data []byte) []string {
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil // 真正的解码错误由调用方的 strict 解码报告
	}
	var warns []string
	for _, k := range forbiddenOverlayKeys {
		if _, ok := raw[k]; ok {
			warns = append(warns, fmt.Sprintf("project %q: overlay key %q is not allowed (准入/server 留全局, D2) — ignored", key, k))
		}
	}
	return warns
}
