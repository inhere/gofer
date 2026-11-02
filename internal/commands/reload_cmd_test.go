package commands

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/daemon"
)

func TestServeReloadCommandRegistered(t *testing.T) {
	cmd := NewServeCmd()
	if findSub(t, cmd, "reload") == nil {
		t.Fatal("serve reload must be registered")
	}
}

func TestWorkerReloadLocalFlagRegistered(t *testing.T) {
	cmd := NewWorkerReloadCmd()
	bound := &gcli.Command{Name: cmd.Name}
	cmd.Config(bound)
	if bound.Opt("local") == nil {
		t.Fatal("worker reload must bind --local")
	}
}

// serveReloadFixture points the command at a config whose server.addr is addr and a
// pidfile naming a process that is not running.
func serveReloadFixture(t *testing.T, addr string, writePID bool) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(config.EnvConfigDir, dir)
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("server:\n  addr: "+addr+"\n  token: tok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prev := config.InputCfgFile
	config.InputCfgFile = cfgPath
	t.Cleanup(func() { config.InputCfgFile = prev })
	if writePID {
		if err := os.MkdirAll(filepath.Dir(servePIDFile()), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := daemon.WritePIDFile(servePIDFile(), 2147483646); err != nil {
			t.Fatal(err)
		}
	}
}

// A pidfile naming a dead process must not stop `serve reload` while the server still
// answers: it reloads over HTTP.
func TestServeReloadStalePidfileFallsBackToHTTP(t *testing.T) {
	var gotAuth string
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/config/reload" {
			hits++
			gotAuth = r.Header.Get("Authorization")
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "reloaded": true, "rev": 7, "path": "p.yaml",
				"changed": []string{"agents"}, "restart_required": []string{}})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	serveReloadFixture(t, strings.TrimPrefix(srv.URL, "http://"), true)
	c := bindCmd(NewServeReloadCmd())
	if err := runServeReload(c, nil); err != nil {
		t.Fatalf("stale pidfile with a reachable server: %v", err)
	}
	if hits != 1 || gotAuth != "Bearer tok" {
		t.Fatalf("hits=%d auth=%q, want one authenticated reload", hits, gotAuth)
	}
}

// A missing pidfile takes the same route.
func TestServeReloadMissingPidfileFallsBackToHTTP(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "reloaded": true, "rev": 1})
	}))
	defer srv.Close()
	serveReloadFixture(t, strings.TrimPrefix(srv.URL, "http://"), false)
	if err := runServeReload(bindCmd(NewServeReloadCmd()), nil); err != nil || hits != 1 {
		t.Fatalf("err=%v hits=%d", err, hits)
	}
}

// Stale pidfile and no server: a clear error naming the stale pidfile and the alternative.
func TestServeReloadStalePidfileAndNoServer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // nothing listens there now
	serveReloadFixture(t, addr, true)
	err = runServeReload(bindCmd(NewServeReloadCmd()), nil)
	if err == nil {
		t.Fatal("want an error when neither the pidfile nor the server is usable")
	}
	for _, want := range []string{"not running", "stale", "gofer tool api config/reload -X POST"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	assertCodedExit(t, err)
}
