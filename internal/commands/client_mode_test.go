package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/buildinfo"
	"github.com/inhere/gofer/internal/config"
)

// TestNewClientErrorInClientModeWithoutServer proves the client-node failure mode:
// with GOFER_RUN_MODE=client and NO server address (no --server / GOFER_SERVER_ADDR,
// no explicit --config), newClient must not fall back to config.Load's default
// 0.0.0.0:8765 — it fails with the connection-env hint a client node can act on.
func TestNewClientErrorInClientModeWithoutServer(t *testing.T) {
	t.Setenv(config.EnvRunMode, config.RunModeClient)
	t.Setenv(config.EnvConfigDir, t.TempDir())
	t.Setenv("GOFER_SERVER_ADDR", "")
	t.Setenv("GOFER_SERVER_TOKEN", "")
	t.Setenv(config.EnvConfigPath, "")
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })

	_, err := newClient("", "", "")
	if err == nil {
		t.Fatal("client mode without a server address must fail, got a client on the default address")
	}
	want := "client mode: set GOFER_SERVER_ADDR/GOFER_SERVER_TOKEN in $GOFER_CONFIG_DIR/.env or pass -s/--token"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}

	// The same node with an address (the -s/--server flag as bound from
	// ${GOFER_SERVER_ADDR}) needs no local config at all.
	cli, err := newClient("", "127.0.0.1:8765", "tok")
	if err != nil {
		t.Fatalf("client mode with --server must build a client: %v", err)
	}
	if cli == nil {
		t.Fatal("client mode with --server returned a nil client")
	}

	// Outside client mode the historical "no config file" hint still wins.
	t.Setenv(config.EnvRunMode, config.RunModeServer)
	if _, err := newClient("", "", ""); err == nil || strings.Contains(err.Error(), "client mode") {
		t.Fatalf("server mode without a config must keep its own error, got %v", err)
	}
}

// clientNode points the process at a stub server the way a real client node is
// wired: GOFER_RUN_MODE=client, an empty config dir (only .env would live there),
// no local config anywhere, and the connection flags carrying the server address.
func clientNode(t *testing.T, serverURL string) {
	t.Helper()
	t.Setenv(config.EnvRunMode, config.RunModeClient)
	t.Setenv(config.EnvConfigDir, t.TempDir())
	t.Setenv(config.EnvConfigPath, "")
	t.Setenv("GOFER_SERVER_TOKEN", "")
	config.InputCfgFile = ""
	jobConnOpts.server, jobConnOpts.token = serverURL, ""
	t.Cleanup(func() {
		config.InputCfgFile = ""
		jobConnOpts.server, jobConnOpts.token = "", ""
	})
}

// metaServer serves a stub /v1/meta with the given aggregate and returns its URL.
func metaServer(t *testing.T, meta map[string]any) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/meta" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(meta)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestProjectListDefaultsRemoteInClientMode proves `project list` (no --remote) on
// a client node lists the SERVER's projects: a client has no local config, so the
// old default read an empty/absent config.yaml and printed "(no projects in …)".
func TestProjectListDefaultsRemoteInClientMode(t *testing.T) {
	clientNode(t, metaServer(t, map[string]any{"projects": []any{
		map[string]any{"key": "from-server", "default_agent": "codex", "allowed_agents": []string{"codex"}},
	}}))

	c := bindCmd(findSub(t, NewProjectCmd(), "list"))
	projectListOpts.remote = false
	t.Cleanup(func() { projectListOpts.remote = false })

	out := captureOutput(t, func() {
		if err := runProjectList(c, nil); err != nil {
			t.Fatalf("project list: %v", err)
		}
	})
	if !strings.Contains(out, "from-server") {
		t.Fatalf("client mode must list the server's projects, got:\n%s", out)
	}
	if strings.Contains(out, "(no projects in") {
		t.Fatalf("client mode read a local config instead of the server, got:\n%s", out)
	}
}

