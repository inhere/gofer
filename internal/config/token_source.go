package config

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/inhere/gofer/internal/util"
)

// Where a bearer token COMES FROM — never the token itself (gofer-t9md).
//
// A serve started by hand inherits the terminal's environment; the same serve
// registered as a native service (systemd unit / Windows task) starts from the
// config dir's dotenv files and config.yaml ONLY. When the operator exported
// GOFER_TOKEN=A in that terminal while <config-dir>/.env (or the config literal)
// held B, the web login that worked with A fails the moment the server runs as a
// service — and nothing said so. Everything that reports the token (the
// `serve register` warning, `serve status`, the startup log) goes through the
// functions below: two sides are compared by their full VALUE (TokenValuesMatch)
// and shown by SOURCE plus a masked form (TokenMask) that is enough to tell them
// apart without handing out anything usable.
//
// On Windows a managed task also inherits the operator's PERSISTENT environment
// (the User / Machine variables), which LoadDotenv's "an exported value wins"
// rule lets override <config-dir>/.env — the stale GOFER_TOKEN case. ServiceEnvView
// layers that store under the dotenv files exactly as the service does.
//
// The resolution mirrors the server exactly: serve.Start uses ResolveServerToken,
// and httpapi builds its caller set from the same precedence (flag > token_env >
// token literal, first caller match wins in buildCallers).

// EnvTokenDefault is the variable name `gofer init` and config/gofer.example.yaml
// write into server.token_env. It is NOT a built-in fallback: the server reads
// only the variable server.token_env names, so a deployment whose config lacks
// token_env does not read GOFER_TOKEN at all.
const EnvTokenDefault = "GOFER_TOKEN"

// RoleServerToken labels the legacy single bearer token in a TokenSource. The
// multi-caller entries are labelled "caller:<id>" / "worker:<id>".
const RoleServerToken = "server"

// TokenSourceKind classifies where an effective token came from.
type TokenSourceKind string

const (
	// TokenSourceUnset means no token resolved at all (auth is off, or the named
	// environment variable is empty).
	TokenSourceUnset TokenSourceKind = "unset"
	// TokenSourceConfig means the literal in the config file (server.token,
	// server.callers[].token, server.workers.<id>.token).
	TokenSourceConfig TokenSourceKind = "config"
	// TokenSourceEnvFile means the environment variable server.token_env named,
	// whose value came from a dotenv file.
	TokenSourceEnvFile TokenSourceKind = "env-file"
	// TokenSourceProcess means that variable, whose value was exported by the
	// process (exported beats dotenv, see LoadDotenv) — the case a service loses.
	TokenSourceProcess TokenSourceKind = "process-env"
	// TokenSourceFlag means the `serve --token` command line override.
	TokenSourceFlag TokenSourceKind = "flag"
	// TokenSourcePersistentEnv means that variable, whose value the service
	// inherits from a persistent environment store (Windows User / Machine) — it
	// shadows the dotenv files (gofer-t9md).
	TokenSourcePersistentEnv TokenSourceKind = "persistent-env"
)

// Persistent environment scopes (Windows): the two stores a managed task hands
// to the service it starts, and the PowerShell scope names used to fix them.
const (
	PersistentEnvScopeUser    = "user"
	PersistentEnvScopeMachine = "machine"
)

// PersistentEnvVar is one variable a service inherits from a process-independent
// environment store instead of from the terminal that registered it.
type PersistentEnvVar struct {
	// Scope is PersistentEnvScopeUser or PersistentEnvScopeMachine.
	Scope string `json:"scope"`
	// Name is the variable's name as the store spells it.
	Name string `json:"name"`
	// Value is the inherited value. It is only ever shown masked, so it is never
	// serialized either (a --json report must not carry a credential).
	Value string `json:"-"`
}

// TokenSource is where one bearer token came from, with a value-free identity:
// Length and Masked are enough for two machines (or two starts of the same
// machine) to tell whether they hold the same token, without ever printing it.
type TokenSource struct {
	// Role is "server" for the legacy token, "caller:<id>" / "worker:<id>" otherwise.
	Role string `json:"role"`
	// Kind is the source class.
	Kind TokenSourceKind `json:"kind"`
	// Field is the config key the literal came from (Kind == config).
	Field string `json:"field,omitempty"`
	// EnvVar is the variable name the token was read from (env kinds, and an
	// unset token whose configured variable is empty).
	EnvVar string `json:"env_var,omitempty"`
	// EnvFile is the dotenv file that supplied the value (Kind == env-file).
	EnvFile string `json:"env_file,omitempty"`
	// Scope is the persistent environment store the value came from
	// (Kind == persistent-env): "user" or "machine".
	Scope string `json:"scope,omitempty"`
	// Set reports whether a token resolved.
	Set bool `json:"set"`
	// Length is the token's length; 0 when unset.
	Length int `json:"length,omitempty"`
	// Masked is the displayable form of the token (see TokenMask), e.g.
	// "len=32 ab**c"; "" when unset. It is NEVER the token and must not be used
	// to decide whether two sides hold the same one (two tokens can share a mask).
	Masked string `json:"masked,omitempty"`
}

