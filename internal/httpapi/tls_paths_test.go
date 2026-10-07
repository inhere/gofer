package httpapi

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
	"github.com/inhere/gofer/internal/certutil"
	"github.com/inhere/gofer/internal/config"
)

func TestRunCtxTLSConfigDirPaths(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "配置 space")
	t.Setenv(config.EnvConfigDir, dir)
	_, err := certutil.Generate(filepath.Join(dir, "certs"), []string{"localhost"})
	assert.Require(t, assert.NoErr(t, err))
	srv := newTestServer(t, "", true)
	tlsCfg := &config.TLSConfig{
		Addr: "127.0.0.1:0", CertFile: "{config_dir}/certs/server.crt", KeyFile: "{config_dir}/certs/server.key",
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.RunCtxTLS(ctx, "127.0.0.1:0", tlsCfg) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		assert.NoErr(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("TLS listener did not stop")
	}
	assert.Eq(t, "{config_dir}/certs/server.crt", tlsCfg.CertFile)

	bad := *tlsCfg
	bad.KeyFile = "{missing}/server.key"
	err = srv.RunCtxTLS(context.Background(), "127.0.0.1:0", &bad)
	assert.Require(t, assert.Err(t, err))
	assert.Eq(t, true, strings.Contains(err.Error(), "server.tls.key_file"))
}
