package commands

import (
	"path/filepath"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/servicemgr"
)

// Token provenance for a managed server (gofer-t9md).
//
// A managed serve is started by systemd / the Windows task scheduler with no
// access to the environment of the terminal that ran `serve register`: it reads
// only the config dir's dotenv files and config.yaml. When the registering
// terminal exported GOFER_TOKEN while <config-dir>/.env (or the config literal)
// held another value, the server the operator reaches after `serve start` uses a
// different token than the CLI they were just using — and web login fails with a
// bare 401. These helpers compare the two sides by SOURCE + FINGERPRINT (never
// the value) so `serve register` can warn and `serve status` can show the drift.

// managedTokenView is the environment a managed serve starts with: the dotenv
// files of its config dir and (when that differs) its working directory, in
// LoadDotenv order — later files win. Nothing from the registering terminal.
func managedTokenView(spec servicemgr.Spec) config.EnvView {
	paths := []string{filepath.Join(spec.ConfigDir, config.EnvFileName)}
	if spec.WorkDir != "" && filepath.Clean(spec.WorkDir) != filepath.Clean(spec.ConfigDir) {
		paths = append(paths, filepath.Join(spec.WorkDir, config.EnvFileName))
	}
	return config.DotenvOnlyEnvView(paths...)
}

// tokenComparison is the two sides of the token question for a managed serve.
type tokenComparison struct {
	Current config.TokenSource
	Service config.TokenSource
	Match   bool
}

// compareManagedToken compares the token THIS process would use with the one the
// managed service will use. current is passed in rather than read from
// os.Environ, so a test can pin both sides without registering anything.
func compareManagedToken(cfg *config.Config, spec servicemgr.Spec, current config.EnvView) tokenComparison {
	server := &config.ServerConfig{}
	if cfg != nil {
		server = &cfg.Server
	}
	_, cur := config.ResolveServerToken(server, "", current)
	_, svc := config.ResolveServerToken(server, "", managedTokenView(spec))
	return tokenComparison{Current: cur, Service: svc, Match: config.TokenSourceMatches(cur, svc)}
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
// from and its fingerprint, never the value.
func tokenSourceText(src config.TokenSource) string {
	switch src.Kind {
	case config.TokenSourceConfig:
		return cliText("cli.daemon.serve.token_from_config", "field", src.Field, "note", tokenNote(src))
	case config.TokenSourceEnvFile:
		return cliText("cli.daemon.serve.token_from_env_file", "name", src.EnvVar,
			"file", src.EnvFile, "note", tokenNote(src))
	case config.TokenSourceProcess:
		return cliText("cli.daemon.serve.token_from_process_env", "name", src.EnvVar, "note", tokenNote(src))
	case config.TokenSourceFlag:
		return cliText("cli.daemon.serve.token_from_flag", "note", tokenNote(src))
	default:
		if src.EnvVar != "" {
			return cliText("cli.daemon.serve.token_env_empty", "name", src.EnvVar)
		}
		return cliText("cli.daemon.serve.token_unset")
	}
}

// tokenNote is the value-free identity of a token: its length and the first 8 hex
// chars of its sha256 — enough to tell whether two sides hold the same one.
func tokenNote(src config.TokenSource) string {
	return cliText("cli.daemon.serve.token_note", "length", src.Length, "fingerprint", src.Fingerprint)
}

// tokenWarningLines is what `serve register` prints when the terminal and the
// service would use different tokens (including "the service has none"). It is
// empty when the two sides agree: registering does not change the token then. The
// warning never blocks the registration.
func tokenWarningLines(cmp tokenComparison, cfg *config.Config, spec servicemgr.Spec) []string {
	if cmp.Match {
		return nil
	}
	return []string{
		cliText("cli.daemon.serve.token_warn_head"),
		"  " + cliText("cli.daemon.serve.token_warn_current", "current", tokenSourceText(cmp.Current)),
		"  " + cliText("cli.daemon.serve.token_warn_service", "service", tokenSourceText(cmp.Service)),
		"  " + cliText("cli.daemon.serve.token_warn_why"),
		"  " + cliText("cli.daemon.serve.token_warn_fix", "name", managedTokenEnvName(cfg),
			"file", filepath.Join(spec.ConfigDir, config.EnvFileName)),
		"  " + cliText("cli.daemon.serve.token_warn_check"),
	}
}

// tokenStatusLines renders the `serve status` token rows: both sides and, when
// they differ, the same pointer the register warning gives. Credentials the
// service cannot resolve (a caller / worker whose variable is empty there) are
// named too — such an entry cannot authenticate at all.
func tokenStatusLines(st *serveAuthTokenStatus) []string {
	cmp := tokenComparison{Current: st.CurrentProcess, Service: st.Service, Match: st.Match}
	lines := []string{
		cliText("cli.daemon.serve.token_status_service", "service", tokenSourceText(cmp.Service)),
		cliText("cli.daemon.serve.token_status_current", "current", tokenSourceText(cmp.Current)),
	}
	if !cmp.Match {
		lines = append(lines, cliText("cli.daemon.serve.token_status_mismatch"))
	}
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
		Credentials: config.DescribeServerTokens(&cfg.Server, "", managedTokenView(spec))}
}
