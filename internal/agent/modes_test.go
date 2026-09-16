package agent

import (
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

func TestModesTable(t *testing.T) {
	tests := []struct {
		name               string
		ac                 config.AgentConfig
		batch, interactive bool
	}{
		{"batch", config.AgentConfig{Args: []string{"run", "{{prompt}}"}}, true, false},
		{"batch without prompt placeholder", config.AgentConfig{Args: []string{"env"}}, true, false},
		{"dual", config.AgentConfig{Args: []string{"{{prompt}}"}, InteractiveArgs: []string{}}, true, true},
		{"dual without prompt placeholder", config.AgentConfig{Args: []string{"run"}, InteractiveArgs: []string{"--tui"}}, true, true},
		{"legacy interactive-only", config.AgentConfig{Interactive: true}, false, true},
		{"exec", config.AgentConfig{Type: TypeExec}, true, false},
	}
	for _, tt := range tests {
		b, i := Modes(tt.ac)
		if b != tt.batch || i != tt.interactive {
			t.Errorf("%s: got (%v,%v)", tt.name, b, i)
		}
	}
}

func TestBuiltinClaudeCodexAreDualMode(t *testing.T) {
	for _, key := range []string{"claude", "codex"} {
		b, i := Modes(builtinTemplates[key])
		if !b || !i {
			t.Errorf("%s modes=(%v,%v)", key, b, i)
		}
	}
}

func TestLoadRejectsInteractiveWithPrompt(t *testing.T) {
	err := ValidateConfig(&config.Config{Agents: map[string]config.AgentConfig{"x": {Interactive: true, Args: []string{"{{prompt}}"}}}})
	if err == nil || !strings.Contains(err.Error(), "use interactive_args") {
		t.Fatalf("err=%v", err)
	}
}
func TestLoadRejectsPromptInInteractiveArgs(t *testing.T) {
	err := ValidateConfig(&config.Config{Agents: map[string]config.AgentConfig{"x": {InteractiveArgs: []string{"{{prompt}}"}}}})
	if err == nil || !strings.Contains(err.Error(), "interactive_args") {
		t.Fatalf("err=%v", err)
	}
}
func TestLoadRejectsExecInteractiveArgs(t *testing.T) {
	err := ValidateConfig(&config.Config{Agents: map[string]config.AgentConfig{"x": {Type: TypeExec, InteractiveArgs: []string{}}}})
	if err == nil || !strings.Contains(err.Error(), "exec") {
		t.Fatalf("err=%v", err)
	}
}
func TestTTYTemplatesStillInteractiveOnly(t *testing.T) {
	for _, key := range []string{"tty-claude", "tty-codex"} {
		b, i := Modes(builtinTemplates[key])
		if b || !i {
			t.Errorf("%s modes=(%v,%v)", key, b, i)
		}
	}
}