// Summary renders the source (and the value-free identity) as one English line,
// for logs. CLI text is rendered from the same struct by the commands layer, so
// the wording can be localized there.
func (s TokenSource) Summary() string {
	var b strings.Builder
	switch s.Kind {
	case TokenSourceConfig:
		fmt.Fprintf(&b, "%s in the config file", s.Field)
	case TokenSourceEnvFile:
		fmt.Fprintf(&b, "environment variable %s from %s", s.EnvVar, s.EnvFile)
	case TokenSourceProcess:
		fmt.Fprintf(&b, "environment variable %s from the process environment", s.EnvVar)
	case TokenSourcePersistentEnv:
		fmt.Fprintf(&b, "environment variable %s from the %s persistent environment", s.EnvVar, s.Scope)
	case TokenSourceFlag:
		b.WriteString("--token on the command line")
	default:
		if s.EnvVar != "" {
			fmt.Fprintf(&b, "environment variable %s (not set)", s.EnvVar)
		} else {
			b.WriteString("not set")
		}
	}
	if s.Set {
		fmt.Fprintf(&b, " (%s)", s.Masked)
	}
	return b.String()
}

// tokenMaskMinLen is the shortest token that shows any of itself: two leading and
// one trailing character of a shorter secret is most of it.
const tokenMaskMinLen = 8

// TokenMask is the value-free identity to SHOW for a token: its length plus the
// first two and the last character, e.g. "len=32 ab**c". Two sides can see at a
// glance that they differ without either handing out anything usable. A token
// shorter than tokenMaskMinLen characters shows only its length ("len=5"). "" for
// an empty token.
func TokenMask(token string) string {
	if token == "" {
		return ""
	}
	runes := []rune(token)
	if len(runes) < tokenMaskMinLen {
		return fmt.Sprintf("len=%d", len(runes))
	}
	return fmt.Sprintf("len=%d %s**%s", len(runes), string(runes[:2]), string(runes[len(runes)-1:]))
}

// TokenValuesMatch reports whether two sides resolved the SAME token, compared by
// the full value in constant time — never by the display mask, which two different
// tokens can share. Two unset (empty) sides match.
func TokenValuesMatch(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// EnvView is the environment a process resolves its tokens against, plus the
// dotenv file each value came from (so a value is attributed to a file rather
// than to the process environment).
type EnvView struct {
	values map[string]string
	files  map[string]string
	scopes map[string]string
}

func newEnvView() EnvView {
	return EnvView{values: map[string]string{}, files: map[string]string{}, scopes: map[string]string{}}
}

// token returns the value of envVar, treating an empty value as absent — the
// server's own reading of "no token here" (serve resolveToken / buildCallers).
// A persistent-environment name is looked up case-insensitively as Windows does.
func (v EnvView) token(envVar string) (string, bool) {
	if val, ok := v.values[envVar]; ok {
		return val, val != ""
	}
	if val, ok := v.values[strings.ToUpper(envVar)]; ok {
		return val, val != ""
	}
	return "", false
}

// file is the dotenv file envVar came from, if any.
func (v EnvView) file(envVar string) string {
	if file := v.files[envVar]; file != "" {
		return file
	}
	return v.files[strings.ToUpper(envVar)]
}

// scope is the persistent environment store envVar came from, if any.
func (v EnvView) scope(envVar string) string {
	if scope := v.scopes[envVar]; scope != "" {
		return scope
	}
	return v.scopes[strings.ToUpper(envVar)]
}

// ProcessEnvView is THIS process's environment as the server would see it:
// os.Environ plus, for every variable a dotenv file defines with the same value,
// that file. LoadDotenv loads <config-dir>/.env then ./.env before anything reads
// the environment, and an exported value wins over both, so a variable whose
// value differs from the file's was exported.
func ProcessEnvView() EnvView {
	view := newEnvView()
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i > 0 {
			view.values[kv[:i]] = kv[i+1:]
		}
	}
	// Inside a job LoadDotenv skips every *_TOKEN key of the files it finds (the
	// files are the SERVER's), so a job's credential never comes from one.
	insideJob := view.values[EnvJobToken] != ""
	for _, path := range dotenvPaths() {
		values, ok, err := dotenvFileValues(path)
		if err != nil || !ok {
			continue // a missing or unreadable file contributes nothing
		}
		for key, val := range values {
			if insideJob && strings.HasSuffix(key, envTokenSuffix) {
				continue
			}
			if cur, seen := view.values[key]; seen && cur == val {
				view.files[key] = path
			}
		}
	}
	return view
}