// TestProjectAddRejectedInClientMode proves add/remove refuse on a client node
// (there is no local config to edit) and write nothing before doing so.
func TestProjectAddRejectedInClientMode(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "cfg.yaml")
	clientNode(t, "")
	config.InputCfgFile = cfgPath

	projectAddOpts.hostPath = t.TempDir() // a valid path: the refusal must precede any write
	projectAddOpts.force = false
	t.Cleanup(func() {
		projectAddOpts.hostPath, projectAddOpts.force = "", false
	})

	want := "run mode is client: project add/remove edits a local config; " +
		"unset GOFER_RUN_MODE or use server/worker on that node"

	addCmd := bindCmd(findSub(t, NewProjectCmd(), "add"))
	addCmd.Arg("key").WithValue("demo")
	if err := runProjectAdd(addCmd, nil); err == nil || err.Error() != want {
		t.Fatalf("project add in client mode: err = %v, want %q", err, want)
	}

	rmCmd := bindCmd(findSub(t, NewProjectCmd(), "remove"))
	rmCmd.Arg("key").WithValue("demo")
	if err := runProjectRemove(rmCmd, nil); err == nil || err.Error() != want {
		t.Fatalf("project remove in client mode: err = %v, want %q", err, want)
	}

	if _, err := os.Stat(cfgPath); err == nil {
		t.Fatalf("project add/remove wrote %s in client mode", cfgPath)
	}
}

// TestAgentListDefaultsRemoteInClientMode proves `agent list` on a client node
// lists the SERVER's agents with their capability bits (batch/interactive), while
// --local still shows the binary's built-in templates.
func TestAgentListDefaultsRemoteInClientMode(t *testing.T) {
	clientNode(t, metaServer(t, map[string]any{"agents": []any{
		map[string]any{"key": "codex", "type": "cli-agent", "batch": true, "interactive": true},
		map[string]any{"key": "tty-only", "type": "cli-agent", "batch": false, "interactive": true},
	}}))

	agentListOpts.runner, agentListOpts.local = "", false
	t.Cleanup(func() { agentListOpts.runner, agentListOpts.local = "", false })
	c := bindCmd(findSub(t, NewAgentCmd(), "list"))

	out := captureOutput(t, func() {
		if err := runAgentList(c, nil); err != nil {
			t.Fatalf("agent list: %v", err)
		}
	})
	if !strings.Contains(out, "codex") || !strings.Contains(out, "interactive=true") {
		t.Fatalf("client mode must list the server's agents with capability bits, got:\n%s", out)
	}
	if !strings.Contains(out, "tty-only") || !strings.Contains(out, "batch=false") {
		t.Fatalf("server-only agent missing from the listing:\n%s", out)
	}
	if strings.Contains(out, "command=") {
		t.Fatalf("client mode listed the LOCAL registry instead of the server's, got:\n%s", out)
	}

	// --local keeps the built-in templates reachable on a client node.
	agentListOpts.local = true
	local := captureOutput(t, func() {
		if err := runAgentList(c, nil); err != nil {
			t.Fatalf("agent list --local: %v", err)
		}
	})
	if !strings.Contains(local, "command=") || strings.Contains(local, "batch=") {
		t.Fatalf("agent list --local must render the local registry, got:\n%s", local)
	}
}

// TestServeRejectedInClientMode proves `serve` refuses on a client node before any
// daemon/pidfile work: the node exists to talk to a remote server, not to start one.
func TestServeRejectedInClientMode(t *testing.T) {
	clientNode(t, "")

	serveOpts.daemon = false
	t.Cleanup(func() { serveOpts.daemon = false })

	want := "run mode is client: serve starts a local server from a local config; " +
		"unset GOFER_RUN_MODE or use server/worker on that node"
	err := runServe(bindCmd(NewServeCmd()), nil, buildinfo.Info{})
	if err == nil || err.Error() != want {
		t.Fatalf("serve in client mode: err = %v, want %q", err, want)
	}
	if _, statErr := os.Stat(servePIDFile()); statErr == nil {
		t.Fatalf("client mode serve must not create %s", servePIDFile())
	}
}
