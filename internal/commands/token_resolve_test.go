package commands

import (
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// TestResolveClientToken locks the client-token precedence after xu64.1 moved the
// GOFER_SERVER_TOKEN fallback out of the --token flag default (which leaked into
// --help) into runtime resolution. Precedence, highest first:
// explicit --token > GOFER_SERVER_TOKEN env > server.token_env > server.token.
func TestResolveClientToken(t *testing.T) {
	const customEnv = "GOFER_CUSTOM_TOKEN_ENV"

	cases := []struct {
		name        string
		flag        string
		serverToken string // GOFER_SERVER_TOKEN ("" = unset)
		sc          config.ServerConfig
		customVal   string // value for sc.TokenEnv (customEnv) when referenced
		want        string
	}{
		{
			name:        "explicit flag wins over everything",
			flag:        "flagtok",
			serverToken: "envtok",
			sc:          config.ServerConfig{Token: "cfgtok", TokenEnv: customEnv},
			customVal:   "customtok",
			want:        "flagtok",
		},
		{
			name:        "GOFER_SERVER_TOKEN env overrides config when no flag",
			flag:        "",
			serverToken: "envtok",
			sc:          config.ServerConfig{Token: "cfgtok", TokenEnv: customEnv},
			customVal:   "customtok",
			want:        "envtok",
		},
		{
			name:        "server.token_env used when no flag and no GOFER_SERVER_TOKEN",
			flag:        "",
			serverToken: "",
			sc:          config.ServerConfig{Token: "cfgtok", TokenEnv: customEnv},
			customVal:   "customtok",
			want:        "customtok",
		},
		{
			name:        "server.token used when nothing else set",
			flag:        "",
			serverToken: "",
			sc:          config.ServerConfig{Token: "cfgtok"},
			want:        "cfgtok",
		},
		{
			name:        "empty when nothing configured",
			flag:        "",
			serverToken: "",
			sc:          config.ServerConfig{},
			want:        "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GOFER_SERVER_TOKEN", tc.serverToken)
			if tc.sc.TokenEnv != "" {
				t.Setenv(tc.sc.TokenEnv, tc.customVal)
			}
			if got := resolveClientToken(&tc.sc, tc.flag); got != tc.want {
				t.Errorf("resolveClientToken() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestBindServerFlagsTokenNoSecretDefault guards the xu64.1 fix: the --token flag
// bound by bindServerFlags must NOT carry a ${GOFER_SERVER_TOKEN} default. gcli
// interpolates such a default and renders the resolved value into --help
// (help.go uses f.DefValue), which leaks the token. An empty DefValue proves no
// leak. --server keeps its ${GOFER_SERVER_ADDR} default on purpose (addr is not
// secret), so it is not asserted here.
func TestBindServerFlagsTokenNoSecretDefault(t *testing.T) {
	// A live GOFER_SERVER_TOKEN would be interpolated if the default were a
	// template, so set one to make a regression visibly fail.
	t.Setenv("GOFER_SERVER_TOKEN", "should-not-appear-in-help")

	c := NewMcpCmd() // mcp binds --server/--token via the shared bindServerFlags
	c.Config(c)      // apply the Config closure to register flags

	f := c.Flags.LookupFlag("token")
	if f == nil {
		t.Fatal("--token flag not bound")
	}
	if f.DefValue != "" {
		t.Errorf("--token DefValue = %q, want empty; a non-empty default leaks the token into --help (xu64.1)", f.DefValue)
	}
}
