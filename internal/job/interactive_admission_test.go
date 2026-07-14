package job

import (
	"errors"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
)

func TestValidateInteractiveAdmission(t *testing.T) {
	cfg := interactiveAdmissionConfig(t.TempDir())
	s := &Service{}

	cases := []struct {
		name    string
		req     JobRequest
		remote  bool
		wantMsg string
	}{
		{
			name: "non-interactive agent",
			req: JobRequest{
				ProjectKey: "self", Agent: "plain", Runner: "local",
				Interactive: true, Prompt: "hi",
			},
			wantMsg: `agent "plain" is not interactive`,
		},
		{
			name: "not in interactive allowlist",
			req: JobRequest{
				ProjectKey: "self", Agent: "term-unlisted", Runner: "local",
				Interactive: true, Prompt: "hi",
			},
			wantMsg: `agent "term-unlisted" not in interactive_allowed_agents`,
		},
		{
			name: "exec-type agent",
			req: JobRequest{
				ProjectKey: "self", Agent: "exec-web", Runner: "local",
				Interactive: true, Prompt: "hi",
			},
			wantMsg: "interactive agent must be no-raw-cmd and non-exec",
		},
		{
			name: "raw command capable agent",
			req: JobRequest{
				ProjectKey: "self", Agent: "raw-term", Runner: "local",
				Interactive: true, Prompt: "hi",
			},
			wantMsg: "interactive agent must be no-raw-cmd and non-exec",
		},
		{
			name: "cmd override",
			req: JobRequest{
				ProjectKey: "self", Agent: "term", Runner: "local",
				Interactive: true, Cmd: []string{"sh"},
			},
			wantMsg: "interactive job cannot override Cmd",
		},
		{
			name: "peer runner",
			req: JobRequest{
				ProjectKey: "self", Agent: "term", Runner: "peer",
				Interactive: true, Prompt: "hi",
			},
			remote:  true,
			wantMsg: "interactive not supported on peer runner",
		},
		{
			name: "recording without pty",
			req: JobRequest{
				ProjectKey: "self", Agent: "term", Runner: "local",
				RecordPty: true, Prompt: "hi",
			},
			wantMsg: "record_pty requires interactive=true",
		},
		{
			name: "recording when cast disabled",
			req: JobRequest{
				ProjectKey: "self", Agent: "term", Runner: "local",
				Interactive: true, RecordPty: true, Prompt: "hi",
			},
			wantMsg: "record_pty requires storage.cast.enabled=true",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.Validate(cfg, tc.req, tc.remote)
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("Validate err = %v, want ErrInvalidRequest", err)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("Validate err = %v, want message containing %q", err, tc.wantMsg)
			}
		})
	}

	t.Run("valid local interactive", func(t *testing.T) {
		_, err := s.Validate(cfg, JobRequest{
			ProjectKey: "self", Agent: "term", Runner: "local",
			Interactive: true, Prompt: "hi",
		}, false)
		if err != nil {
			t.Fatalf("Validate valid local interactive: %v", err)
		}
	})

	t.Run("valid worker interactive", func(t *testing.T) {
		_, err := s.Validate(cfg, JobRequest{
			ProjectKey: "self", Agent: "term", Runner: "remote-w1", WorkerID: "w1",
			Interactive: true, Prompt: "hi",
		}, true)
		if err != nil {
			t.Fatalf("Validate valid worker interactive: %v", err)
		}
	})

	t.Run("valid recorded interactive", func(t *testing.T) {
		cfg := interactiveAdmissionConfig(t.TempDir())
		cfg.Storage.Cast.Enabled = true
		_, err := s.Validate(cfg, JobRequest{
			ProjectKey: "self", Agent: "term", Runner: "local",
			Interactive: true, RecordPty: true, Prompt: "hi",
		}, false)
		if err != nil {
			t.Fatalf("Validate valid recorded interactive: %v", err)
		}
	})
}

