package config

import (
	"slices"
	"testing"
)

// EnvRefNames collects what the `*_env` settings point at, wherever they sit in the
// tree: the server token, a worker's token, a webhook secret, an MCP secret_env value.
func TestEnvRefNames(t *testing.T) {
	cfg := &Config{
		Server: ServerConfig{TokenEnv: "MY_SERVER_TOKEN"},
		MCPServers: map[string]MCPServerConfig{
			"m": {SecretEnv: map[string]string{"CHILD_VAR": "HOST_MCP_SECRET"}},
		},
	}
	got := cfg.EnvRefNames()
	for _, want := range []string{"MY_SERVER_TOKEN", "HOST_MCP_SECRET"} {
		if !slices.Contains(got, want) {
			t.Errorf("EnvRefNames() = %v, missing %q", got, want)
		}
	}
	if slices.Contains(got, "CHILD_VAR") {
		t.Errorf("EnvRefNames() = %v: the map key is what the child sees, not a host variable", got)
	}
	var nilCfg *Config
	if nilCfg.EnvRefNames() != nil {
		t.Error("nil config must give no names")
	}
}
