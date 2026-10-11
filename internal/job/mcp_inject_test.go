package job

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/runner"
)

const mcpSentinel = "s3cr3t-MCP-sentinel"

func mcpLookup(vars map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := vars[k]; return v, ok }
}

func mcpTestSpecs() map[string]config.MCPServerConfig {
	return map[string]config.MCPServerConfig{
		"db": {
			Command: "db-mcp", Args: []string{"--readonly"},
			Env: map[string]string{"APP": "gofer"}, SecretEnv: map[string]string{"DB_URL": "SRC_DB_URL"},
			Tools: []string{"query"},
		},
		"api": {
			Type: "http", URL: "https://mcp.example/mcp", Headers: map[string]string{"X-Team": "a"},
			SecretHeaders: map[string]string{"Authorization": "SRC_API_AUTH"},
		},
	}
}

func TestBuildMCPInjectionACP(t *testing.T) {
	specs := mcpTestSpecs()
	specs["db"] = func(s config.MCPServerConfig) config.MCPServerConfig {
		s.Args = append(s.Args, "{{env:DB_URL}}")
		return s
	}(specs["db"])
	inj, err := buildMCPInjection(config.MCPFormatACP, []string{"db", "api"}, specs, t.TempDir(),
		mcpLookup(map[string]string{"SRC_DB_URL": mcpSentinel, "SRC_API_AUTH": "Bearer " + mcpSentinel}))
	if err != nil {
		t.Fatal(err)
	}
	if len(inj.ACP) != 2 || len(inj.Env) != 0 || len(inj.Args) != 0 {
		t.Fatalf("acp injection: %+v", inj)
	}
	db, api := inj.ACP[0], inj.ACP[1]
	if db.Command != "db-mcp" || db.Env["DB_URL"] != mcpSentinel || db.Env["APP"] != "gofer" || db.Args[1] != mcpSentinel {
		t.Fatalf("stdio entry: %+v", db)
	}
	if api.Type != "http" || api.URL != "https://mcp.example/mcp" || api.Headers["Authorization"] != "Bearer "+mcpSentinel || api.Headers["X-Team"] != "a" {
		t.Fatalf("http entry: %+v", api)
	}
	if len(inj.Notes) != 1 || !strings.Contains(inj.Notes[0], "tools filter") {
		t.Fatalf("notes: %v", inj.Notes)
	}
}

func TestBuildMCPInjectionClaudeJSON(t *testing.T) {
	dir := t.TempDir()
	inj, err := buildMCPInjection(config.MCPFormatClaudeJSON, []string{"db", "api"}, mcpTestSpecs(), dir,
		mcpLookup(map[string]string{"SRC_DB_URL": mcpSentinel, "SRC_API_AUTH": mcpSentinel}))
	if err != nil {
		t.Fatal(err)
	}
	if !inj.UseMCPArgs || inj.ConfigFile != filepath.Join(dir, "mcp.json") {
		t.Fatalf("injection: %+v", inj)
	}
	b, err := os.ReadFile(inj.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if strings.Contains(text, mcpSentinel) {
		t.Fatalf("secret written to the config file:\n%s", text)
	}
	for _, want := range []string{`"DB_URL": "${GOFER_MCP_DB_DB_URL}"`, `"Authorization": "${GOFER_MCP_API_AUTHORIZATION}"`, `"type": "http"`, `"command": "db-mcp"`, `"APP": "gofer"`} {
		if !strings.Contains(text, want) {
			t.Errorf("config lacks %s:\n%s", want, text)
		}
	}
	if inj.Env["GOFER_MCP_DB_DB_URL"] != mcpSentinel || inj.Env["GOFER_MCP_API_AUTHORIZATION"] != mcpSentinel {
		t.Fatalf("env: %v", inj.Env)
	}
	if fi, _ := os.Stat(inj.ConfigFile); fi.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Fatalf("config file mode %v, want 0600", fi.Mode().Perm())
	}
}

