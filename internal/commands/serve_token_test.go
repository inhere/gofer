package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/daemon"
	"github.com/inhere/gofer/internal/servicemgr"
)

// neutralPersistentEnv pins the persistent-environment side to empty, so a test
// never reads the machine's real Windows registry (gofer-t9md).
func neutralPersistentEnv(t *testing.T) {
	t.Helper()
	old := managedPersistentEnv
	managedPersistentEnv = func() []config.PersistentEnvVar { return nil }
	t.Cleanup(func() { managedPersistentEnv = old })
}

// TestManagedTokenComparison is the gofer-t9md core: the token this terminal uses
// and the token the managed service will use are resolved from DIFFERENT
// environments (the terminal's vs. the config dir's .env only), so a token
// exported for a hand-run serve is exactly the drift that breaks web login after
// `serve register`.
func TestManagedTokenComparison(t *testing.T) {
	const (
		svcTok  = "token-service-sees"
		termTok = "token-terminal-exported"
		litTok  = "token-config-literal"
		envName = "GOFER_T9MD_TOKEN"
	)
	cases := []struct {
		name        string
		server      config.ServerConfig
		dotenvLine  string // content of <config-dir>/.env ("" = no file)
		processTok  string // value exported in this terminal ("" = unset)
		wantService config.TokenSourceKind
		wantCurrent config.TokenSourceKind
		wantMatch   bool
	}{
		{
			name:        "exported token shadows the service's .env token",
			server:      config.ServerConfig{TokenEnv: envName},
			dotenvLine:  envName + "=" + svcTok + "\n",
			processTok:  termTok,
			wantService: config.TokenSourceEnvFile,
			wantCurrent: config.TokenSourceProcess,
			wantMatch:   false,
		},
		{
			name:        "same token in the .env and the terminal",
			server:      config.ServerConfig{TokenEnv: envName},
			dotenvLine:  envName + "=" + svcTok + "\n",
			processTok:  svcTok,
			wantService: config.TokenSourceEnvFile,
			wantCurrent: config.TokenSourceEnvFile,
			wantMatch:   true,
		},
		{
			name:        "only the terminal has one: the service has none",
			server:      config.ServerConfig{TokenEnv: envName},
			processTok:  termTok,
			wantService: config.TokenSourceUnset,
			wantCurrent: config.TokenSourceProcess,
			wantMatch:   false,
		},
		{
			name:        "the config literal is what both sides read",
			server:      config.ServerConfig{Token: litTok},
			processTok:  termTok, // irrelevant: no token_env names it
			wantService: config.TokenSourceConfig,
			wantCurrent: config.TokenSourceConfig,
			wantMatch:   true,
		},
		{
			name:        "neither side has one",
			server:      config.ServerConfig{TokenEnv: envName},
			wantService: config.TokenSourceUnset,
			wantCurrent: config.TokenSourceUnset,
			wantMatch:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			neutralPersistentEnv(t)
			dir := t.TempDir()
			// A hand-run CLI is not inside a job, so the dotenv files are read for real
			// (inside a job LoadDotenv skips their *_TOKEN keys).
			unsetCommandsEnv(t, config.EnvJobToken)
			// The terminal's config dir is the one the service will use: same file, two views.
			t.Setenv(config.EnvConfigDir, dir)
			if tc.dotenvLine != "" {
				writeCommandsFile(t, filepath.Join(dir, config.EnvFileName), tc.dotenvLine)
			}
			t.Setenv(envName, tc.processTok)
			spec := servicemgr.Spec{ConfigDir: dir, WorkDir: dir}
			cfg := &config.Config{Server: tc.server}

			cmp := compareManagedToken(cfg, spec, config.ProcessEnvView())
			if cmp.Service.Kind != tc.wantService {
				t.Errorf("service kind = %q (%+v), want %q", cmp.Service.Kind, cmp.Service, tc.wantService)
			}
			if cmp.Current.Kind != tc.wantCurrent {
				t.Errorf("current kind = %q (%+v), want %q", cmp.Current.Kind, cmp.Current, tc.wantCurrent)
			}
			if cmp.Match != tc.wantMatch {
				t.Errorf("match = %v, want %v (service=%+v current=%+v)", cmp.Match, tc.wantMatch, cmp.Service, cmp.Current)
			}
			// The warning is exactly the mismatch, and it never holds a token value.
			lines := tokenWarningLines(cmp, cfg, spec)
			if tc.wantMatch && len(lines) != 0 {
				t.Fatalf("matched sides warned anyway: %v", lines)
			}
			if !tc.wantMatch {
				if len(lines) == 0 {
					t.Fatal("mismatched sides printed no warning")
				}
				out := strings.Join(lines, "\n")
				for _, secret := range []string{svcTok, termTok, litTok} {
					if strings.Contains(out, secret) {
						t.Fatalf("the warning leaked a token value:\n%s", out)
					}
				}
				// The fix has to name the file to write and the variable to use.
				if !strings.Contains(out, filepath.Join(dir, config.EnvFileName)) || !strings.Contains(out, envName) {
					t.Fatalf("the warning does not say where to put the token:\n%s", out)
				}
				if !strings.Contains(out, config.TokenMask(termTok)) && !strings.Contains(out, config.TokenMask(svcTok)) {
					t.Fatalf("the warning carries no mask to compare:\n%s", out)
				}
			}
		})
	}
}