// DotenvOnlyEnvView is the environment a MANAGED service starts with: nothing
// from the terminal that registered it — only the given dotenv files, later
// files overriding earlier ones, exactly like LoadDotenv.
func DotenvOnlyEnvView(paths ...string) EnvView {
	view := newEnvView()
	for _, path := range paths {
		values, ok, err := dotenvFileValues(path)
		if err != nil || !ok {
			continue
		}
		for key, val := range values {
			view.values[key] = val
			view.files[key] = path
		}
	}
	return view
}

// PersistentEnv returns the process-independent environment a managed service
// inherits on this platform: on Windows the User and Machine persistent
// variables its task scheduler applies, nowhere else.
func PersistentEnv() []PersistentEnvVar {
	return platformPersistentEnv()
}

// FindPersistentEnv picks the value a service would get for name out of the
// persistent stores: the User one wins over the Machine one (that is the order
// Windows applies), the name matches case-insensitively, and an empty value
// counts as unset.
func FindPersistentEnv(vars []PersistentEnvVar, name string) (PersistentEnvVar, bool) {
	best := PersistentEnvVar{}
	found := false
	for _, v := range vars {
		if v.Name == "" || v.Value == "" || !strings.EqualFold(v.Name, name) {
			continue
		}
		if found && !(v.Scope == PersistentEnvScopeUser && best.Scope != PersistentEnvScopeUser) {
			continue
		}
		best, found = v, true
	}
	return best, found
}

// ServiceEnvView is the environment a MANAGED service starts with: the dotenv
// files of its config dir (and working directory) with the inherited PERSISTENT
// environment layered OVER them — a value the service inherits beats the file,
// which is LoadDotenv's rule and the reason a stale Windows GOFER_TOKEN shadows
// <config-dir>/.env. Nothing from the terminal that registered the service.
func ServiceEnvView(persistent []PersistentEnvVar, paths ...string) EnvView {
	view := DotenvOnlyEnvView(paths...)
	for _, v := range persistent {
		if v.Name == "" || v.Value == "" {
			continue
		}
		key := strings.ToUpper(v.Name)
		if view.scopes[key] == PersistentEnvScopeUser {
			continue // a User value already won over this Machine one
		}
		view.values[key] = v.Value
		view.values[v.Name] = v.Value
		delete(view.files, key)
		delete(view.files, v.Name)
		view.scopes[key] = v.Scope
		view.scopes[v.Name] = v.Scope
	}
	return view
}

// dotenvPaths lists the files LoadDotenv loads, in ITS order (later wins):
// <config-dir>/.env, then ./.env of the current working directory.
func dotenvPaths() []string {
	var out []string
	if dir, err := ConfigDir(); err == nil && dir != "" {
		out = append(out, filepath.Join(dir, EnvFileName))
	}
	return append(out, EnvFileName)
}

// ConfigDirDotenvPath is the global dotenv file (<config-dir>/.env) — where a
// managed service (and a `serve` started in that directory) reads a token from.
func ConfigDirDotenvPath() string {
	dir, err := ConfigDir()
	if err != nil || dir == "" {
		return EnvFileName
	}
	return filepath.Join(dir, EnvFileName)
}

// dotenvFileValues parses one .env file the way LoadDotenv's parser does —
// goutil's ParseEnvLines with SkipOnErrorLine: true, keys upper-cased — WITHOUT
// touching the process environment (LoadDotenv cannot be reused for this: it
// skips keys the OS env already defines, which after startup is every key).
// ok is false when the file does not exist.
func dotenvFileValues(path string) (map[string]string, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if key, val, isValid := splitDotenvLine(line); isValid {
			values[key] = val
		}
	}
	return values, true, nil
}

// splitDotenvLine mirrors goutil's KEY=VAL rules: the line is trimmed, comment
// lines (#, ;, //) are skipped, the first '=' splits, a fully quoted value is
// unquoted, and " #" starts an inline comment.
func splitDotenvLine(line string) (string, string, bool) {
	line = strings.TrimSpace(line)
	if line == "" || line[0] == '#' || line[0] == ';' || strings.HasPrefix(line, "//") {
		return "", "", false
	}
	key, val, found := strings.Cut(line, "=")
	if !found {
		return "", "", false
	}
	key = strings.ToUpper(strings.TrimSpace(key))
	if key == "" {
		return "", "", false
	}
	return key, dotenvValue(val), true
}

