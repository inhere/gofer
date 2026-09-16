package job

import (
	"errors"
	"strings"
	"testing"
)

// boolPtr is the shared *bool constructor for the AGT-02 project switch in this
// package's fixtures.
func boolPtr(b bool) *bool { return &b }

// TestProjectAllowInteractiveGate pins the AGT-02 project switch: allow_interactive is
// the ONLY project-level gate for interactive jobs. AGT-02 0.3 removed the
// per-agent narrowing list, so nothing here can narrow WHICH agents run them — the
// agent's own interactive mode is the only agent-side gate, and a project that wants to
// exclude an agent simply does not give it an interactive mode.
func TestProjectAllowInteractiveGate(t *testing.T) {
	t.Run("switch off rejects an interactive submission", func(t *testing.T) {
		cfg := interactiveAdmissionConfig(t.TempDir())
		p := cfg.Projects["self"]
		p.AllowInteractive = boolPtr(false)
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

	t.Run("unset switch stays closed", func(t *testing.T) {
		cfg := interactiveAdmissionConfig(t.TempDir())
		p := cfg.Projects["self"]
		p.AllowInteractive = nil // "not written" must never mean "inherit"/"allow"
		cfg.Projects["self"] = p

		_, err := (&Service{}).Validate(cfg, JobRequest{
			ProjectKey: "self", Agent: "term", Runner: "local", Interactive: true, Prompt: "hi",
		}, false)
		if want := `does not allow interactive jobs (allow_interactive)`; err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("Validate err = %v, want message containing %q", err, want)
		}
	})

	t.Run("switch on admits every interactive-capable agent", func(t *testing.T) {
		cfg := interactiveAdmissionConfig(t.TempDir())
		p := cfg.Projects["self"]
		p.AllowInteractive = boolPtr(true)
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
}
