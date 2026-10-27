package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolveServerToken pins the server's token precedence together with the
// SOURCE it reports (gofer-t9md): --token > server.token_env (non-empty value) >
// server.token, with an env value attributed to the dotenv file it came from or
// to the process environment. A value the process exported beats the file
// (LoadDotenv's rule), which is exactly the drift a managed service exposes.
func TestResolveServerToken(t *testing.T) {
	const (
		envFile  = "/etc/gofer/.env"
		fileTok  = "token-from-dotenv-file"
		procTok  = "token-exported-in-terminal"
		cfgTok   = "token-in-config-literal"
		flagTok  = "token-from-flag"
		customEn = "GOFER_T9MD_CUSTOM"
	)

	t.Run("flag wins over everything", func(t *testing.T) {
		sc := &ServerConfig{Token: cfgTok, TokenEnv: EnvTokenDefault}
		view := DotenvOnlyEnvView(envFile) // no file on disk: unset
		view.values[EnvTokenDefault] = procTok
		token, src := ResolveServerToken(sc, flagTok, view)
		if token != flagTok || src.Kind != TokenSourceFlag || !src.Set {
			t.Fatalf("token=%q src=%+v", token, src)
		}
		if src.Fingerprint != TokenFingerprint(flagTok) || src.Length != len(flagTok) {
			t.Fatalf("flag source lost its value-free id: %+v", src)
		}
	})

	t.Run("exported env wins over the dotenv file and the literal", func(t *testing.T) {
		dir := t.TempDir()
		file := filepath.Join(dir, EnvFileName)
		writeFile(t, file, "GOFER_TOKEN="+fileTok+"\n")
		t.Setenv(EnvConfigDir, dir)
		t.Setenv(EnvTokenDefault, procTok)
		sc := &ServerConfig{Token: cfgTok, TokenEnv: EnvTokenDefault}

		// The process view sees the exported value; the service view (files only)
		// sees the file's. That difference is the whole bug: same config, two tokens.
		procToken, procSrc := ResolveServerToken(sc, "", ProcessEnvView())
		if procToken != procTok || procSrc.Kind != TokenSourceProcess || procSrc.EnvVar != EnvTokenDefault {
			t.Fatalf("process side: token=%q src=%+v", procToken, procSrc)
		}
		svcToken, svcSrc := ResolveServerToken(sc, "", DotenvOnlyEnvView(file))
		if svcToken != fileTok || svcSrc.Kind != TokenSourceEnvFile || svcSrc.EnvFile != file {
			t.Fatalf("service side: token=%q src=%+v", svcToken, svcSrc)
		}
		if TokenSourceMatches(procSrc, svcSrc) {
			t.Fatal("the two sides were reported as the same token")
		}
	})

	t.Run("only a dotenv file", func(t *testing.T) {
		dir := t.TempDir()
		file := filepath.Join(dir, EnvFileName)
		writeFile(t, file, "# deployment secrets\nGOFER_TOKEN = \""+fileTok+"\" # quoted\n")
		t.Setenv(EnvConfigDir, dir)
		// NOT t.Setenv(EnvTokenDefault, ""): an empty-but-set variable shadows the file
		// for LoadDotenv too (its "exported wins" rule keys on LookupEnv), so unset it.
		unsetEnv(t, EnvTokenDefault)
		// A job's process skips *_TOKEN keys of the dotenv files (the files are the
		// SERVER's); a hand-run CLI is not in a job, so clear that marker too.
		unsetEnv(t, EnvJobToken)
		// The CLI loads the dotenv files before any command runs (cmd/gofer/main.go), so
		// by the time `serve register` reads the environment the file's value IS there —
		// do the same here, which is what lets the report attribute it to the file.
		t.Cleanup(func() { _ = os.Unsetenv(EnvTokenDefault) })
		if _, err := LoadDotenv(); err != nil {
			t.Fatalf("LoadDotenv: %v", err)
		}
		token, src := ResolveServerToken(&ServerConfig{TokenEnv: EnvTokenDefault}, "", ProcessEnvView())
		if token != fileTok || src.Kind != TokenSourceEnvFile || src.EnvFile != file {
			t.Fatalf("token=%q src=%+v", token, src)
		}
	})

	t.Run("only the process environment", func(t *testing.T) {
		t.TempDir() // no config dir .env: GOFER_CONFIG_DIR stays wherever it was
		dir := t.TempDir()
		t.Setenv(EnvConfigDir, dir) // empty directory: no .env file to attribute to
		t.Setenv(customEn, procTok)
		token, src := ResolveServerToken(&ServerConfig{TokenEnv: customEn}, "", ProcessEnvView())
		if token != procTok || src.Kind != TokenSourceProcess || src.EnvVar != customEn {
			t.Fatalf("token=%q src=%+v", token, src)
		}
	})

	t.Run("only the config literal", func(t *testing.T) {
		t.Setenv(EnvConfigDir, t.TempDir())
		t.Setenv(EnvTokenDefault, "")
		token, src := ResolveServerToken(&ServerConfig{Token: cfgTok}, "", ProcessEnvView())
		if token != cfgTok || src.Kind != TokenSourceConfig || src.Field != "server.token" {
			t.Fatalf("token=%q src=%+v", token, src)
		}
	})

	t.Run("token_env empty falls back to the literal", func(t *testing.T) {
		t.Setenv(EnvConfigDir, t.TempDir())
		t.Setenv(EnvTokenDefault, "")
		token, src := ResolveServerToken(&ServerConfig{Token: cfgTok, TokenEnv: EnvTokenDefault}, "", ProcessEnvView())
		if token != cfgTok || src.Kind != TokenSourceConfig {
			t.Fatalf("token=%q src=%+v", token, src)
		}
	})

	t.Run("nothing anywhere", func(t *testing.T) {
		t.Setenv(EnvConfigDir, t.TempDir())
		t.Setenv(EnvTokenDefault, "")
		token, src := ResolveServerToken(&ServerConfig{}, "", ProcessEnvView())
		if token != "" || src.Kind != TokenSourceUnset || src.Set || src.EnvVar != "" {
			t.Fatalf("token=%q src=%+v", token, src)
		}
		// A configured-but-empty variable is named, so the diagnosis can point at it.
		_, src = ResolveServerToken(&ServerConfig{TokenEnv: customEn}, "", ProcessEnvView())
		if src.Kind != TokenSourceUnset || src.EnvVar != customEn {
			t.Fatalf("empty token_env src=%+v", src)
		}
	})

	t.Run("no default GOFER_TOKEN fallback", func(t *testing.T) {
		// GOFER_TOKEN is read only because server.token_env names it (gofer init
		// writes that line). A config without token_env ignores the variable — the
		// server would, so the report must too.
		t.Setenv(EnvTokenDefault, procTok)
		token, src := ResolveServerToken(&ServerConfig{}, "", ProcessEnvView())
		if token != "" || src.Kind != TokenSourceUnset {
			t.Fatalf("token=%q src=%+v; a config without token_env must not read GOFER_TOKEN", token, src)
		}
	})
}