func dotenvValue(val string) string {
	val = strings.TrimSpace(val)
	if len(val) < 2 {
		return val
	}
	if quoted(val) {
		return val[1 : len(val)-1]
	}
	if pos := strings.Index(val, " #"); pos > 0 {
		val = strings.TrimRight(val[:pos], " \t")
		if len(val) >= 2 && quoted(val) {
			return val[1 : len(val)-1]
		}
	}
	return val
}

func quoted(val string) bool {
	pair := (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'')
	return pair
}

// ResolveServerToken returns the effective server bearer token and where it came
// from, with the server's precedence: `--token` > server.token_env (the variable
// it names, when its value is non-empty) > server.token. This is the ONE
// implementation — serve.Start enforces the value it returns, so the enforced
// token and the reported source cannot disagree.
func ResolveServerToken(sc *ServerConfig, flagToken string, view EnvView) (string, TokenSource) {
	if sc == nil {
		sc = &ServerConfig{}
	}
	if flagToken != "" {
		return flagToken, TokenSource{Role: RoleServerToken, Kind: TokenSourceFlag, Set: true,
			Length: len(flagToken), Masked: TokenMask(flagToken)}
	}
	if sc.TokenEnv != "" {
		if val, ok := view.token(sc.TokenEnv); ok {
			return val, view.envTokenSource(RoleServerToken, sc.TokenEnv)
		}
	}
	if sc.Token != "" {
		return sc.Token, configTokenSource(RoleServerToken, "server.token", sc.Token)
	}
	unset := TokenSource{Role: RoleServerToken, Kind: TokenSourceUnset}
	if sc.TokenEnv != "" {
		unset.EnvVar = sc.TokenEnv // named but empty: the diagnosis needs the name
	}
	return "", unset
}

// DescribeServerTokens reports every credential a server authenticates with, in
// the order buildCallers resolves them: the legacy token (caller id "default"),
// then server.callers, then server.workers. An entry with no resolvable token is
// reported too (as unset) — it cannot authenticate at all.
func DescribeServerTokens(sc *ServerConfig, flagToken string, view EnvView) []TokenSource {
	if sc == nil {
		return nil
	}
	_, legacy := ResolveServerToken(sc, flagToken, view)
	out := make([]TokenSource, 0, util.CapSum(1, len(sc.Callers), len(sc.Workers)))
	out = append(out, legacy)
	for _, cc := range sc.Callers {
		out = append(out, credentialTokenSource("caller:"+cc.ID, "server.callers."+cc.ID+".token",
			cc.Token, cc.TokenEnv, view))
	}
	workerIDs := make([]string, 0, len(sc.Workers))
	for id := range sc.Workers {
		workerIDs = append(workerIDs, id)
	}
	slices.Sort(workerIDs) // map order is random; status output must not be
	for _, id := range workerIDs {
		wc := sc.Workers[id]
		out = append(out, credentialTokenSource("worker:"+id, "server.workers."+id+".token",
			wc.Token, wc.TokenEnv, view))
	}
	return out
}

// EnvTokenSource reports where a token a caller already HOLDS (in envVar) comes
// from — a dotenv file, a persistent environment store, or the process — or that
// the variable is empty there. Used to describe the CLI side of a comparison.
func EnvTokenSource(role, envVar string, view EnvView) TokenSource {
	if _, ok := view.token(envVar); ok {
		return view.envTokenSource(role, envVar)
	}
	return TokenSource{Role: role, Kind: TokenSourceUnset, EnvVar: envVar}
}

// credentialTokenSource resolves ONE caller/worker credential the way
// buildCallers does: the literal wins, else the named variable.
func credentialTokenSource(role, field, literal, envVar string, view EnvView) TokenSource {
	if literal != "" {
		return configTokenSource(role, field, literal)
	}
	if envVar != "" {
		if _, ok := view.token(envVar); ok {
			return view.envTokenSource(role, envVar)
		}
	}
	return TokenSource{Role: role, Kind: TokenSourceUnset, EnvVar: envVar}
}

func configTokenSource(role, field, value string) TokenSource {
	return TokenSource{Role: role, Kind: TokenSourceConfig, Field: field, Set: true,
		Length: len(value), Masked: TokenMask(value)}
}

// envTokenSource attributes an env-resolved value to the dotenv file it came
// from, or to the process environment when no file defines it.
func (v EnvView) envTokenSource(role, envVar string) TokenSource {
	val, _ := v.token(envVar)
	src := TokenSource{Role: role, Kind: TokenSourceProcess, EnvVar: envVar, Set: true,
		Length: len(val), Masked: TokenMask(val)}
	if scope := v.scope(envVar); scope != "" {
		src.Kind, src.Scope = TokenSourcePersistentEnv, scope
	} else if file := v.file(envVar); file != "" {
		src.Kind, src.EnvFile = TokenSourceEnvFile, file
	}
	return src
}
