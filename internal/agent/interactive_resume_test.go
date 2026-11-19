package agent

import (
	"slices"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

func TestInteractiveResumeArgvOmitsGlobalArgs(t *testing.T) {
	ac := config.AgentConfig{
		Type: TypeCLIAgent, Command: "tool",
		GlobalArgs:               []string{"--sandbox", "off"},
		SessionResumeInteractive: []string{"--resume", "{{session_id}}"},
	}
	got := InteractiveResumeArgv(ac, "sid-1")
	if slices.Contains(got, "--sandbox") || slices.Contains(got, "off") {
		t.Fatalf("GlobalArgs must not appear in a hand-typed command: %v", got)
	}
	if len(got) < 2 || got[0] != "tool" || !slices.Contains(got, "sid-1") {
		t.Fatalf("argv = %v", got)
	}
	if InteractiveResumeArgv(config.AgentConfig{Type: TypeCLIAgent, Command: "tool"}, "x") != nil {
		t.Fatal("no resume template must give no argv")
	}
}
