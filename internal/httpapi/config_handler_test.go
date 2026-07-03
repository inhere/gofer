package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
)

func newConfigViewTestServer(t *testing.T, cfg *config.Config) *Server {
	t.Helper()
	root := t.TempDir()
	if cfg.Storage.Root == "" {
		cfg.Storage.Root = root
	}
	if cfg.Projects == nil {
		cfg.Projects = map[string]config.ProjectConfig{}
	}
	if _, ok := cfg.Projects["self"]; !ok {
		cfg.Projects["self"] = config.ProjectConfig{
			HostPath:       root,
			AllowedAgents:  []string{"exec"},
			AllowedRunners: []string{"local"},
			AllowExec:      true,
		}
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	st := openTestStore(t, root)
	jobs := job.NewService(cfg, projects, agents, runners, st, nil)
	eng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(eng)
	return New(&cfg.Server, cfg.Server.Token, cfg.Server.AllowEmptyToken, jobs, eng, projects, agents, nil, cfg.Runners, nil, nil)
}

func TestGetConfigRedactsSecrets(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{
			Token:    "supersecret",
			TokenEnv: "SERVER_TOKEN_ENV",
			Callers: []config.CallerConfig{
				{ID: "ci", Token: "tk-ci", CanAnswer: true},
				{ID: "op", TokenEnv: "OP_TOKEN", CanAdmin: true},
			},
			Workers: map[string]config.WorkerAuthConfig{
				"w1": {TokenEnv: "W_TOKEN", Labels: []string{"gpu"}},
			},
			Metrics: config.MetricsConfig{Token: "METRIC_SECRET"},
			Notification: &config.NotificationConfig{
				Webhooks: []config.WebhookConfig{{
					URL:       "https://hooks.example.test/gofer",
					SecretEnv: "HOOK_SECRET",
				}},
			},
		},
		Agents: map[string]config.AgentConfig{
			"codex": {
				Type: "cli",
				Env: map[string]string{
					"API_KEY": "sk-leak",
					"MODEL":   "x",
				},
			},
		},
		Runners: map[string]config.RunnerConfig{
			"peer": {Type: "peer-http", BaseURL: "http://peer.example.test", TokenEnv: "R_TOKEN"},
		},
		Roles: map[string]config.RoleConfig{
			"reviewer": {
				Agent: "codex",
				Env:   map[string]string{"ROLE_TOKEN": "role-secret"},
			},
		},
	}
	s := newConfigViewTestServer(t, cfg)

	resp := do(t, s, http.MethodGet, "/v1/config", "tk-ci", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	bodyBytes, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	body := string(bodyBytes)

	for _, secret := range []string{
		"supersecret", "SERVER_TOKEN_ENV", "tk-ci", "sk-leak", "OP_TOKEN",
		"W_TOKEN", "METRIC_SECRET", "HOOK_SECRET", "R_TOKEN", "role-secret",
	} {
		if strings.Contains(body, secret) {
			t.Fatalf("response leaked %q: %s", secret, body)
		}
	}

	var got configView
	if err := json.Unmarshal(bodyBytes, &got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if !got.Server.TokenSet {
		t.Fatalf("server.token_set=false, want true")
	}
	var op callerConfigView
	for _, c := range got.Server.Callers {
		if c.ID == "op" {
			op = c
			break
		}
	}
	if !op.CanAdmin || !op.TokenSet {
		t.Fatalf("op caller = %+v, want can_admin=true token_set=true", op)
	}
	if len(got.Agents) != 1 || got.Agents[0].Key != "codex" {
		t.Fatalf("agents = %+v, want codex", got.Agents)
	}
	if !slices.Equal(got.Agents[0].EnvKeys, []string{"API_KEY", "MODEL"}) {
		t.Fatalf("agent env_keys=%v, want [API_KEY MODEL]", got.Agents[0].EnvKeys)
	}
	if len(got.Runners) != 1 || got.Runners[0].Key != "peer" || !got.Runners[0].TokenSet {
		t.Fatalf("runner view = %+v, want peer token_set=true", got.Runners)
	}
}

func TestGetConfigRequiresAuth(t *testing.T) {
	s := newConfigViewTestServer(t, &config.Config{
		Server: config.ServerConfig{Token: "supersecret"},
	})
	resp := do(t, s, http.MethodGet, "/v1/config", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", resp.StatusCode)
	}
	resp.Body.Close()
}
