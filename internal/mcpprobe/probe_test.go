package mcpprobe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

func TestProbeStdioPassesSecretEnv(t *testing.T) {
	bin := testcmd.Path(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	spec := config.MCPServerConfig{Command: bin, Args: []string{"mcp-server"}, SecretEnv: map[string]string{"MCPTEST_TOOL": "SRC_TOOL"}}
	res, err := Probe(ctx, spec, func(k string) (string, bool) {
		if k == "SRC_TOOL" {
			return "secret_tool", true
		}
		return "", false
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if res.ServerName != "mcptest" || len(res.Tools) != 1 || res.Tools[0] != "secret_tool" {
		t.Fatalf("result = %+v", res)
	}
	if _, err := Probe(ctx, spec, func(string) (string, bool) { return "", false }, "test"); err == nil || !strings.Contains(err.Error(), "SRC_TOOL") {
		t.Fatalf("missing secret: %v", err)
	}
	bad := config.MCPServerConfig{Command: bin, Args: []string{"exit", "3"}}
	if _, err := Probe(ctx, bad, func(string) (string, bool) { return "", false }, "test"); err == nil {
		t.Fatal("a server that exits must fail the probe")
	}
}

func TestProbeHTTPSendsSecretHeaders(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "httptest-mcp", Version: "1"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "search"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{}, nil, nil
	})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	var sawAuth, sawTeam bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer tok" {
			sawAuth = true
		}
		if r.Header.Get("X-Team") == "a" {
			sawTeam = true
		}
		h.ServeHTTP(w, r)
	}))
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	spec := config.MCPServerConfig{Type: "http", URL: ts.URL, Headers: map[string]string{"X-Team": "a"}, SecretHeaders: map[string]string{"Authorization": "SRC_AUTH"}}
	res, err := Probe(ctx, spec, func(k string) (string, bool) { return "Bearer tok", k == "SRC_AUTH" }, "test")
	if err != nil {
		t.Fatal(err)
	}
	if res.ServerName != "httptest-mcp" || len(res.Tools) != 1 || res.Tools[0] != "search" || !sawAuth || !sawTeam {
		t.Fatalf("result=%+v auth=%v team=%v", res, sawAuth, sawTeam)
	}
}