func TestBuildMCPInjectionCodex(t *testing.T) {
	inj, err := buildMCPInjection(config.MCPFormatCodexConfig, []string{"db", "api"}, mcpTestSpecs(), t.TempDir(),
		mcpLookup(map[string]string{"SRC_DB_URL": mcpSentinel, "SRC_API_AUTH": mcpSentinel}))
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Join(inj.Args, " ")
	if strings.Contains(argv, mcpSentinel) {
		t.Fatalf("secret in argv: %s", argv)
	}
	for _, want := range []string{
		`-c mcp_servers.db.command="db-mcp"`,
		`-c mcp_servers.db.args=["--readonly"]`,
		`-c mcp_servers.db.env={"APP"="gofer"}`,
		`-c mcp_servers.db.env_vars=["DB_URL"]`,
		`-c mcp_servers.db.enabled_tools=["query"]`,
		`-c mcp_servers.api.url="https://mcp.example/mcp"`,
		`-c mcp_servers.api.http_headers={"X-Team"="a"}`,
		`-c mcp_servers.api.env_http_headers={"Authorization"="GOFER_MCP_API_AUTHORIZATION"}`,
	} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv lacks %s:\n%s", want, argv)
		}
	}
	if inj.Env["DB_URL"] != mcpSentinel || inj.Env["GOFER_MCP_API_AUTHORIZATION"] != mcpSentinel {
		t.Fatalf("env: %v", inj.Env)
	}
}

func TestBuildMCPInjectionErrors(t *testing.T) {
	specs := mcpTestSpecs()
	lookup := mcpLookup(map[string]string{"SRC_DB_URL": "v"})
	if _, err := buildMCPInjection(config.MCPFormatACP, []string{"api"}, specs, t.TempDir(), lookup); err == nil || !strings.Contains(err.Error(), "SRC_API_AUTH") {
		t.Fatalf("missing secret: %v", err)
	}
	withRef := map[string]config.MCPServerConfig{"x": {Command: "c", Args: []string{"{{env:K}}"}, SecretEnv: map[string]string{"K": "SRC_DB_URL"}}}
	if _, err := buildMCPInjection(config.MCPFormatCodexConfig, []string{"x"}, withRef, t.TempDir(), lookup); err == nil || !strings.Contains(err.Error(), "{{env:K}}") {
		t.Fatalf("codex env ref: %v", err)
	}
	dotted := map[string]config.MCPServerConfig{"a.b": {Command: "c"}}
	if _, err := buildMCPInjection(config.MCPFormatCodexConfig, []string{"a.b"}, dotted, t.TempDir(), lookup); err == nil || !strings.Contains(err.Error(), "'.'") {
		t.Fatalf("codex dotted name: %v", err)
	}
	clash := map[string]config.MCPServerConfig{
		"a": {Command: "c", SecretEnv: map[string]string{"TOKEN": "SRC_DB_URL"}},
		"b": {Command: "c", SecretEnv: map[string]string{"TOKEN": "OTHER"}},
	}
	if _, err := buildMCPInjection(config.MCPFormatCodexConfig, []string{"a", "b"}, clash, t.TempDir(), mcpLookup(map[string]string{"SRC_DB_URL": "1", "OTHER": "2"})); err == nil || !strings.Contains(err.Error(), "TOKEN") {
		t.Fatalf("codex clash: %v", err)
	}
}

func TestSpliceMCPArgs(t *testing.T) {
	argv := []string{"claude", "--resume", "sid", agent.MCPArgsPlaceholder, "-p", "next"}
	if got := agent.SpliceMCPArgs(argv, []string{"--mcp-config", "/r/mcp.json"}); !slices.Equal(got, []string{"claude", "--resume", "sid", "--mcp-config", "/r/mcp.json", "-p", "next"}) {
		t.Fatalf("splice = %v", got)
	}
	if got := agent.SpliceMCPArgs(argv, nil); !slices.Equal(got, []string{"claude", "--resume", "sid", "-p", "next"}) {
		t.Fatalf("drop = %v", got)
	}
}

