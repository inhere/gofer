package serve

import (
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// TestMergeServeOptsWebDir pins the CLI-overrides-config merge for web_dir: an unset
// --web-dir must LEAVE the configured dir intact. The original code assigned it
// unconditionally, so serving without the flag wiped server.web_dir and fell back to
// the embedded dist — a dead config key that silently ignored what the operator wrote
// (tools-k0q). nssm passes the flag, which is why live never noticed.
func TestMergeServeOptsWebDir(t *testing.T) {
	cases := []struct {
		name   string
		cfgDir string
		optDir string
		want   string
	}{
		{"flag unset keeps config", "./web/dist", "", "./web/dist"},
		{"flag overrides config", "./web/dist", "/opt/dist", "/opt/dist"},
		{"both empty = embedded", "", "", ""},
		{"flag alone", "", "/opt/dist", "/opt/dist"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{Server: config.ServerConfig{WebDir: tc.cfgDir}}
			mergeServeOpts(cfg, Opts{WebDir: tc.optDir})
			if cfg.Server.WebDir != tc.want {
				t.Fatalf("web_dir = %q, want %q (cfg=%q flag=%q)",
					cfg.Server.WebDir, tc.want, tc.cfgDir, tc.optDir)
			}
		})
	}
}

// TestMergeServeOptsAddrAndToken: the same CLI-overrides-config rule for the other
// two knobs merged here (addr wins when set; allow-empty-token is an OR).
func TestMergeServeOptsAddrAndToken(t *testing.T) {
	cfg := &config.Config{Server: config.ServerConfig{Addr: "0.0.0.0:LIVE-PORT"}}
	if addr, _ := mergeServeOpts(cfg, Opts{}); addr != "0.0.0.0:LIVE-PORT" {
		t.Fatalf("unset --addr must keep config addr, got %q", addr)
	}
	if addr, _ := mergeServeOpts(cfg, Opts{Addr: "127.0.0.1:9000"}); addr != "127.0.0.1:9000" {
		t.Fatalf("--addr must override config, got %q", addr)
	}

	cfg = &config.Config{Server: config.ServerConfig{AllowEmptyToken: true}}
	if _, allowEmpty := mergeServeOpts(cfg, Opts{}); !allowEmpty {
		t.Fatal("config allow_empty_token must hold without the flag")
	}
	cfg = &config.Config{}
	if _, allowEmpty := mergeServeOpts(cfg, Opts{AllowEmptyTok: true}); !allowEmpty {
		t.Fatal("--allow-empty-token must opt out of auth on its own")
	}
}

// TestMergeServeOptsWebEnabled: --no-web force-disables; otherwise the config's
// web_enabled (default true) decides.
func TestMergeServeOptsWebEnabled(t *testing.T) {
	cfg := &config.Config{}
	mergeServeOpts(cfg, Opts{})
	if !cfg.Server.IsWebEnabled() {
		t.Fatal("web console must be on by default")
	}
	mergeServeOpts(cfg, Opts{NoWeb: true})
	if cfg.Server.IsWebEnabled() {
		t.Fatal("--no-web must force-disable the web console")
	}
}
