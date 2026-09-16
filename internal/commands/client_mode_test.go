package commands

import (
	"strings"
	"testing"

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