// End to end on the local runner: the agent receives the MCP argv fragment and the
// secret in its environment, and the secret is nowhere in what gofer persists.
func TestMCPInjectedLocallyWithoutLeaking(t *testing.T) {
	t.Setenv("SRC_DB_URL", mcpSentinel)
	root := t.TempDir()
	s, _ := newSkillService(t, root, func(cfg *config.Config) {
		cfg.MCPServers = map[string]config.MCPServerConfig{"db": {Command: "db-mcp", SecretEnv: map[string]string{"DB_URL": "SRC_DB_URL"}}}
		bin := cfg.Agents["ok"].Command
		cfg.Agents["ok"] = config.AgentConfig{Type: "cli-agent", Command: bin, Args: []string{"argv", "{{prompt}}"},
			MCPFormat: config.MCPFormatClaudeJSON, MCPArgs: []string{"--mcp-config", "{{mcp_config_file}}"}}
		cfg.Agents["envp"] = config.AgentConfig{Type: "cli-agent", Command: bin, Args: []string{"env-print", "GOFER_MCP_DB_DB_URL", "{{prompt}}"},
			MCPFormat: config.MCPFormatClaudeJSON, MCPArgs: []string{"--mcp-config", "{{mcp_config_file}}"}}
		p := cfg.Projects["self"]
		p.AllowedAgents = append(p.AllowedAgents, "envp")
		cfg.Projects["self"] = p
	})
	final := submitAndWait(t, s, JobRequest{ProjectKey: "self", Agent: "ok", Runner: "local", Cwd: ".", Prompt: "hello", TimeoutSec: 30, MCP: []string{"db"}})
	out := executedPromptOf(t, final)
	lines := strings.Split(out, "\n")
	cfgFile := filepath.Join(final.ResultDir, "mcp.json")
	if !slices.Equal(lines, []string{"--mcp-config", cfgFile, "hello"}) {
		t.Fatalf("argv = %q, want --mcp-config %s hello", lines, cfgFile)
	}
	rec, _, err := s.meta.GetJob(final.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(cfgFile)
	events, _ := s.meta.ListJobEvents(final.ID, 0)
	evText := ""
	for _, e := range events {
		evText += e.Type + " " + e.Detail + "\n"
	}
	for where, text := range map[string]string{"argv": out, "request_json": rec.RequestJSON, "rendered_command": rec.RenderedCommand, "mcp.json": string(b), "events": evText} {
		if strings.Contains(text, mcpSentinel) {
			t.Errorf("secret leaked into %s: %s", where, text)
		}
	}
	if !strings.Contains(evText, EventJobMCPInjected) {
		t.Errorf("no %s event: %s", EventJobMCPInjected, evText)
	}

	envJob := submitAndWait(t, s, JobRequest{ProjectKey: "self", Agent: "envp", Runner: "local", Cwd: ".", Prompt: "x", TimeoutSec: 30, MCP: []string{"db"}})
	if got := executedPromptOf(t, envJob); !strings.Contains(got, "GOFER_MCP_DB_DB_URL="+mcpSentinel) {
		t.Fatalf("agent env lacks the secret: %q", got)
	}

	// The secret variable missing on this machine refuses the job, naming it.
	t.Setenv("SRC_DB_URL", "")
	os.Unsetenv("SRC_DB_URL")
	if _, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "ok", Runner: "local", Cwd: ".", Prompt: "x", MCP: []string{"db"}}); err == nil || !strings.Contains(err.Error(), "SRC_DB_URL") {
		t.Fatalf("missing secret: err=%v", err)
	}
}

// A continuation of a job with MCP servers gets them again, pointing at ITS OWN
// result dir's mcp.json (the placeholder resume leaves in the argv is filled at submit).
func TestResumeReinjectsMCP(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{"self": {
			HostPath: root, AllowedAgents: []string{"claude", "exec"}, AllowedRunners: []string{"local"}, AllowExec: true,
			MCP: []string{"docs"},
		}},
		Agents:     map[string]config.AgentConfig{"claude": {Type: agent.TypeCLIAgent, Command: "claude", Args: []string{"-p", "{{prompt}}"}}},
		MCPServers: map[string]config.MCPServerConfig{"docs": {Command: "docs-mcp"}},
	}
	s := newServiceFromCfg(t, root, cfg)
	src := submitSourceCancel(t, s, JobRequest{ProjectKey: "self", Agent: "claude", Runner: "local", Prompt: "p", Cwd: ".", TimeoutSec: 30})
	if !slices.Equal(src.MCP, []string{"docs"}) {
		t.Fatalf("source MCP = %v", src.MCP)
	}
	next, err := s.ResumeJob(src.ID, "again", "", "c")
	if err != nil {
		t.Fatalf("ResumeJob: %v", err)
	}
	s.Wait(next.ID)
	rec, _, err := s.meta.GetJob(next.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(next.ResultDir, "mcp.json")
	if !strings.Contains(rec.RenderedCommand, "--mcp-config") || !strings.Contains(rec.RenderedCommand, filepath.ToSlash(want)) && !strings.Contains(rec.RenderedCommand, want) {
		t.Fatalf("resumed command lacks its own MCP config %s: %s", want, rec.RenderedCommand)
	}
	if strings.Contains(rec.RenderedCommand, agent.MCPArgsPlaceholder) {
		t.Fatalf("placeholder left in the command: %s", rec.RenderedCommand)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("continuation's mcp.json: %v", err)
	}
}

func TestMergeACPMCPAgentEntryWins(t *testing.T) {
	own := []runner.ACPMCPServer{{Name: "docs", Command: "mine"}}
	reg := []runner.ACPMCPServer{{Name: "docs", Command: "registry"}, {Name: "db", Command: "db"}}
	got := mergeACPMCP(own, reg)
	if len(got) != 2 || got[0].Command != "mine" || got[1].Name != "db" {
		t.Fatalf("merge = %+v", got)
	}
}
