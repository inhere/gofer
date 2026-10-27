package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/servicemgr"
	"github.com/inhere/gofer/internal/util"
)

// Token provenance for a managed server (gofer-t9md).
//
// A managed serve is started by systemd / the Windows task scheduler with no
// access to the environment of the terminal that ran `serve register`: it reads
// the config dir's dotenv files, config.yaml, and — on Windows — the operator's
// PERSISTENT environment (the User / Machine variables the task scheduler hands
// it), which wins over .env. When the registering terminal exported GOFER_TOKEN
// while <config-dir>/.env (or the config literal) held another value, the server
// the operator reaches after `serve start` uses a different token than the CLI
// they were just using — and web login fails with a bare 401. These helpers
// compare the two sides by VALUE (never by the display mask) so `serve register`
// can warn and `serve status` can show the drift.

// managedPersistentEnv is the process-independent environment a managed service
// inherits (Windows User / Machine variables; nothing elsewhere). A variable so
// let replaces the .env value, so it is part of the service's view. Injectable:
// a test pins both sides without touching a real registry.
var managedPersistentEnv = config.PersistentEnv

// managedDotenvPaths lists the dotenv files the managed service reads, in
// LoadDotenv order (later wins): its config dir's, then its working directory's.
func managedDotenvPaths(spec servicemgr.Spec) []string {
	configDir, err := filepath.Abs(spec.ConfigDir)
	if err != nil {
		configDir = filepath.Clean(spec.ConfigDir)
	}
	paths := []string{filepath.Join(configDir, config.EnvFileName)}
	if spec.WorkDir != "" {
		workDir, absErr := filepath.Abs(spec.WorkDir)
		if absErr != nil {
			workDir = filepath.Clean(spec.WorkDir)
		}
		if workDir != configDir {
			paths = append(paths, filepath.Join(workDir, config.EnvFileName))
		}
	}
	return paths
}

// managedTokenView is the environment a managed serve starts with: its dotenv
// files with the inherited persistent environment on top — never anything from
// the registering terminal.
func managedTokenView(spec servicemgr.Spec) config.EnvView {
	return config.ServiceEnvView(managedPersistentEnv(), managedDotenvPaths(spec)...)
}

// unreadDotenvFiles lists, as absolute paths, the dotenv files THIS process
// loaded that the service will not read: it runs in its own working directory
// (the config dir), so a project-local ./.env of the registering terminal is
// invisible to it — one more reason two tokens can differ.
func unreadDotenvFiles(spec servicemgr.Spec) []string {
	return unreadDotenvFilesWith(config.LoadedDotenvFiles(), spec)
}

// unreadDotenvFilesWith is unreadDotenvFiles over an explicit file list, so a test
// can pin what LoadDotenv read.
func unreadDotenvFilesWith(loaded []string, spec servicemgr.Spec) []string {
	servicePaths := managedDotenvPaths(spec)
	cwd, _ := os.Getwd()
	seen := make(map[string]bool, 2)
	out := make([]string, 0, len(loaded))
	for _, file := range loaded {
		abs := file
		if !filepath.IsAbs(abs) && cwd != "" {
			abs = filepath.Join(cwd, abs)
		}
		abs = filepath.Clean(abs)
		if seen[abs] || slices.Contains(servicePaths, abs) {
			continue
		}
		seen[abs] = true
		out = append(out, abs)
	}
	return out
}

// tokenComparison is the two sides of the token question for a managed serve.
type tokenComparison struct {
	Current config.TokenSource
	Service config.TokenSource
	Match   bool
	// Persistent is the inherited persistent-environment variable that shadows
	// the service's .env/config token, when there is one AND it changes the value.
	Persistent *persistentShadow
	// UnreadDotenv lists dotenv files this process loaded that the service does
	// not (see unreadDotenvFiles).
	UnreadDotenv []string
}

// persistentShadow is a persistent-environment variable that overrides the token
// the .env / config alone would give the service.
type persistentShadow struct {
	// Var carries the scope and name (its Value is never serialized).
	Var config.PersistentEnvVar `json:"var"`
	// Masked is the inherited value's mask.
	Masked string `json:"masked"`
	// Shadowed is what the .env / config would resolve to instead.
	Shadowed config.TokenSource `json:"shadowed"`
}