// TestManagedTokenWarningLocalizedAndValueFree runs the same rendering under both
// languages: the warning must exist and stay value-free in each.
func TestManagedTokenWarningLocalizedAndValueFree(t *testing.T) {
	const secret = "s3cr3t-do-not-print"
	neutralPersistentEnv(t)
	dir := t.TempDir()
	writeCommandsFile(t, filepath.Join(dir, config.EnvFileName), "GOFER_T9MD_LANG_TOKEN="+secret+"\n")
	t.Setenv("GOFER_T9MD_LANG_TOKEN", secret+"-exported")
	cfg := &config.Config{Server: config.ServerConfig{TokenEnv: "GOFER_T9MD_LANG_TOKEN"}}
	spec := servicemgr.Spec{ConfigDir: dir, WorkDir: dir}
	cmp := compareManagedToken(cfg, spec, config.ProcessEnvView())
	if cmp.Match {
		t.Fatal("the fixture was supposed to differ")
	}
	for _, lang := range []string{"C.UTF-8", "zh_CN.UTF-8"} {
		t.Run(lang, func(t *testing.T) {
			t.Setenv("GOFER_LANG", "")
			t.Setenv("LC_ALL", lang)
			t.Setenv("LANG", lang)
			out := strings.Join(tokenWarningLines(cmp, cfg, spec), "\n")
			if out == "" {
				t.Fatal("no warning")
			}
			if strings.Contains(out, secret) {
				t.Fatalf("leaked the token under %s:\n%s", lang, out)
			}
			if status := strings.Join(tokenStatusLines(&serveAuthTokenStatus{Service: cmp.Service,
				CurrentProcess: cmp.Current, Match: cmp.Match}), "\n"); strings.Contains(status, secret) || status == "" {
				t.Fatalf("status rows under %s: %q", lang, status)
			}
		})
	}
	t.Setenv("LC_ALL", "")
	t.Setenv("LANG", "")
}

