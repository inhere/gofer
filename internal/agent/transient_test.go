package agent

import (
	"reflect"
	"regexp"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

func TestTransientPatternsBuiltinAndOverride(t *testing.T) {
	for _, command := range []string{"codex", "claude", "omp", `C:\tools\CODEX.EXE`, "/opt/bin/claude"} {
		t.Run(command, func(t *testing.T) {
			cfg := &config.Config{Agents: map[string]config.AgentConfig{
				"custom": {Type: TypeCLIAgent, Command: command},
			}}
			ac, ok := ResolveAgent(cfg, "custom")
			if !ok || len(ac.TransientErrorPatterns) != 1 {
				t.Fatalf("command %q patterns = %v, found = %v", command, ac.TransientErrorPatterns, ok)
			}
			re := regexp.MustCompile(ac.TransientErrorPatterns[0])
			for _, message := range []string{"Selected model is at capacity", "RATE LIMIT", "overloaded", "too many requests", "HTTP 429", "HTTP 503", "econnreset", "Connection reset", "stream disconnected", "temporarily unavailable"} {
				if !re.MatchString(message) {
					t.Errorf("pattern did not match %q", message)
				}
			}
			for _, message := range []string{"compile error", "14290", "5030"} {
				if re.MatchString(message) {
					t.Errorf("pattern incorrectly matched %q", message)
				}
			}
			for _, override := range [][]string{{"provider busy"}, {}} {
				cfg.Agents["custom"] = config.AgentConfig{Type: TypeCLIAgent, Command: command, TransientErrorPatterns: override}
				ac, _ = ResolveAgent(cfg, "custom")
				if !reflect.DeepEqual(ac.TransientErrorPatterns, override) {
					t.Fatalf("override = %#v, want %#v", ac.TransientErrorPatterns, override)
				}
			}
		})
	}
	ac, _ := ResolveAgent(&config.Config{Agents: map[string]config.AgentConfig{
		"custom": {Type: TypeCLIAgent, Command: "unknown-agent"},
	}}, "custom")
	if len(ac.TransientErrorPatterns) != 0 {
		t.Fatalf("unknown command acquired built-in patterns: %v", ac.TransientErrorPatterns)
	}
}