// compareManagedToken compares the token THIS process would use with the one the
// managed service will use. current is passed in rather than read from
// os.Environ, so a test can pin both sides without registering anything.
func compareManagedToken(cfg *config.Config, spec servicemgr.Spec, current config.EnvView) tokenComparison {
	server := &config.ServerConfig{}
	if cfg != nil {
		server = &cfg.Server
	}
	persistent := managedPersistentEnv()
	curToken, cur := config.ResolveServerToken(server, "", current)
	svcToken, svc := config.ResolveServerToken(server, "", managedTokenView(spec))
	cmp := tokenComparison{Current: cur, Service: svc, Match: config.TokenValuesMatch(curToken, svcToken),
		UnreadDotenv: unreadDotenvFiles(spec)}
	// A persistent variable the service inherits beats the .env value. Report it
	// whenever it CHANGES the token: that is the case the operator cannot see by
	// editing the file.
	if name := managedTokenEnvName(cfg); name != "" {
		if inherited, ok := config.FindPersistentEnv(persistent, name); ok {
			fileToken, fileSrc := config.ResolveServerToken(server, "", config.DotenvOnlyEnvView(managedDotenvPaths(spec)...))
			if !config.TokenValuesMatch(inherited.Value, fileToken) {
				cmp.Persistent = &persistentShadow{Var: inherited, Masked: config.TokenMask(inherited.Value), Shadowed: fileSrc}
			}
		}
	}
	return cmp
}

// managedTokenEnvName is the variable a token is expected in for this config
// (server.token_env, else the name `gofer init` writes).
func managedTokenEnvName(cfg *config.Config) string {
	if cfg != nil && cfg.Server.TokenEnv != "" {
		return cfg.Server.TokenEnv
	}
	return config.EnvTokenDefault
}

// tokenSourceText renders a config.TokenSource for people — where the token comes
// from and its mask, never the value.
func tokenSourceText(src config.TokenSource) string {
	switch src.Kind {
	case config.TokenSourceConfig:
		return cliText("cli.daemon.serve.token_from_config", "field", src.Field, "note", tokenNote(src))
	case config.TokenSourceEnvFile:
		return cliText("cli.daemon.serve.token_from_env_file", "name", src.EnvVar,
			"file", src.EnvFile, "note", tokenNote(src))
	case config.TokenSourceProcess:
		return cliText("cli.daemon.serve.token_from_process_env", "name", src.EnvVar, "note", tokenNote(src))
	case config.TokenSourcePersistentEnv:
		return cliText("cli.daemon.serve.token_from_persistent_env", "name", src.EnvVar,
			"scope", persistentScopeLabel(src.Scope), "note", tokenNote(src))
	case config.TokenSourceFlag:
		return cliText("cli.daemon.serve.token_from_flag", "note", tokenNote(src))
	default:
		if src.EnvVar != "" {
			return cliText("cli.daemon.serve.token_env_empty", "name", src.EnvVar)
		}
		return cliText("cli.daemon.serve.token_unset")
	}
}

// tokenNote is the value-free identity of a token: its length and the first two
// and last characters, e.g. "len=32 ab**c" — enough to tell whether two sides hold
// the same one, useless as a credential.
func tokenNote(src config.TokenSource) string {
	return cliText("cli.daemon.serve.token_note", "mask", src.Masked)
}

// persistentScopeLabel localizes a persistent-environment scope ("user" /
// "machine").
func persistentScopeLabel(scope string) string {
	if scope == config.PersistentEnvScopeMachine {
		return cliText("cli.daemon.serve.token_scope_machine")
	}
	return cliText("cli.daemon.serve.token_scope_user")
}

// powershellScope is the Scope argument of [Environment]::SetEnvironmentVariable.
func powershellScope(scope string) string {
	if scope == config.PersistentEnvScopeMachine {
		return "Machine"
	}
	return "User"
}

// persistentEnvClearCommand is the PowerShell command that REMOVES a persistent
// variable (the recommended fix: the .env becomes the one source).
func persistentEnvClearCommand(v config.PersistentEnvVar) string {
	return fmt.Sprintf("[Environment]::SetEnvironmentVariable('%s',$null,'%s')", v.Name, powershellScope(v.Scope))
}