// TestServeStatusJSONReportsTokenSource pins the machine-readable side of
// gofer-t9md: `serve status --json` carries both sources and whether they agree,
// and no token value.
func TestServeStatusJSONReportsTokenSource(t *testing.T) {
	neutralPersistentEnv(t)
	root := t.TempDir()
	const (
		secret  = "service-side-secret-token"
		envName = "GOFER_T9MD_STATUS_TOKEN"
	)
	writeCommandsFile(t, filepath.Join(root, config.EnvFileName), envName+"="+secret+"\n")
	t.Setenv(envName, "terminal-side-token")
	unsetCommandsEnv(t, config.EnvJobToken)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	addr := strings.TrimPrefix(server.URL, "http://")
	configFile := filepath.Join(root, "config.yaml")
	writeCommandsFile(t, configFile, "server:\n  addr: "+addr+"\n  token_env: "+envName+"\n  callers:\n    - id: ops\n      token_env: GOFER_T9MD_STATUS_ABSENT\n")
	t.Setenv(config.EnvConfigDir, root)

	self, err := daemon.CurrentProcessIdentity()
	if err != nil {
		t.Fatal(err)
	}
	m, err := servicemgr.NewManager(root, "gofer-t9md-status")
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
	managedPortOwned = func(int, string, uint16) (bool, error) { return true, nil }

	out, err := inspectManagedStatus(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if out.AuthToken == nil {
		t.Fatal("auth_token missing from the status output")
	}
	if out.AuthToken.Service.Kind != config.TokenSourceEnvFile ||
		out.AuthToken.Service.EnvFile != filepath.Join(root, config.EnvFileName) ||
		out.AuthToken.Service.Masked != config.TokenMask(secret) {
		t.Fatalf("service token source = %+v", out.AuthToken.Service)
	}
	if out.AuthToken.CurrentProcess.Kind != config.TokenSourceProcess || out.AuthToken.Match {
		t.Fatalf("current token source = %+v match=%v", out.AuthToken.CurrentProcess, out.AuthToken.Match)
	}
	// Every credential the server accepts is reported from the service's view, so a
	// caller whose variable is empty there cannot hide.
	var callerSeen bool
	for _, cred := range out.AuthToken.Credentials {
		if cred.Role != "caller:ops" {
			continue
		}
		callerSeen = true
		if cred.Set || cred.EnvVar != "GOFER_T9MD_STATUS_ABSENT" || cred.Kind != config.TokenSourceUnset {
			t.Errorf("caller:ops credential = %+v, want an unset named variable", cred)
		}
	}
	if !callerSeen {
		t.Fatalf("credentials = %+v, want the caller entries too", out.AuthToken.Credentials)
	}
	if text := strings.Join(tokenStatusLines(out.AuthToken), "\n"); !strings.Contains(text, "caller:ops") {
		t.Fatalf("status text does not report the unresolvable caller:\n%s", text)
	}

	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "terminal-side-token") {
		t.Fatalf("status JSON leaked a token value: %s", encoded)
	}
	var decoded struct {
		AuthToken struct {
			Service struct {
				Kind    string `json:"kind"`
				EnvVar  string `json:"env_var"`
				EnvFile string `json:"env_file"`
				Masked  string `json:"masked"`
				Length  int    `json:"length"`
			} `json:"service"`
			CurrentProcess struct {
				Kind string `json:"kind"`
			} `json:"current_process"`
			Match bool `json:"match"`
		} `json:"auth_token"`
		ConfigFiles []string `json:"config_files"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.AuthToken.Service.Kind != "env-file" || decoded.AuthToken.Service.EnvVar != envName ||
		decoded.AuthToken.Service.Masked != config.TokenMask(secret) ||
		decoded.AuthToken.Service.Length != len(secret) || decoded.AuthToken.Match {
		t.Fatalf("decoded auth_token = %+v", decoded.AuthToken)
	}
	// The old fingerprint field is gone: a --json consumer reads the mask now.
	if strings.Contains(string(encoded), "fingerprint") {
		t.Fatalf("status JSON still carries a fingerprint field: %s", encoded)
	}
	// The config file(s) and the .env files this run read are machine-readable too.
	if len(decoded.ConfigFiles) == 0 || decoded.ConfigFiles[0] != configFile {
		t.Fatalf("config_files = %v, want %q first", decoded.ConfigFiles, configFile)
	}
	if text := strings.Join(statusFileLines(&out), "\n"); !strings.Contains(text, configFile) {
		t.Fatalf("status text does not name the config file:\n%s", text)
	}
}

// TestManagedStartPreflightUsesTheServiceToken: `serve register --start` must
// refuse when the SERVICE has no token, even if this terminal exported one — a
// check against os.Getenv (the old one) accepted such a registration and the
// service then refused to start (gofer-t9md).
func TestManagedStartPreflightUsesTheServiceToken(t *testing.T) {
	const envName = "GOFER_T9MD_PREFLIGHT_TOKEN"
	neutralPersistentEnv(t)
	configDir := t.TempDir()
	t.Setenv(envName, "exported-only-in-this-terminal")
	unsetCommandsEnv(t, config.EnvJobToken)
	cfg := &config.Config{Server: config.ServerConfig{TokenEnv: envName}}
	spec := servicemgr.Spec{ConfigDir: configDir, WorkDir: configDir}

	if err := preflightManagedAssets(cfg, spec); err == nil {
		t.Fatal("a token only this terminal sees passed the preflight")
	}

	// The token in the config dir's .env is what the service will read.
	writeCommandsFile(t, filepath.Join(configDir, config.EnvFileName), envName+"=service-side\n")
	if err := preflightManagedAssets(cfg, spec); err != nil {
		t.Fatalf("a service-side token was refused: %v", err)
	}

	// allow_empty_token is the documented way to start without one.
	if err := os.Remove(filepath.Join(configDir, config.EnvFileName)); err != nil {
		t.Fatal(err)
	}
	spec.Serve.AllowEmptyToken = true
	if err := preflightManagedAssets(cfg, spec); err != nil {
		t.Fatalf("allow_empty_token was not honoured: %v", err)
	}
}

// TestManagedTokenPersistentEnvShadow: a Windows persistent environment variable
// the service inherits beats <config-dir>/.env, so the operator who edits the file
// sees no effect. The warning has to name the inherited value (masked), what the
// file would give, and the command that removes it (gofer-t9md).
func TestManagedTokenPersistentEnvShadow(t *testing.T) {
	const (
		inherited = "stale-persistent-token"
		fileTok   = "token-in-the-dotenv-file"
		envName   = "GOFER_T9MD_PERSIST_TOKEN"
	)
	dir := t.TempDir()
	writeCommandsFile(t, filepath.Join(dir, config.EnvFileName), envName+"="+fileTok+"\n")
	unsetCommandsEnv(t, config.EnvJobToken)
	unsetCommandsEnv(t, envName)
	old := managedPersistentEnv
	managedPersistentEnv = func() []config.PersistentEnvVar {
		return []config.PersistentEnvVar{{Scope: config.PersistentEnvScopeUser, Name: envName, Value: inherited}}
	}
	t.Cleanup(func() { managedPersistentEnv = old })
	cfg := &config.Config{Server: config.ServerConfig{TokenEnv: envName}}
	spec := servicemgr.Spec{ConfigDir: dir, WorkDir: dir}

	cmp := compareManagedToken(cfg, spec, config.ProcessEnvView())
	if cmp.Service.Kind != config.TokenSourcePersistentEnv || cmp.Service.Scope != config.PersistentEnvScopeUser {
		t.Fatalf("service source = %+v, want the persistent environment", cmp.Service)
	}
	if cmp.Service.Masked != config.TokenMask(inherited) {
		t.Fatalf("service mask = %q, want %q", cmp.Service.Masked, config.TokenMask(inherited))
	}
	if cmp.Persistent == nil {
		t.Fatal("the shadowing persistent variable was not reported")
	}
	if cmp.Persistent.Shadowed.Kind != config.TokenSourceEnvFile || cmp.Persistent.Shadowed.EnvFile != filepath.Join(dir, config.EnvFileName) {
		t.Fatalf("shadowed source = %+v", cmp.Persistent.Shadowed)
	}

	out := strings.Join(tokenWarningLines(cmp, cfg, spec), "\n")
	clearCmd := "[Environment]::SetEnvironmentVariable('" + envName + "',$null,'User')"
	if !strings.Contains(out, clearCmd) {
		t.Fatalf("the warning does not give the command that removes it:\n%s", out)
	}
	if !strings.Contains(out, config.TokenMask(fileTok)) || !strings.Contains(out, config.TokenMask(inherited)) {
		t.Fatalf("the warning does not show both masks:\n%s", out)
	}
	for _, secret := range []string{inherited, fileTok} {
		if strings.Contains(out, secret) {
			t.Fatalf("the warning leaked a token value:\n%s", out)
		}
	}

	st := &serveAuthTokenStatus{Service: cmp.Service, CurrentProcess: cmp.Current, Match: cmp.Match, Persistent: cmp.Persistent}
	if status := strings.Join(tokenStatusLines(st), "\n"); !strings.Contains(status, clearCmd) {
		t.Fatalf("serve status does not report the shadowing variable:\n%s", status)
	}
	encoded, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"persistent_env"`) || !strings.Contains(string(encoded), config.TokenMask(inherited)) {
		t.Fatalf("status JSON does not report the shadowing variable: %s", encoded)
	}
	for _, secret := range []string{inherited, fileTok} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("status JSON leaked a token value: %s", encoded)
		}
	}
}

