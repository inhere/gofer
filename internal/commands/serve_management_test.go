package commands

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/daemon"
	"github.com/inhere/gofer/internal/servicemgr"
)

func TestServeUpgradeStatusReadsNamedArgumentBeforeAndAfterFlags(t *testing.T) {
	root := t.TempDir()
	t.Setenv(config.EnvConfigDir, root)
	m, err := servicemgr.NewManager(root, servicemgr.DefaultName)
	assert.Require(t, assert.NoErr(t, err))
	id := "0123456789abcdef0123456789abcdef"
	assert.Require(t, assert.NoErr(t, m.SaveUpgradeReceipt(servicemgr.UpgradeReceipt{
		SchemaVersion: servicemgr.UpgradeSchema, UpgradeID: id, Name: m.Name,
		Phase: servicemgr.UpgradeSucceeded, CandidateVersion: "plain-v2", StartedAt: time.Now(),
	})))
	for _, args := range [][]string{
		{"serve", "upgrade", "status", "--json", id},
		{"serve", "upgrade", "status", id, "--json"},
	} {
		if code := NewApp("test").Run(args); code != 0 {
			t.Fatalf("upgrade status %v returned code %d", args, code)
		}
	}
}

func TestServeManagementHelpIncludesPublicCommands(t *testing.T) {
	serve := NewApp("test").GetCommand("serve")
	for _, name := range []string{"register", "start", "stop", "restart", "uninstall", "status", "logs", "upgrade"} {
		if serve.GetCommand(name) == nil {
			t.Fatalf("serve %s missing", name)
		}
	}
	if serve.GetCommand("upgrade").GetCommand("status") == nil {
		t.Fatal("upgrade status missing")
	}
}

