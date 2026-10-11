package config

import (
	"fmt"

	yaml "github.com/goccy/go-yaml"
)

// DecodeWorkerConfig decodes and validates a worker.yaml read from path (path only
// names the file in errors): agent extends, removed keys, local paths, log defaults,
// tunnel and tools. Shared by the worker command and `gofer config lint`.
func DecodeWorkerConfig(path string, data []byte) (*WorkerConfig, error) {
	var wc WorkerConfig
	if err := yaml.Unmarshal(data, &wc); err != nil {
		return nil, fmt.Errorf("decode worker config %s: %w", path, err)
	}
	if err := CheckWorkerID(wc.WorkerID); err != nil {
		return nil, fmt.Errorf("invalid worker config %s: worker_id: %w", path, err)
	}
	// gofer-arsj: worker agents may also start from a built-in template.
	if err := ApplyAgentExtends(wc.Agents, data); err != nil {
		return nil, fmt.Errorf("invalid worker config %s: %w", path, err)
	}
	// Projects are a second yaml surface for the same fields as config.yaml (this file
	// is what a LEGACY-mode worker reads), so the removed-key rejection runs here too:
	// a worker.yaml still carrying interactive_allowed_agents must fail loudly rather
	// than silently lose its interactive switch.
	if err := RejectRemovedKeys(data); err != nil {
		return nil, fmt.Errorf("invalid worker config %s: %w", path, err)
	}
	if err := (&Config{Log: wc.Log, Storage: wc.Storage}).ValidateLocalPaths(); err != nil {
		return nil, fmt.Errorf("invalid worker config %s: %w", path, err)
	}
	if wc.Log.MaxSizeMB <= 0 {
		wc.Log.MaxSizeMB = 50
	}
	if wc.Log.MaxAgeDays <= 0 {
		wc.Log.MaxAgeDays = 14
	}
	if wc.Log.MaxBackups <= 0 {
		wc.Log.MaxBackups = 10
	}
	if err := ValidateWorkerTunnel(wc); err != nil {
		return nil, fmt.Errorf("validate worker config %s: %w", path, err)
	}
	if err := ValidateTools(wc.Tools); err != nil {
		return nil, fmt.Errorf("validate worker config %s: %w", path, err)
	}
	return &wc, nil
}