// TestValidateRejectsInteractiveOnlyAgentOnNonInteractiveJob pins the reverse gate
// (T4): an interactive agent submitted as a NON-interactive job must be rejected at
// admission.
//
// Without the gate the request validates clean and BuildFrom renders the interactive
// agent's arg template — which is EMPTY for a terminal agent (it is launched bare and
// driven through the pty) — so the prompt is silently dropped and the job execs a bare
// CLI that sits at its own prompt until the job timeout. The BuildFrom assertion below
// documents that failure mode: it is the reason the gate exists, so do not "fix" this
// test by relaxing the gate.
func TestValidateRejectsInteractiveOnlyAgentOnNonInteractiveJob(t *testing.T) {
	cfg := interactiveAdmissionConfig(t.TempDir())
	s := &Service{}

	req := JobRequest{
		ProjectKey: "self", Agent: "tty-term", Runner: "local",
		Prompt: "remember 42", TimeoutSec: 30,
	}

	// Evidence: the non-interactive build path silently loses the prompt.
	resolved, berr := agent.BuildFrom(cfg, req.Agent, req.Prompt, nil, agent.Vars{}, agent.BuildOptions{})
	if berr != nil {
		t.Fatalf("BuildFrom err = %v, want nil (the drop is silent, not an error)", berr)
	}
	argv := append([]string{resolved.Command}, resolved.Args...)
	for _, a := range argv {
		if a == req.Prompt {
			t.Fatalf("prompt survived in argv %#v — the drop this gate guards against is gone", argv)
		}
	}
	t.Logf("BuildFrom(%q, prompt=%q) -> argv=%#v: prompt silently dropped", req.Agent, req.Prompt, argv)

	_, err := s.Validate(cfg, req, false)
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Validate err = %v, want ErrInvalidRequest", err)
	}
	if want := `agent "tty-term" is interactive-only`; !strings.Contains(err.Error(), want) {
		t.Fatalf("Validate err = %v, want message containing %q", err, want)
	}
}

// TestValidateInteractiveOnlyAgentGateAllowsResumeCarrier pins that the T4 gate does
// not hit the resume path. A resume submission mechanically carries Agent="exec" while
// its gate identity is the SOURCE agent (gateAgentOf/ResumeSourceAgent), and ResumeJob
// copies Interactive from the source — so an interactive source resumes as an
// interactive job and the gate's !req.Interactive half is false.
//
// (The end-to-end counterpart is TestResumeJobInteractiveSourceUsesInteractiveTemplate,
// which drives the real ResumeJob.)
func TestValidateInteractiveOnlyAgentGateAllowsResumeCarrier(t *testing.T) {
	cfg := interactiveAdmissionConfig(t.TempDir())
	s := &Service{}

	_, err := s.Validate(cfg, JobRequest{
		ProjectKey: "self", Agent: agent.ExecAgentKey, Runner: "local",
		ResumeSourceAgent: "term", Interactive: true,
		Cmd: []string{"echo", "--resume", "sid-1"},
	}, false)
	if err != nil {
		t.Fatalf("Validate interactive resume carrier = %v, want nil", err)
	}
}

// TestValidateInteractiveOnlyAgentGateSkippedForRemote pins the gate's scope: it lives
// in the !remote block, so a worker/peer job (whose agent is resolved with the REMOTE
// side's config) is not judged against the host's same-named agent definition.
func TestValidateInteractiveOnlyAgentGateSkippedForRemote(t *testing.T) {
	cfg := interactiveAdmissionConfig(t.TempDir())
	s := &Service{}

	_, err := s.Validate(cfg, JobRequest{
		ProjectKey: "self", Agent: "tty-term", Runner: "peer",
		Prompt: "remember 42", TimeoutSec: 30,
	}, true)
	if err != nil {
		t.Fatalf("Validate remote non-interactive tty agent = %v, want nil (gate is host-local)", err)
	}
}

func interactiveAdmissionConfig(root string) *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Workers: map[string]config.WorkerAuthConfig{"w1": {Token: "tok-w1"}},
		},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath: root,
				AllowedAgents: []string{
					"plain",
					"term",
					"term-unlisted",
					"exec-web",
					"raw-term",
					"tty-term",
				},
				InteractiveAllowedAgents: []string{"term", "exec-web", "raw-term"},
				AllowedRunners:           []string{"local", "remote-w1", "peer"},
				AllowExec:                true,
			},
		},
		Agents: map[string]config.AgentConfig{
			"plain":         {Type: agent.TypeCLIAgent, Command: "echo", Args: []string{"{{prompt}}"}},
			"term":          {Type: agent.TypeCLIAgent, Command: "echo", Args: []string{"{{prompt}}"}, Interactive: true, NoRawCmd: true},
			"term-unlisted": {Type: agent.TypeCLIAgent, Command: "echo", Args: []string{"{{prompt}}"}, Interactive: true, NoRawCmd: true},
			"exec-web":      {Type: agent.TypeExec, Interactive: true, NoRawCmd: true},
			"raw-term":      {Type: agent.TypeCLIAgent, Command: "echo", Args: []string{"{{prompt}}"}, Interactive: true},
			// A real terminal agent: launched bare (no arg template — the prompt is typed
			// into the pty), so a non-interactive submit would render an argv with no
			// prompt at all. See TestValidateRejectsInteractiveOnlyAgentOnNonInteractiveJob.
			"tty-term": {Type: agent.TypeCLIAgent, Command: "echo", Interactive: true, NoRawCmd: true},
		},
		Runners: map[string]config.RunnerConfig{
			"remote-w1": {Type: "worker", WorkerID: "w1"},
			"peer":      {Type: "peer-http", BaseURL: "http://peer.invalid"},
		},
	}
}