// TestTokenSourceNeverLeaksTheValue: every rendering of a source is value-free.
func TestTokenSourceNeverLeaksTheValue(t *testing.T) {
	const secret = "s3cr3t-token-value-do-not-print"
	view := DotenvOnlyEnvView()
	view.values[EnvTokenDefault] = secret
	view.files[EnvTokenDefault] = "/etc/gofer/.env"
	_, src := ResolveServerToken(&ServerConfig{TokenEnv: EnvTokenDefault}, "", view)
	out := src.Summary()
	if strings.Contains(out, secret) {
		t.Fatalf("Summary() leaked the token: %q", out)
	}
	if !strings.Contains(out, TokenFingerprint(secret)) || !strings.Contains(out, EnvFileName) {
		t.Fatalf("Summary() = %q, want the fingerprint and the source", out)
	}
	if src.Length != len(secret) {
		t.Fatalf("length = %d, want %d", src.Length, len(secret))
	}
	if TokenFingerprint(secret) == TokenFingerprint(secret+"x") {
		t.Fatal("fingerprints of different tokens collided")
	}
	if len(TokenFingerprint(secret)) != 8 {
		t.Fatalf("fingerprint = %q, want 8 hex chars", TokenFingerprint(secret))
	}
	if TokenFingerprint("") != "" {
		t.Fatal("an empty token must have no fingerprint")
	}
}

// TestDescribeServerTokens covers EVERY credential the server authenticates with,
// not just the legacy one: callers, workers, and the entries that resolve to
// nothing (they cannot authenticate at all).
func TestDescribeServerTokens(t *testing.T) {
	const (
		callerTok = "caller-literal-token"
		workerTok = "worker-env-token"
		workerEnv = "GOFER_T9MD_WORKER_TOKEN"
	)
	view := DotenvOnlyEnvView()
	view.values[workerEnv] = workerTok
	view.files[workerEnv] = "/etc/gofer/.env"
	sc := &ServerConfig{
		Token: "legacy-literal",
		Callers: []CallerConfig{
			{ID: "ci", Token: callerTok},
			{ID: "ops", TokenEnv: "GOFER_T9MD_MISSING"},
		},
		Workers: map[string]WorkerAuthConfig{
			"w2": {TokenEnv: workerEnv},
			"w1": {Token: "worker-literal"},
		},
	}
	got := DescribeServerTokens(sc, "", view)
	wantRoles := []string{"server", "caller:ci", "caller:ops", "worker:w1", "worker:w2"}
	if len(got) != len(wantRoles) {
		t.Fatalf("got %d sources, want %d: %+v", len(got), len(wantRoles), got)
	}
	for i, role := range wantRoles {
		if got[i].Role != role {
			t.Fatalf("source %d role = %q, want %q (%+v)", i, got[i].Role, role, got)
		}
	}
	if got[0].Kind != TokenSourceConfig || got[0].Field != "server.token" {
		t.Errorf("legacy source = %+v", got[0])
	}
	if got[1].Kind != TokenSourceConfig || got[1].Field != "server.callers.ci.token" {
		t.Errorf("caller source = %+v", got[1])
	}
	if got[2].Kind != TokenSourceUnset || got[2].EnvVar != "GOFER_T9MD_MISSING" {
		t.Errorf("unresolvable caller = %+v", got[2])
	}
	if got[4].Kind != TokenSourceEnvFile || got[4].EnvFile != "/etc/gofer/.env" ||
		got[4].Fingerprint != TokenFingerprint(workerTok) {
		t.Errorf("worker env source = %+v", got[4])
	}
}

