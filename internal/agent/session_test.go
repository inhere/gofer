package agent

import (
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// TestRenderSessionID verifies the {{session_id}} placeholder substitutes (and
// stays one argv element).
func TestRenderSessionID(t *testing.T) {
	got := Render([]string{"--session-id", "{{session_id}}"}, Vars{SessionID: "u-123"})
	if len(got) != 2 || got[0] != "--session-id" || got[1] != "u-123" {
		t.Fatalf("render = %#v, want [--session-id u-123]", got)
	}
}

// TestBuiltinSessionDefaultsClaude: a declared claude agent with no session
// fields gets the built-in inject + resume defaults; capture stays empty.
func TestBuiltinSessionDefaultsClaude(t *testing.T) {
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		"claude": {Type: TypeCLIAgent, Command: "claude", Args: []string{"-p", "{{prompt}}"}},
	}}
	ac, ok := ResolveAgent(cfg, "claude")
	if !ok {
		t.Fatal("claude should resolve")
	}
	if len(ac.SessionInject) != 2 || ac.SessionInject[0] != "--session-id" || ac.SessionInject[1] != "{{session_id}}" {
		t.Errorf("SessionInject = %#v, want [--session-id {{session_id}}]", ac.SessionInject)
	}
	if ac.SessionCapture != "" {
		t.Errorf("claude SessionCapture = %q, want empty (claude uses inject)", ac.SessionCapture)
	}
	if len(ac.SessionResume) != 4 || ac.SessionResume[0] != "--resume" {
		t.Errorf("SessionResume = %#v, want claude resume template", ac.SessionResume)
	}
}

// TestBuiltinSessionDefaultsCodex: a declared codex agent gets the built-in
// capture regex + resume template; inject stays empty.
func TestBuiltinSessionDefaultsCodex(t *testing.T) {
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		"codex": {Type: TypeCLIAgent, Command: "codex", Args: []string{"exec", "{{prompt}}"}},
	}}
	ac, ok := ResolveAgent(cfg, "codex")
	if !ok {
		t.Fatal("codex should resolve")
	}
	if len(ac.SessionInject) != 0 {
		t.Errorf("codex SessionInject = %#v, want empty (codex uses capture)", ac.SessionInject)
	}
	if ac.SessionCapture != `session id:\s*([0-9a-f-]+)` {
		t.Errorf("codex SessionCapture = %q, want the built-in regex", ac.SessionCapture)
	}
	if len(ac.SessionResume) != 4 || ac.SessionResume[0] != "exec" || ac.SessionResume[1] != "resume" {
		t.Errorf("SessionResume = %#v, want codex resume template", ac.SessionResume)
	}
}

// TestExplicitSessionConfigWinsOverBuiltin: an explicit session field is NOT
// overwritten by the built-in default (per-field, independently).
func TestExplicitSessionConfigWinsOverBuiltin(t *testing.T) {
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		"claude": {
			Type:           TypeCLIAgent,
			Command:        "claude",
			SessionInject:  []string{"--sid", "{{session_id}}"},
			SessionCapture: `custom:\s*(\S+)`,
		},
	}}
	ac, _ := ResolveAgent(cfg, "claude")
	if len(ac.SessionInject) != 2 || ac.SessionInject[0] != "--sid" {
		t.Errorf("explicit SessionInject overwritten: %#v", ac.SessionInject)
	}
	if ac.SessionCapture != `custom:\s*(\S+)` {
		t.Errorf("explicit SessionCapture overwritten: %q", ac.SessionCapture)
	}
	// SessionResume was unset -> filled from built-in.
	if len(ac.SessionResume) != 4 || ac.SessionResume[0] != "--resume" {
		t.Errorf("SessionResume should default-fill: %#v", ac.SessionResume)
	}
}

// TestNonSessionAgentUnchanged: an agent with no built-in default keeps all
// session fields empty.
func TestNonSessionAgentUnchanged(t *testing.T) {
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		"other": {Type: TypeCLIAgent, Command: "other", Args: []string{"{{prompt}}"}},
	}}
	ac, _ := ResolveAgent(cfg, "other")
	if len(ac.SessionInject) != 0 || ac.SessionCapture != "" || len(ac.SessionResume) != 0 {
		t.Errorf("non-session agent gained session fields: %#v", ac)
	}
}