// TestUnreadDotenvFilesAreReported: the registering terminal can load a dotenv
// file the service never reads (its working directory is the config dir), which is
// one more reason the two tokens differ.
func TestUnreadDotenvFilesAreReported(t *testing.T) {
	dir := t.TempDir()
	spec := servicemgr.Spec{ConfigDir: dir, WorkDir: dir}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	loaded := []string{filepath.Join(dir, config.EnvFileName), "./" + config.EnvFileName}
	got := unreadDotenvFilesWith(loaded, spec)
	want := filepath.Join(cwd, config.EnvFileName)
	if len(got) != 1 || got[0] != want {
		t.Fatalf("unread = %v, want [%s]", got, want)
	}
	// The file the service DOES read is not listed.
	for _, file := range got {
		if file == filepath.Join(dir, config.EnvFileName) {
			t.Fatalf("the service's own .env was reported unread: %v", got)
		}
	}
	if text := strings.Join(tokenDiffLines(nil, got), "\n"); !strings.Contains(text, want) {
		t.Fatalf("the reason line does not name the file:\n%s", text)
	}
}

// TestStatusVersionUnauthorizedHintsAtTheToken: a 401 from /v1/stats used to be a
// bare "stats returned HTTP 401". It has to say that the CLI's token is not the
// service's, by source and mask (gofer-t9md).
func TestStatusVersionUnauthorizedHintsAtTheToken(t *testing.T) {
	neutralPersistentEnv(t)
	root := t.TempDir()
	const (
		svrTok  = "service-side-token-0123456789ab"
		cliTok  = "cli-side-token-9876543210zyxw"
		envName = "GOFER_T9MD_401_TOKEN"
	)
	writeCommandsFile(t, filepath.Join(root, config.EnvFileName), envName+"="+svrTok+"\n")
	t.Setenv(config.EnvConfigDir, root)
	t.Setenv(envName, "")
	unsetCommandsEnv(t, config.EnvJobToken)
	t.Setenv(config.EnvServerToken, cliTok)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/stats" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	addr := strings.TrimPrefix(server.URL, "http://")
	configFile := filepath.Join(root, "config.yaml")
	writeCommandsFile(t, configFile, "server:\n  addr: "+addr+"\n  token_env: "+envName+"\n")
	self, err := daemon.CurrentProcessIdentity()
	if err != nil {
		t.Fatal(err)
	}
	m, err := servicemgr.NewManager(root, "gofer-t9md-401")
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
	managedPortOwned = func(int, string, uint16) (bool, error) { return true, nil }

	out, err := inspectManagedStatus(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if out.VersionError != "stats returned HTTP 401" {
		t.Fatalf("version_error = %q", out.VersionError)
	}
	mismatch := out.VersionAuthMismatch
	if mismatch == nil {
		t.Fatal("a 401 was reported without the token diagnosis")
	}
	if mismatch.CLI.EnvVar != config.EnvServerToken || mismatch.CLI.Masked != config.TokenMask(cliTok) {
		t.Fatalf("CLI side = %+v", mismatch.CLI)
	}
	if mismatch.Server.Kind != config.TokenSourceEnvFile || mismatch.Server.Masked != config.TokenMask(svrTok) {
		t.Fatalf("server side = %+v", mismatch.Server)
	}
	lines := strings.Join(versionAuthHintLines(mismatch), "\n")
	if lines == "" || !strings.Contains(lines, config.EnvServerToken) ||
		!strings.Contains(lines, config.TokenMask(cliTok)) || !strings.Contains(lines, config.TokenMask(svrTok)) {
		t.Fatalf("hint = %q", lines)
	}
	for _, secret := range []string{svrTok, cliTok} {
		if strings.Contains(lines, secret) {
			t.Fatalf("the hint leaked a token value: %s", lines)
		}
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"version_auth_mismatch"`) {
		t.Fatalf("status JSON lost the diagnosis: %s", encoded)
	}
	for _, secret := range []string{svrTok, cliTok} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("status JSON leaked a token value: %s", encoded)
		}
	}
}

func writeCommandsFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// unsetCommandsEnv removes key for the test: "absent" and "empty" are different
// answers for both LoadDotenv and the job marker GOFER_JOB_TOKEN.
func unsetCommandsEnv(t *testing.T, key string) {
	t.Helper()
	old, had := os.LookupEnv(key)
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, old)
			return
		}
		_ = os.Unsetenv(key)
	})
	_ = os.Unsetenv(key)
}