// TestDotenvFileValuesFollowsLoadDotenv keeps the attribution parser faithful to
// the one LoadDotenv uses (goutil's ParseEnvLines + upper-cased keys): a drift here
// would mis-report which side a token came from.
func TestDotenvFileValuesFollowsLoadDotenv(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, EnvFileName)
	writeFile(t, file, strings.Join([]string{
		"# comment",
		"; another comment",
		"// and another",
		"",
		"lower_case=plain",
		"  spaced  =  trimmed value  ",
		`quoted="with spaces"`,
		"single='one two'",
		"inline=value # trailing comment",
		"empty=",
		"not a key value line",
		"GOFER_TOKEN=abc123",
	}, "\n"))
	values, ok, err := dotenvFileValues(file)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	want := map[string]string{
		"LOWER_CASE":  "plain",
		"SPACED":      "trimmed value",
		"QUOTED":      "with spaces",
		"SINGLE":      "one two",
		"INLINE":      "value",
		"EMPTY":       "",
		"GOFER_TOKEN": "abc123",
	}
	for key, wantVal := range want {
		if got := values[key]; got != wantVal {
			t.Errorf("%s = %q, want %q", key, got, wantVal)
		}
	}
	if _, ok, err := dotenvFileValues(filepath.Join(dir, "absent.env")); ok || err != nil {
		t.Fatalf("missing file: ok=%v err=%v", ok, err)
	}
}

// unsetEnv removes key for the duration of the test. t.Setenv cannot express
// "absent", and absent vs empty IS the distinction LoadDotenv keys on.
func unsetEnv(t *testing.T, key string) {
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

// TestDotenvOnlyEnvViewLaterFileWins: LoadDotenv loads <config-dir>/.env then
// ./.env, so the later file overrides — the managed service is simulated with
// exactly those layers and NOTHING from this process's environment.
func TestDotenvOnlyEnvViewLaterFileWins(t *testing.T) {
	dir := t.TempDir()
	global, local := filepath.Join(dir, "global.env"), filepath.Join(dir, "local.env")
	writeFile(t, global, "GOFER_TOKEN=from-global\nONLY_GLOBAL=1\n")
	writeFile(t, local, "GOFER_TOKEN=from-local\n")
	t.Setenv(EnvTokenDefault, "from-process") // must be ignored by the service view

	view := DotenvOnlyEnvView(global, local)
	sc := &ServerConfig{TokenEnv: EnvTokenDefault}
	token, src := ResolveServerToken(sc, "", view)
	if token != "from-local" || src.EnvFile != local {
		t.Fatalf("token=%q src=%+v", token, src)
	}
	if v, ok := view.token("ONLY_GLOBAL"); !ok || v != "1" {
		t.Fatalf("an earlier layer's key was lost: %q %v", v, ok)
	}
	if _, ok := view.token("HOME"); ok {
		t.Fatal("the service view picked up a variable of this process")
	}
}

// TestProcessEnvViewInsideJobSkipsDotenvTokens: LoadDotenv refuses *_TOKEN keys
// from the files inside a job, so the report must not attribute them to a file.
func TestProcessEnvViewInsideJobSkipsDotenvTokens(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, EnvFileName)
	writeFile(t, file, "GOFER_TOKEN=shared\n")
	t.Setenv(EnvConfigDir, dir)
	t.Setenv(EnvTokenDefault, "shared")
	t.Setenv(EnvJobToken, "job-credential")

	view := ProcessEnvView()
	if got := view.files[EnvTokenDefault]; got != "" {
		t.Fatalf("inside a job %s was attributed to %q", EnvTokenDefault, got)
	}

	t.Setenv(EnvJobToken, "")
	view = ProcessEnvView()
	if got := view.files[EnvTokenDefault]; got != file {
		t.Fatalf("%s attributed to %q, want %q", EnvTokenDefault, got, file)
	}
}
