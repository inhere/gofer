package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

func TestResolveLocalPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "space 中文")
	t.Setenv(EnvConfigDir, dir)
	got, err := ResolveLocalPath("server.tls.cert_file", "{config_dir}/certs/server.crt")
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, filepath.Join(dir, "certs", "server.crt"), got)
	got, err = ResolveLocalPath("log.file", `relative/log.jsonl`)
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, `relative/log.jsonl`, got)
	got, err = ResolveLocalPath("log.dir", "")
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, "", got)

	for _, value := range []string{"{other}/certs", "prefix/{config_dir}/certs", "{config_dir}suffix", "{config_dir}/{other}"} {
		_, err := ResolveLocalPath("server.tls.cert_file", value)
		assert.Require(t, assert.Err(t, err), value)
		assert.Eq(t, true, strings.Contains(err.Error(), "server.tls.cert_file"))
	}
}

func TestLocalPathConfigRoundTripAndConfigSelection(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "配置 space")
	t.Setenv(EnvConfigDir, dir)
	selected := filepath.Join(t.TempDir(), "custom.yaml")
	raw := `server:
  web_dir: "{config_dir}/web"
  tls:
    addr: 127.0.0.1:9443
    cert_file: "{config_dir}/certs/server.crt"
    key_file: "{config_dir}/certs/server.key"
log:
  file: "{config_dir}/run/serve.log"
storage:
  root: "{config_dir}/results"
agents:
  demo:
    type: cli-agent
    command: demo
    args: ["{config_dir}", "{{prompt}}"]
projects:
  remote:
    host_path: "{config_dir}/remote"
    container_path: "{config_dir}/container"
`
	assert.Require(t, assert.NoErr(t, os.WriteFile(selected, []byte(raw), 0o600)))
	cfg, loaded, err := Load(selected)
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, selected, loaded)
	assert.Eq(t, "{config_dir}/web", cfg.Server.WebDir)
	assert.Eq(t, "{config_dir}/remote", cfg.Projects["remote"].HostPath)
	assert.Eq(t, "{config_dir}", cfg.Agents["demo"].Args[0])
	root, err := cfg.ResolveStorageRoot()
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, filepath.Join(dir, "results"), root)
	assert.Eq(t, filepath.Join(dir, "results", DBFileName), cfg.ResolveDBPath())
	assert.Require(t, assert.NoErr(t, Save(selected, cfg)))
	stored, err := os.ReadFile(selected)
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, true, strings.Contains(string(stored), `cert_file: "{config_dir}/certs/server.crt"`))
	again, _, err := Load(selected)
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, "{config_dir}/web", again.Server.WebDir)
	assert.Eq(t, "{config_dir}/remote", again.Projects["remote"].HostPath)
}

func TestLocalPathDefaultAndUnknownField(t *testing.T) {
	t.Setenv(EnvConfigDir, "")
	dir, err := ConfigDir()
	assert.Require(t, assert.NoErr(t, err))
	got, err := ResolveLocalPath("storage.db_path", "{config_dir}/gofer.db")
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, filepath.Join(dir, "gofer.db"), got)
	path := filepath.Join(t.TempDir(), "bad.yaml")
	assert.Require(t, assert.NoErr(t, os.WriteFile(path, []byte("server:\n  tls:\n    cert_file: '{unknown}/cert.crt'\n"), 0o600)))
	_, _, err = Load(path)
	assert.Require(t, assert.Err(t, err))
	assert.Eq(t, true, strings.Contains(err.Error(), "server.tls.cert_file"))
}

func TestLocalPathSupportedFieldsRejectUnknownBeforeSave(t *testing.T) {
	for _, tc := range []struct {
		field string
		set   func(*Config)
	}{
		{"server.web_dir", func(c *Config) { c.Server.WebDir = "{wrong}/web" }},
		{"server.tls.key_file", func(c *Config) { c.Server.TLS = &TLSConfig{KeyFile: "{wrong}/key"} }},
		{"log.file", func(c *Config) { c.Log.File = "{wrong}/serve.log" }},
		{"log.dir", func(c *Config) { c.Log.Dir = "{wrong}/run" }},
		{"storage.root", func(c *Config) { c.Storage.Root = "{wrong}/results" }},
		{"storage.db_path", func(c *Config) { c.Storage.DBPath = "{wrong}/gofer.db" }},
	} {
		t.Run(tc.field, func(t *testing.T) {
			cfg := &Config{}
			tc.set(cfg)
			path := filepath.Join(t.TempDir(), "config.yaml")
			err := Save(path, cfg)
			assert.Require(t, assert.Err(t, err))
			assert.Eq(t, true, strings.Contains(err.Error(), tc.field))
			_, statErr := os.Stat(path)
			assert.Eq(t, true, os.IsNotExist(statErr))
		})
	}
}
