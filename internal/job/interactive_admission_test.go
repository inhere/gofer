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
		},
		Runners: map[string]config.RunnerConfig{
			"remote-w1": {Type: "worker", WorkerID: "w1"},
			"peer":      {Type: "peer-http", BaseURL: "http://peer.invalid"},
		},
	}
}
