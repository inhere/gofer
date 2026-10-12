// Package mcpprobe checks that a registry MCP server (MCP-06) can be reached from this
// machine: it starts (stdio) or connects to (http) the server with the declaration's
// environment and headers, performs the MCP handshake and lists the tools. Secret
// values are read through lookup — this machine's environment — and never returned.
package mcpprobe

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/config"
)

// DefaultTimeout bounds a probe end to end.
const DefaultTimeout = 30 * time.Second

// Result is what a probe found.
type Result struct {
	ServerName    string   `json:"server_name,omitempty"`
	ServerVersion string   `json:"server_version,omitempty"`
	Tools         []string `json:"tools"`
	ElapsedMs     int64    `json:"elapsed_ms"`
}

// Probe connects to spec and lists its tools. version is reported as the client
// version. A secret variable lookup cannot find is an error naming it.
func Probe(ctx context.Context, spec config.MCPServerConfig, lookup func(string) (string, bool), version string) (Result, error) {
	start := time.Now()
	secretEnv := map[string]string{}
	for k, src := range spec.SecretEnv {
		v, ok := lookup(src)
		if !ok {
			return Result{}, fmt.Errorf("environment variable %s (secret_env %s) is not set on this machine", src, k)
		}
		secretEnv[k] = v
	}
	headers := map[string]string{}
	for k, v := range spec.Headers {
		headers[k] = v
	}
	for k, src := range spec.SecretHeaders {
		v, ok := lookup(src)
		if !ok {
			return Result{}, fmt.Errorf("environment variable %s (secret_headers %s) is not set on this machine", src, k)
		}
		headers[k] = v
	}
	plain := func(s string) string {
		return config.ReplaceMCPEnvRefs(s, func(k string) string { return secretEnv[k] })
	}

	var transport mcp.Transport
	var stderr *tailBuffer
	switch spec.EffectiveType() {
	case config.MCPTypeHTTP:
		transport = &mcp.StreamableClientTransport{
			Endpoint:             plain(spec.URL),
			HTTPClient:           &http.Client{Transport: headerTransport{headers: headers, base: http.DefaultTransport}},
			MaxRetries:           -1,
			DisableStandaloneSSE: true,
		}
	default:
		args := make([]string, 0, len(spec.Args))
		for _, a := range spec.Args {
			args = append(args, plain(a))
		}
		cmd := exec.CommandContext(ctx, spec.Command, args...)
		env := os.Environ()
		for k, v := range spec.Env {
			env = append(env, k+"="+v)
		}
		for k, v := range secretEnv {
			env = append(env, k+"="+v)
		}
		cmd.Env = env
		stderr = &tailBuffer{max: 4 << 10}
		cmd.Stderr = stderr
		transport = &mcp.CommandTransport{Command: cmd, TerminateDuration: 2 * time.Second}
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "gofer-mcp-probe", Version: version}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return Result{}, withStderr(fmt.Errorf("connect: %w", err), stderr)
	}
	defer func() { _ = session.Close() }()
	res := Result{Tools: []string{}}
	if ir := session.InitializeResult(); ir != nil && ir.ServerInfo != nil {
		res.ServerName, res.ServerVersion = ir.ServerInfo.Name, ir.ServerInfo.Version
	}
	var cursor string
	for page := 0; page < 20; page++ {
		lt, err := session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return Result{}, withStderr(fmt.Errorf("tools/list: %w", err), stderr)
		}
		for _, t := range lt.Tools {
			res.Tools = append(res.Tools, t.Name)
		}
		if lt.NextCursor == "" {
			break
		}
		cursor = lt.NextCursor
	}
	sort.Strings(res.Tools)
	res.ElapsedMs = time.Since(start).Milliseconds()
	return res, nil
}

// headerTransport adds the declaration's headers to every request.
type headerTransport struct {
	headers map[string]string
	base    http.RoundTripper
}

func (t headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range t.headers {
		r.Header.Set(k, v)
	}
	return t.base.RoundTrip(r)
}

// tailBuffer keeps the last max bytes a process wrote to stderr.
type tailBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
	max int
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Write(p)
	if over := b.buf.Len() - b.max; over > 0 {
		b.buf.Next(over)
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func withStderr(err error, stderr *tailBuffer) error {
	if stderr == nil {
		return err
	}
	if tail := strings.TrimSpace(stderr.String()); tail != "" {
		return fmt.Errorf("%w; server stderr: %s", err, tail)
	}
	return err
}