func TestServeStatusDoesNotTrustOtherProcessHealth(t *testing.T) {
	root := t.TempDir()
	m, err := servicemgr.NewManager(root, "gofer-t6-health")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	addr := strings.TrimPrefix(server.URL, "http://")
	configFile := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configFile, []byte("server:\n  addr: "+addr+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	self, err := daemon.CurrentProcessIdentity()
	if err != nil {
		t.Fatal(err)
	}
	spec := servicemgr.Spec{SchemaVersion: servicemgr.SpecSchema, Backend: testManagedBackend(), Name: m.Name,
		Owner: self.Owner, Exe: self.Exe, WorkDir: root, ConfigFile: configFile, ConfigDir: root,
		RuntimeDir: filepath.Join(root, "run"), Serve: servicemgr.ServeOptions{Addr: addr}}
	if err := m.SaveSpec(spec); err != nil {
		t.Fatal(err)
	}
	oldNative, oldPort := managedNativeStatusRead, managedPortOwned
	defer func() { managedNativeStatusRead, managedPortOwned = oldNative, oldPort }()
	managedNativeStatusRead = func(context.Context, *servicemgr.Manager) (nativeServeStatus, error) {
		return nativeServeStatus{Installed: true, Active: true, Server: self}, nil
	}
	checks := 0
	managedPortOwned = func(int, string, uint16) (bool, error) { checks++; return checks == 1, nil }
	out, err := inspectManagedStatus(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if out.Health == "healthy" || out.IdentityError == "" || checks != 2 {
		t.Fatalf("foreign listener accepted: %+v checks=%d", out, checks)
	}
}

func TestServeStatusReadsVersionOnlyFromVerifiedRuntimeStats(t *testing.T) {
	root := t.TempDir()
	m, err := servicemgr.NewManager(root, "gofer-t6-version")
	if err != nil {
		t.Fatal(err)
	}
	const token = "version-test-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(http.StatusOK)
		case "/v1/stats":
			if r.Header.Get("Authorization") != "Bearer "+token {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"version":"runtime-v9"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	addr := strings.TrimPrefix(server.URL, "http://")
	configFile := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configFile, []byte("server:\n  addr: "+addr+"\n  token_env: GOFER_T6_VERSION_TOKEN\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOFER_T6_VERSION_TOKEN", token)
	t.Setenv(config.EnvJobToken, "")
	self, err := daemon.CurrentProcessIdentity()
	if err != nil {
		t.Fatal(err)
	}
	spec := servicemgr.Spec{SchemaVersion: servicemgr.SpecSchema, Backend: testManagedBackend(), Name: m.Name,
		Owner: self.Owner, Exe: self.Exe, WorkDir: root, ConfigFile: configFile, ConfigDir: root,
		RuntimeDir: filepath.Join(root, "run"), Serve: servicemgr.ServeOptions{Addr: addr}, RegisteredVersion: "registered-v1"}
	if err := m.SaveSpec(spec); err != nil {
		t.Fatal(err)
	}
	oldNative, oldPort := managedNativeStatusRead, managedPortOwned
	defer func() { managedNativeStatusRead, managedPortOwned = oldNative, oldPort }()
	managedNativeStatusRead = func(context.Context, *servicemgr.Manager) (nativeServeStatus, error) {
		return nativeServeStatus{Installed: true, Active: true, Server: self}, nil
	}
	managedPortOwned = func(int, string, uint16) (bool, error) { return true, nil }
	out, err := inspectManagedStatus(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if out.Health != "healthy" || out.RunningVersion != "runtime-v9" || out.RegisteredVersion != "registered-v1" {
		t.Fatalf("runtime version not attributed: %+v", out)
	}
	t.Setenv("GOFER_T6_VERSION_TOKEN", "wrong")
	out, err = inspectManagedStatus(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if out.Health != "healthy" || out.RunningVersion != "" || out.VersionError != "stats returned HTTP 401" {
		t.Fatalf("unavailable version was guessed: %+v", out)
	}
}

func TestServeRegisterStartPreflightHasNoNativeSideEffect(t *testing.T) {
	root := t.TempDir()
	configFile := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configFile, []byte("server:\n  tls:\n    addr: 127.0.0.1:9443\n    cert_file: '{config_dir}/missing.crt'\n    key_file: '{config_dir}/missing.key'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvConfigDir, root)
	old := managedRegisterNative
	called := false
	managedRegisterNative = func(context.Context, *servicemgr.Manager, servicemgr.Spec, serveRegisterOptions) error {
		called = true
		return nil
	}
	t.Cleanup(func() { managedRegisterNative = old })
	code := NewApp("test").Run([]string{"serve", "register", "--name", "gofer-t6-preflight", "-c", configFile, "--start", "--allow-empty-token"})
	if code == 0 || called {
		t.Fatalf("invalid TLS start registered native entry: code=%d called=%v", code, called)
	}
}

func TestServeRegisterConfigFlagLocationsAndFrozenRuntime(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "assets")
	configFileDir := filepath.Join(root, "chosen")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(configFileDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(configFileDir, "config.yaml")
	if err := os.WriteFile(configFile, []byte("server: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvConfigDir, configDir)
	old := managedRegisterNative
	var seen []servicemgr.Spec
	managedRegisterNative = func(_ context.Context, _ *servicemgr.Manager, spec servicemgr.Spec, _ serveRegisterOptions) error {
		seen = append(seen, spec)
		return nil
	}
	t.Cleanup(func() { managedRegisterNative = old })
	for _, args := range [][]string{
		{"-c", configFile, "serve", "register", "--name", "gofer-t6-config"},
		{"serve", "register", "--name", "gofer-t6-config", "-c", configFile},
	} {
		config.InputCfgFile = ""
		if code := NewApp("test").Run(args); code != 0 {
			t.Fatalf("register %v = %d", args, code)
		}
	}
	if len(seen) != 2 {
		t.Fatalf("register called %d times", len(seen))
	}
	for _, spec := range seen {
		if spec.ConfigFile != configFile || spec.ConfigDir != configDir || spec.RuntimeDir != filepath.Join(configFileDir, "run") {
			t.Fatalf("selected config paths drifted: %+v", spec)
		}
		if !strings.HasPrefix(spec.Exe, root) && !filepath.IsAbs(spec.Exe) {
			t.Fatal("executable was not absolute")
		}
	}
}
