package httpapi

import (
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// GET /v1/mcp-servers shows each entry with its bindings and, for every secret, only
// the environment variable's NAME and whether it is set here — never the value.
func TestListMCPServersShowsNamesNotValues(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GOFER_TEST_MCP_TOKEN", "s3cr3t-sentinel")
	cfg := &config.Config{
		Server:  config.ServerConfig{Token: testToken, MCP: []string{"docs"}},
		Storage: config.StorageConfig{Root: root},
		MCPServers: map[string]config.MCPServerConfig{
			"docs": {Type: "http", URL: "https://x/mcp", SecretHeaders: map[string]string{"Authorization": "GOFER_TEST_MCP_TOKEN"}},
			"db":   {Command: "db-mcp", SecretEnv: map[string]string{"URL": "GOFER_TEST_MCP_UNSET"}},
		},
		Agents:   map[string]config.AgentConfig{"mine": {Type: agent.TypeCLIAgent, Command: "echo", MCP: []string{"db"}}},
		Projects: map[string]config.ProjectConfig{"self": {HostPath: root, AllowedAgents: []string{"mine"}, AllowedRunners: []string{"local"}, MCP: []string{"db"}}},
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	jobs := drainOnCleanup(t, job.NewService(cfg, projects, agents, runners, openTestStore(t, filepath.Join(root, "db")), nil))
	s := New(&cfg.Server, testToken, false, jobs, workflow.NewEngine(jobs), projects, agents, nil, nil, nil, nil)

	resp := do(t, s, http.MethodGet, "/v1/mcp-servers", testToken, nil)
	body, _ := io.ReadAll(resp.Body)
	text := string(body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, text)
	}
	if strings.Contains(text, "s3cr3t-sentinel") {
		t.Fatalf("secret value leaked: %s", text)
	}
	for _, want := range []string{`"name":"db"`, `"env":"GOFER_TEST_MCP_UNSET","set":false`, `"env":"GOFER_TEST_MCP_TOKEN","set":true`, `"agents":["mine"]`, `"projects":["self"]`, `"server":true`} {
		if !strings.Contains(text, want) {
			t.Errorf("list lacks %s: %s", want, text)
		}
	}
	if r := do(t, s, http.MethodGet, "/v1/mcp-servers/nope", testToken, nil); r.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown: status=%d", r.StatusCode)
	}
}

// POST /v1/mcp-servers/{name}/test probes from the server's machine and reports the
// tools; a failure is a 200 with ok=false and the reason.
func TestMCPServerProbeEndpoint(t *testing.T) {
	root := t.TempDir()
	bin := testcmd.Path(t)
	cfg := &config.Config{
		Server:  config.ServerConfig{Token: testToken},
		Storage: config.StorageConfig{Root: root},
		MCPServers: map[string]config.MCPServerConfig{
			"ok":  {Command: bin, Args: []string{"mcp-server"}},
			"bad": {Command: bin, Args: []string{"mcp-server"}, SecretEnv: map[string]string{"X": "GOFER_TEST_MCP_ABSENT"}},
		},
		Projects: map[string]config.ProjectConfig{"self": {HostPath: root, AllowedRunners: []string{"local"}}},
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	jobs := drainOnCleanup(t, job.NewService(cfg, projects, agents, runners, openTestStore(t, filepath.Join(root, "db")), nil))
	s := New(&cfg.Server, testToken, false, jobs, workflow.NewEngine(jobs), projects, agents, nil, nil, nil, nil)

	resp := do(t, s, http.MethodPost, "/v1/mcp-servers/ok/test", testToken, nil)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"ok":true`) || !strings.Contains(string(body), `"tools":["ping"]`) {
		t.Fatalf("probe ok: status=%d body=%s", resp.StatusCode, body)
	}
	resp = do(t, s, http.MethodPost, "/v1/mcp-servers/bad/test", testToken, nil)
	body, _ = io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"ok":false`) || !strings.Contains(string(body), "GOFER_TEST_MCP_ABSENT") {
		t.Fatalf("probe bad: body=%s", body)
	}
}