// persistentEnvSetCommand is the PowerShell command that sets the persistent
// variable to a token the operator chooses. The value is a placeholder: a command
// printed by gofer never carries the secret.
func persistentEnvSetCommand(v config.PersistentEnvVar) string {
	return fmt.Sprintf("[Environment]::SetEnvironmentVariable('%s','<token>','%s')", v.Name, powershellScope(v.Scope))
}

// tokenDiffLines names every reason the two sides can differ besides "this
// terminal exported it": an inherited Windows persistent variable that shadows
// the .env, and dotenv files this process loaded that the service never reads.
func tokenDiffLines(shadow *persistentShadow, unread []string) []string {
	var lines []string
	if shadow != nil {
		lines = append(lines,
			cliText("cli.daemon.serve.token_persistent_head", "name", shadow.Var.Name, "scope", persistentScopeLabel(shadow.Var.Scope)),
			"  "+cliText("cli.daemon.serve.token_persistent_inherited", "mask", shadow.Masked),
			"  "+cliText("cli.daemon.serve.token_persistent_file", "source", tokenSourceText(shadow.Shadowed)),
			"  "+cliText("cli.daemon.serve.token_persistent_why"),
			"  "+cliText("cli.daemon.serve.token_persistent_clear", "cmd", persistentEnvClearCommand(shadow.Var)),
			"  "+cliText("cli.daemon.serve.token_persistent_set", "cmd", persistentEnvSetCommand(shadow.Var)),
			"  "+cliText("cli.daemon.serve.token_persistent_restart"),
		)
	}
	for _, file := range unread {
		lines = append(lines, cliText("cli.daemon.serve.token_unread_dotenv", "file", file))
	}
	return lines
}

// tokenWarningLines is what `serve register` prints when the terminal and the
// service would use different tokens (including "the service has none"). It is
// empty when the two sides agree and nothing shadows the .env: registering does
// not change the token then. The warning never blocks the registration.
func tokenWarningLines(cmp tokenComparison, cfg *config.Config, spec servicemgr.Spec) []string {
	if cmp.Match {
		// The two sides agree, so the login will work — but a persistent variable can
		// still make editing the .env pointless, and that is worth saying.
		return tokenDiffLines(cmp.Persistent, nil)
	}
	lines := []string{
		cliText("cli.daemon.serve.token_warn_head"),
		"  " + cliText("cli.daemon.serve.token_warn_current", "current", tokenSourceText(cmp.Current)),
		"  " + cliText("cli.daemon.serve.token_warn_service", "service", tokenSourceText(cmp.Service)),
		"  " + cliText("cli.daemon.serve.token_warn_why"),
	}
	lines = append(lines, tokenDiffLines(cmp.Persistent, cmp.UnreadDotenv)...)
	// "Put the token in the .env" is the fix — unless a persistent variable shadows
	// that file, in which case the block above has the real fix (remove / change it).
	if cmp.Persistent == nil {
		lines = append(lines, "  "+cliText("cli.daemon.serve.token_warn_fix", "name", managedTokenEnvName(cfg),
			"file", filepath.Join(spec.ConfigDir, config.EnvFileName)))
	}
	return append(lines, "  "+cliText("cli.daemon.serve.token_warn_check"))
}

// tokenStatusLines renders the `serve status` token rows: both sides and, when
// they differ, the same pointers the register warning gives. Credentials the
// service cannot resolve (a caller / worker whose variable is empty there) are
// named too — such an entry cannot authenticate at all.
func tokenStatusLines(st *serveAuthTokenStatus) []string {
	lines := []string{
		cliText("cli.daemon.serve.token_status_service", "service", tokenSourceText(st.Service)),
		cliText("cli.daemon.serve.token_status_current", "current", tokenSourceText(st.CurrentProcess)),
	}
	if !st.Match {
		lines = append(lines, cliText("cli.daemon.serve.token_status_mismatch"))
	}
	lines = append(lines, tokenDiffLines(st.Persistent, st.UnreadDotenv)...)
	for _, cred := range st.Credentials {
		if cred.Set || cred.Role == config.RoleServerToken {
			continue
		}
		lines = append(lines, cliText("cli.daemon.serve.token_status_credential_unset",
			"role", cred.Role, "name", cred.EnvVar))
	}
	return lines
}

