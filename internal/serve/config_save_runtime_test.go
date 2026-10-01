package serve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/core"
)

type y1Detector struct{}

func (y1Detector) Detect(candidates map[string]config.AgentConfig) map[string]agent.DetectResult {
	results := make(map[string]agent.DetectResult, len(candidates))
	for key := range candidates {
		results[key] = agent.DetectResult{Available: key == "claude"}
	}
	return results
}

func TestConfigSaveDoesNotPersistRuntimeOverlays(t *testing.T) {
	for _, existing := range []bool{true, false} {
		name := "first-create"
		if existing {
			name = "existing-file"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			baseDir := filepath.Join(dir, "base")
			if err := os.Mkdir(baseDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(baseDir, config.ProjectOverlayName), []byte("exchange_subdir: overlay-only\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "config.yaml")
			var cfg *config.Config
			if existing {
				original := "server:\n  web_dir: configured-web\nprojects:\n  base:\n    host_path: " + filepath.ToSlash(baseDir) + "\nagents:\n  own:\n    type: cli-agent\n    command: own-cli\n"
				if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
					t.Fatal(err)
				}
				var err error
				cfg, _, err = config.Load(path)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				cfg = &config.Config{
					Projects: map[string]config.ProjectConfig{"base": {HostPath: baseDir}},
					Agents:   map[string]config.AgentConfig{"own": {Type: agent.TypeCLIAgent, Command: "own-cli"}},
				}
				config.ApplyDefaults(cfg)
			}
			cfg.Storage.Root = filepath.Join(dir, "runtime-storage")
			mergeServeOpts(cfg, Opts{WebDir: "runtime-web", NoWeb: true})
			if warns := config.ApplyProjectOverlays(cfg); len(warns) != 0 {
				t.Fatalf("overlay warnings: %v", warns)
			}
			cr, err := core.Build(cfg, core.WithConfigPath(path), core.WithAgentDetector(y1Detector{}))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cr.Close() })
			if !cr.Config().IsInjectedAgent("claude") || cr.Config().Projects["base"].ExchangeSubdir != "overlay-only" {
				t.Fatal("runtime overlay and injected agent preconditions failed")
			}
			if err := cr.Projects.Add("added", config.ProjectConfig{HostPath: filepath.Join(dir, "added")}, false); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			got := string(raw)
			for _, want := range []string{"added:", "own:", "command: own-cli"} {
				if !strings.Contains(got, want) {
					t.Errorf("missing explicit value %q:\n%s", want, got)
				}
			}
			for _, leak := range []string{"overlay-only", "runtime-web", "runtime-storage", "claude:", "web_enabled: false"} {
				if strings.Contains(got, leak) {
					t.Errorf("runtime value %q persisted:\n%s", leak, got)
				}
			}
			if existing && !strings.Contains(got, "web_dir: configured-web") {
				t.Errorf("original web_dir was replaced:\n%s", got)
			}
			if !existing && strings.Contains(got, "server:") {
				t.Errorf("first create invented a server block:\n%s", got)
			}
			if err := cr.Update(func(next *config.Config) error {
				next.Server.WebDir = "explicit-web"
				next.Agents["own"] = config.AgentConfig{Type: agent.TypeCLIAgent, Command: "updated-cli"}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			raw, err = os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), "explicit-web") || !strings.Contains(string(raw), "updated-cli") {
				t.Fatalf("explicit server/agent edit was not persisted:\n%s", raw)
			}
		})
	}
}
