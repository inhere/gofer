package job

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
)

func TestAdmitDualModeAgentBothWays(t *testing.T) {
	cfg := interactiveAdmissionConfig(t.TempDir())
	p := cfg.Projects["self"]
	p.AllowedAgents = append(p.AllowedAgents, "codex")
	p.InteractiveAllowedAgents = append(p.InteractiveAllowedAgents, "codex")
	cfg.Projects["self"] = p
	cfg.Agents["codex"] = config.AgentConfig{Type: agent.TypeCLIAgent, Command: "codex", Args: []string{"exec", "{{prompt}}"}, InteractiveArgs: []string{"tui"}}
	s := &Service{}

	if _, err := s.Validate(cfg, JobRequest{ProjectKey: "self", Agent: "codex", Runner: "local", Prompt: "hello"}, false); err != nil {
		t.Fatalf("batch admission: %v", err)
	}
	batch, err := agent.BuildFrom(cfg, "codex", "hello", nil, agent.Vars{}, agent.BuildOptions{})
	if err != nil || !reflect.DeepEqual(batch.Args, []string{"exec", "hello"}) {
		t.Fatalf("batch argv = %#v, err=%v; want [exec hello]", batch.Args, err)
	}

	if _, err := s.Validate(cfg, JobRequest{ProjectKey: "self", Agent: "codex", Runner: "local", Interactive: true, Prompt: "hello"}, false); err != nil {
		t.Fatalf("interactive admission: %v", err)
	}
	interactive, err := agent.BuildFrom(cfg, "codex", "hello", nil, agent.Vars{}, agent.BuildOptions{AllowEmptyPrompt: true, Interactive: true})
	if err != nil || !reflect.DeepEqual(interactive.Args, []string{"tui"}) {
		t.Fatalf("interactive argv = %#v, err=%v; want [tui]", interactive.Args, err)
	}
}

func TestAdmitRejectsInteractiveWithoutMode(t *testing.T) {
	cfg := interactiveAdmissionConfig(t.TempDir())
	_, err := (&Service{}).Validate(cfg, JobRequest{ProjectKey: "self", Agent: "plain", Runner: "local", Interactive: true, Prompt: "hi"}, false)
	if !errors.Is(err, ErrInvalidRequest) || !strings.Contains(err.Error(), "has no interactive mode") {
		t.Fatalf("err = %v, want no interactive mode", err)
	}
}

func TestAdmitRejectsBatchWithoutMode(t *testing.T) {
	cfg := interactiveAdmissionConfig(t.TempDir())
	_, err := (&Service{}).Validate(cfg, JobRequest{ProjectKey: "self", Agent: "tty-term", Runner: "local", Prompt: "hi"}, false)
	if !errors.Is(err, ErrInvalidRequest) || !strings.Contains(err.Error(), "has no batch mode") {
		t.Fatalf("err = %v, want no batch mode", err)
	}
}

func TestAdmitInteractiveNoLongerRequiresNoRawCmd(t *testing.T) {
	cfg := interactiveAdmissionConfig(t.TempDir())
	_, err := (&Service{}).Validate(cfg, JobRequest{ProjectKey: "self", Agent: "raw-term", Runner: "local", Interactive: true, Prompt: "hi"}, false)
	if err != nil {
		t.Fatalf("raw command capable interactive agent rejected: %v", err)
	}
}

func TestAdmitInteractiveStillRejectsCmdOverride(t *testing.T) {
	cfg := interactiveAdmissionConfig(t.TempDir())
	_, err := (&Service{}).Validate(cfg, JobRequest{ProjectKey: "self", Agent: "term", Runner: "local", Interactive: true, Cmd: []string{"sh"}}, false)
	if !errors.Is(err, ErrInvalidRequest) || !strings.Contains(err.Error(), "interactive job cannot override Cmd") {
		t.Fatalf("err = %v, want cmd override rejection", err)
	}
}
