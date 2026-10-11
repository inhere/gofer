package acp

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// MCP-06: an http MCP server is dropped (and said so on stderr) for an agent whose
// mcpCapabilities.http is false — the test agent's — and the secret values handed to
// MCP servers never reach acp.jsonl.
func TestACPRunnerMCPServers(t *testing.T) {
	const sentinel = "s3cr3t-acp-sentinel"
	resultDir := t.TempDir()
	var stderr syncBuffer
	res := (&Runner{}).Run(context.Background(), runner.Request{
		JobID:   "job-mcp",
		Command: testcmd.Path(t),
		Args:    acptest.CmdArgs(acptest.Options{}),
		WorkDir: t.TempDir(),
		Stdout:  io.Discard,
		Stderr:  &stderr,
		ACP: &runner.ACPRequest{
			Prompt:    "hi",
			ResultDir: resultDir,
			MCPServers: []runner.ACPMCPServer{
				{Name: "db", Command: "db-mcp", Env: map[string]string{"DB_URL": sentinel}},
				{Name: "api", Type: "http", URL: "https://mcp.example/mcp", Headers: map[string]string{"Authorization": sentinel}},
			},
		},
	})
	if res.Err != nil || res.ExitCode != 0 {
		t.Fatalf("run failed: exit=%d err=%v stderr=%s", res.ExitCode, res.Err, stderr.String())
	}
	if !strings.Contains(stderr.String(), `MCP server "api" skipped`) || strings.Contains(stderr.String(), `"db" skipped`) {
		t.Fatalf("stderr = %s", stderr.String())
	}
	b, err := os.ReadFile(filepath.Join(resultDir, "artifacts", ACPFileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), sentinel) || strings.Contains(stderr.String(), sentinel) {
		t.Fatal("MCP secret leaked into acp.jsonl / stderr")
	}
}
