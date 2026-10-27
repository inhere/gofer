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
				if !strings.Contains(out, config.TokenFingerprint(termTok)) && !strings.Contains(out, config.TokenFingerprint(svcTok)) {
					t.Fatalf("the warning carries no fingerprint to compare:\n%s", out)
				}
			}
		})
	}
}

// TestManagedTokenWarningLocalizedAndValueFree runs the same rendering under both
// languages: the warning must exist and stay value-free in each.
func TestManagedTokenWarningLocalizedAndValueFree(t *testing.T) {
	const secret = "s3cr3t-do-not-print"
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
		out.AuthToken.Service.Fingerprint != config.TokenFingerprint(secret) {
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
				Kind        string `json:"kind"`
				EnvVar      string `json:"env_var"`
				EnvFile     string `json:"env_file"`
				Fingerprint string `json:"fingerprint"`
				Length      int    `json:"length"`
			} `json:"service"`
			CurrentProcess struct {
				Kind string `json:"kind"`
			} `json:"current_process"`
			Match bool `json:"match"`
		} `json:"auth_token"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.AuthToken.Service.Kind != "env-file" || decoded.AuthToken.Service.EnvVar != envName ||
		decoded.AuthToken.Service.Length != len(secret) || decoded.AuthToken.Match {
		t.Fatalf("decoded auth_token = %+v", decoded.AuthToken)
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
