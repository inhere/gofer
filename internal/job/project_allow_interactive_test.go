package job

import (
	"errors"
	"strings"
	"testing"
)

// TestProjectAllowInteractiveGate pins the AGT-02 project switch: allow_interactive
// is the on/off gate for interactive jobs and interactive_allowed_agents is only the
// optional narrowing on top of it (empty = no narrowing).
func TestProjectAllowInteractiveGate(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }

	t.Run("switch off rejects an interactive submission", func(t *testing.T) {
		cfg := interactiveAdmissionConfig(t.TempDir())
		p := cfg.Projects["self"]
		p.AllowInteractive = boolPtr(false) // explicit false wins over the legacy list
		cfg.Projects["self"] = p

		_, err := (&Service{}).Validate(cfg, JobRequest{
			ProjectKey: "self", Agent: "term", Runner: "local", Interactive: true, Prompt: "hi",
		}, false)
		if !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("Validate err = %v, want ErrInvalidRequest", err)
		}
		if want := `project "self" does not allow interactive jobs (allow_interactive)`; !strings.Contains(err.Error(), want) {
			t.Fatalf("Validate err = %v, want message containing %q", err, want)
		}
	})

	t.Run("switch on admits an interactive-capable agent without narrowing", func(t *testing.T) {
		cfg := interactiveAdmissionConfig(t.TempDir())
		p := cfg.Projects["self"]
		p.AllowInteractive = boolPtr(true)
		p.InteractiveAllowedAgents = nil // no narrowing: the switch alone is the gate
		cfg.Projects["self"] = p

		if _, err := (&Service{}).Validate(cfg, JobRequest{
			ProjectKey: "self", Agent: "term", Runner: "local", Interactive: true, Prompt: "hi",
		}, false); err != nil {
			t.Fatalf("Validate valid interactive job = %v, want nil", err)
		}

		// The agent-side gate is independent: a batch-only agent still has no mode.
		_, err := (&Service{}).Validate(cfg, JobRequest{
			ProjectKey: "self", Agent: "plain", Runner: "local", Interactive: true, Prompt: "hi",
		}, false)
		if want := `agent "plain" has no interactive mode`; err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("Validate err = %v, want message containing %q", err, want)
		}
	})

	t.Run("narrowing list rejects an interactive agent outside it", func(t *testing.T) {
		cfg := interactiveAdmissionConfig(t.TempDir())
		p := cfg.Projects["self"]
		p.AllowInteractive = boolPtr(true)
		p.InteractiveAllowedAgents = []string{"term"} // "term-unlisted" has a mode but is not listed
		cfg.Projects["self"] = p

		if _, err := (&Service{}).Validate(cfg, JobRequest{
			ProjectKey: "self", Agent: "term", Runner: "local", Interactive: true, Prompt: "hi",
		}, false); err != nil {
			t.Fatalf("Validate listed agent = %v, want nil", err)
		}

		_, err := (&Service{}).Validate(cfg, JobRequest{
			ProjectKey: "self", Agent: "term-unlisted", Runner: "local", Interactive: true, Prompt: "hi",
		}, false)
		if !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("Validate narrowed-out agent err = %v, want ErrInvalidRequest", err)
		}
		if want := `agent "term-unlisted" not in interactive_allowed_agents`; !strings.Contains(err.Error(), want) {
			t.Fatalf("Validate err = %v, want message containing %q", err, want)
		}
	})
}