// managedAuthTokenStatus is the `serve status --json` auth_token block. nil when
// the managed config cannot be read (the status path reports that error itself).
func managedAuthTokenStatus(spec servicemgr.Spec) *serveAuthTokenStatus {
	cfg, _, err := config.Load(spec.ConfigFile)
	if err != nil {
		return nil
	}
	cmp := compareManagedToken(cfg, spec, config.ProcessEnvView())
	return &serveAuthTokenStatus{Service: cmp.Service, CurrentProcess: cmp.Current, Match: cmp.Match,
		Credentials:  config.DescribeServerTokens(&cfg.Server, "", managedTokenView(spec)),
		Persistent:   cmp.Persistent,
		UnreadDotenv: cmp.UnreadDotenv}
}

// cliStatsToken is the token `serve status` sends to /v1/stats and where it came
// from: the precedence every other CLI call uses (resolveClientToken), so the
// status report explains the same 401 the operator's `gofer job ls` gets.
func cliStatsToken(sc *config.ServerConfig) (string, config.TokenSource) {
	if sc == nil {
		sc = &config.ServerConfig{}
	}
	view := config.ProcessEnvView()
	for _, envVar := range []string{job.EnvJobToken, config.EnvServerToken} {
		if v := os.Getenv(envVar); v != "" {
			return v, withRole(config.EnvTokenSource("cli", envVar, view))
		}
	}
	token, src := config.ResolveServerToken(&config.ServerConfig{Token: sc.Token, TokenEnv: sc.TokenEnv}, "", view)
	return token, withRole(src)
}

// withRole relabels a source for the CLI side of a comparison.
func withRole(src config.TokenSource) config.TokenSource {
	src.Role = "cli"
	return src
}

// versionAuthHintLines explains a 401 from the version probe: the token this CLI
// sent is not one the service accepts, and both sides are shown by mask only.
func versionAuthHintLines(m *versionAuthMismatch) []string {
	if m == nil {
		return nil
	}
	return []string{
		cliText("cli.daemon.serve.version_unauthorized_head"),
		"  " + cliText("cli.daemon.serve.version_unauthorized_cli", "source", tokenSourceText(m.CLI)),
		"  " + cliText("cli.daemon.serve.version_unauthorized_server", "source", tokenSourceText(m.Server)),
		"  " + cliText("cli.daemon.serve.version_unauthorized_fix", "name", config.EnvServerToken),
	}
}

// statusFileLines reports which config files and which dotenv files this run
// loaded, so "the service reads another file" is visible without -v.
func statusFileLines(st *serveStatusOutput) []string {
	files := st.ConfigFiles
	if len(files) == 0 && st.ConfigFile != "" {
		files = []string{st.ConfigFile}
	}
	lines := make([]string, 0, util.CapSum(len(files), 1))
	if len(files) == 0 {
		lines = append(lines, cliText("cli.daemon.serve.status_config_none"))
	}
	for _, file := range files {
		lines = append(lines, cliText("cli.daemon.serve.status_config_file", "file", file))
	}
	if len(st.DotenvFiles) > 0 {
		lines = append(lines, cliText("cli.daemon.serve.status_dotenv_files", "files", strings.Join(st.DotenvFiles, ", ")))
	} else {
		lines = append(lines, cliText("cli.daemon.serve.status_dotenv_none"))
	}
	return lines
}

// statusConfigFiles lists the config files a status run read: the managed spec's
// (what the service runs) first, then this CLI's own -c when it is another file.
func statusConfigFiles(spec servicemgr.Spec) []string {
	files := []string{spec.ConfigFile}
	if cli := config.InputCfgFile; cli != "" {
		if abs, err := filepath.Abs(cli); err == nil {
			cli = abs
		}
		if cli = filepath.Clean(cli); !slices.Contains(files, cli) {
			files = append(files, cli)
		}
	}
	return files
}

// loadedDotenvFilesAbs is what config.LoadDotenv read in this process, as
// absolute paths.
func loadedDotenvFilesAbs() []string {
	files := config.LoadedDotenvFiles()
	if len(files) == 0 {
		return nil
	}
	cwd, _ := os.Getwd()
	out := make([]string, 0, len(files))
	for _, file := range files {
		if !filepath.IsAbs(file) && cwd != "" {
			file = filepath.Join(cwd, file)
		}
		out = append(out, filepath.Clean(file))
	}
	return out
}
