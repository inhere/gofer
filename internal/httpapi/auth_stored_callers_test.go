package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
)

// 鉴权支持入库用户（配置入库 P1，设计 docs/design/2026-11-03-config-into-db-design.md §鉴权，
// 计划 T4）：
//
//   - a caller stored in the database authenticates with its TOKEN, of which the server holds only
//     the sha256 — the hash itself is never a credential;
//   - a disabled stored caller authenticates nothing and holds no permission;
//   - a deployment whose only callers are disabled does NOT fall back to the anonymous caller;
//   - a long connection opened by a stored caller survives a re-judgment and ends when its owner is
//     disabled or rotated.

// sha256Hex is the token hash form the caller store keeps (lowercase hex).
func tokenSha256Hex(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// storedCaller is the config projection of an auth_callers row holding token: only the hash.
func storedCaller(id, token string) config.CallerConfig {
	return config.CallerConfig{
		ID: id, Source: config.CallerSourceDB, TokenHash: tokenSha256Hex(token), TokenHint: "gct_…" + token[len(token)-4:],
	}
}

// TestStoredCallerAuthenticatesAndIsBoundedByItsCeiling: the token of a database caller
// authenticates (the server never sees the plaintext), the job carries that caller id, and the
// caller's ceiling — read from the same caller entry — is what bounds it. A ceiling and a quota
// reaching a stored caller is the point of the merge: the entry is a config.CallerConfig like any
// other, only its credential is a hash.
func TestStoredCallerAuthenticatesAndIsBoundedByItsCeiling(t *testing.T) {
	t.Parallel()
	bounded := storedCaller("dbuser", "gct-dbuser-token")
	bounded.Ceiling = &config.CallerCeiling{Runners: []string{"w-elsewhere"}, AllowExec: boolptr(true)}
	s := newTestServerCfg(t, config.ServerConfig{Callers: []config.CallerConfig{bounded}})

	// The ceiling refuses the runner the fixture's project allows, and admission's refusal is what
	// answers 403 — a caller that could NOT authenticate would be a 401 instead.
	resp := do(t, s, http.MethodPost, "/v1/jobs", "gct-dbuser-token", job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	assert.Eq(t, http.StatusForbidden, resp.StatusCode, "a stored caller's ceiling must refuse a runner outside it")
	_ = resp.Body.Close()

	// Without the ceiling the same credential submits, and the job is stamped with its id. A
	// separate server: the job service reads the configuration it was built with, not the one the
	// HTTP layer swapped.
	open := newTestServerCfg(t, config.ServerConfig{Callers: []config.CallerConfig{storedCaller("dbuser", "gct-dbuser-token")}})
	created, status := createJob(t, open, "gct-dbuser-token")
	assert.Eq(t, http.StatusOK, status, "a stored caller must authenticate with its token")
	assert.Eq(t, "dbuser", created.CallerID, "the job is stamped with the stored caller's id")
	waitDoneTok(t, open, created.ID, "gct-dbuser-token")
}

// TestStoredCallerRejectsWrongTokenAndTheHashItself: an unrelated token is 401, and so is the
// stored HASH presented as a bearer token — the hash is what the server compares against, it is not
// a credential (SR1301: the secret exists only in the token the operator received once).
func TestStoredCallerRejectsWrongTokenAndTheHashItself(t *testing.T) {
	t.Parallel()
	sc := storedCaller("dbuser", "gct-dbuser-token")
	s := newTestServerCfg(t, config.ServerConfig{Callers: []config.CallerConfig{sc}})

	for name, token := range map[string]string{
		"unrelated token": "gct-someone-else",
		"the stored hash": sc.TokenHash,
	} {
		resp := do(t, s, http.MethodGet, "/v1/projects", token, nil)
		assert.Eq(t, http.StatusUnauthorized, resp.StatusCode, name)
		_ = resp.Body.Close()
	}
}

// TestDisabledStoredCallerAuthenticatesNothing: disabling a stored caller takes its credential away
// at once (the token is still the operator's — the caller simply may not use it any more).
func TestDisabledStoredCallerAuthenticatesNothing(t *testing.T) {
	t.Parallel()
	sc := storedCaller("dbuser", "gct-dbuser-token")
	sc.Disabled = true
	s := newTestServerCfg(t, config.ServerConfig{Callers: []config.CallerConfig{sc}})

	resp := do(t, s, http.MethodGet, "/v1/projects", "gct-dbuser-token", nil)
	assert.Eq(t, http.StatusUnauthorized, resp.StatusCode, "a disabled caller's token must not authenticate")
	_ = resp.Body.Close()
}

// TestAllCallersDisabledIsNotEmptyAuth is the reverse-validated rule of plan T4: `allow_empty_token`
// admits the anonymous caller only when the deployment HAS no caller. Callers that are merely
// disabled still count — otherwise disabling the last caller would silently open the server to
// anonymous requests, which is the opposite of what the operator asked for.
func TestAllCallersDisabledIsNotEmptyAuth(t *testing.T) {
	t.Parallel()
	disabled := storedCaller("dbuser", "gct-dbuser-token")
	disabled.Disabled = true

	s := newTestServerCfg(t, config.ServerConfig{
		AllowEmptyToken: true,
		Callers:         []config.CallerConfig{disabled},
	})
	resp := do(t, s, http.MethodGet, "/v1/projects", "", nil)
	assert.Eq(t, http.StatusUnauthorized, resp.StatusCode,
		"a deployment whose only caller is disabled must not fall back to the anonymous caller")
	_ = resp.Body.Close()

	// The control: the same deployment WITHOUT any caller is the anonymous one, exactly as before.
	anon := newTestServerCfg(t, config.ServerConfig{AllowEmptyToken: true})
	resp = do(t, anon, http.MethodGet, "/v1/projects", "", nil)
	assert.Eq(t, http.StatusOK, resp.StatusCode, "a deployment with no caller at all keeps the anonymous pass-through")
	_ = resp.Body.Close()
}

// TestEffectiveDisabledCallerHasNoPermissions: a disabled caller resolves to an EMPTY decision,
// whatever the configuration derives or the database granted before it was switched off. The
// control caller proves the users' grants really are there, so an implementation that simply has
// nothing to grant could not pass this case.
func TestEffectiveDisabledCallerHasNoPermissions(t *testing.T) {
	t.Parallel()
	disabled := storedCaller("dbuser", "gct-dbuser-token")
	disabled.Disabled = true
	s := newTestServerCfg(t, config.ServerConfig{Callers: []config.CallerConfig{
		disabled,
		{ID: "alice", Token: "tok-alice"},
	}})

	assert.False(t, s.effective("dbuser", "").Has(authz.PermInteractionAnswer),
		"a disabled caller must hold no permission")
	assert.True(t, s.effective("alice", "").Has(authz.PermInteractionAnswer),
		"the control caller must still hold the users' grants")
}

// TestTokenFingerprintOfHashIsThePlaintextFingerprint pins the one property the long-connection
// table relies on: a stored caller's fingerprint (taken from the stored hash) is EXACTLY the value
// the same token produces as plaintext. A change to either算法 would break every stored caller's
// open connection on the next sweep, silently.
func TestTokenFingerprintOfHashIsThePlaintextFingerprint(t *testing.T) {
	t.Parallel()
	for _, token := range []string{"gct-dbuser-token", "another-token"} {
		assert.Eq(t, tokenFingerprint(token), tokenFingerprintOfHash(tokenSha256Hex(token)), token)
	}
	entries := buildCallers(&config.ServerConfig{Callers: []config.CallerConfig{
		{ID: "file", Token: "tok-file"},
		storedCaller("dbuser", "gct-dbuser-token"),
	}}, "")
	assert.Eq(t, 2, len(entries), "the file caller and the stored caller are both resolved")
	for _, ce := range entries {
		token := map[string]string{"file": "tok-file", "dbuser": "gct-dbuser-token"}[ce.id]
		assert.Eq(t, tokenFingerprint(token), ce.fingerprint, ce.id)
	}
	// The stored entry holds no plaintext token at all.
	for _, ce := range entries {
		if ce.id == "dbuser" {
			assert.Eq(t, "", ce.token, "a stored caller carries no plaintext token")
			assert.Eq(t, tokenSha256Hex("gct-dbuser-token"), ce.tokenHash)
		}
	}
}

// TestLookupCallerInMatchesBothCredentialKinds: one scan resolves a file caller (plaintext) and a
// stored caller (hash) — the same function, two forms, and no cross-match (a hash is not a token).
func TestLookupCallerInMatchesBothCredentialKinds(t *testing.T) {
	t.Parallel()
	entries := buildCallers(&config.ServerConfig{Callers: []config.CallerConfig{
		{ID: "file", Token: "tok-file"},
		storedCaller("dbuser", "gct-dbuser-token"),
	}}, "")

	ce, ok := lookupCallerIn(entries, "tok-file")
	assert.True(t, ok, "the file caller's plaintext token must match")
	assert.Eq(t, "file", ce.id)
	ce, ok = lookupCallerIn(entries, "gct-dbuser-token")
	assert.True(t, ok, "the stored caller's token must match through its hash")
	assert.Eq(t, "dbuser", ce.id)
	if _, ok = lookupCallerIn(entries, tokenSha256Hex("gct-dbuser-token")); ok {
		t.Fatal("the stored hash must never authenticate")
	}
}

// TestStoredCallerLongConnSurvivesSweepAndEndsOnDisable: the periodic re-judgment recognises a
// stored caller's connection by its credential (a caller that only exists as a hash would otherwise
// look tokenless and be dropped at once), and disabling that caller ends the connection.
func TestStoredCallerLongConnSurvivesSweepAndEndsOnDisable(t *testing.T) {
	t.Parallel()
	sc := storedCaller("dbuser", "gct-dbuser-token")
	s := newTestServerCfg(t, config.ServerConfig{Callers: []config.CallerConfig{sc}})
	conn := startTunnel(t, s, "gct-dbuser-token", "")

	// The reverse validation of plan T4: judging the same, unchanged configuration must KEEP the
	// connection. Recomputing the fingerprint from the (absent) plaintext would drop it here.
	s.recheckLiveConns("test")
	echoTunnelOnce(t, conn)
	waitLiveConn(t, s, 1, "an unchanged stored caller keeps its long connection")

	// Rotation: the credential the connection was opened with is gone.
	rotated := storedCaller("dbuser", "gct-dbuser-token-2")
	s.SetServerConfig(&config.ServerConfig{Callers: []config.CallerConfig{rotated}})
	waitLiveConnClosed(t, s, conn, "rotating a stored caller's token must end its long connection")
}

// TestStoredCallerViewExposesSourceAndHint: the configuration view says where a caller comes from and
// that it holds a credential it cannot show. Without `token_set` the console would render a live
// stored caller as having no token, and `source` is what marks it as not-from-the-file.
func TestStoredCallerViewExposesSourceAndHint(t *testing.T) {
	t.Parallel()
	disabled := storedCaller("dbuser", "gct-dbuser-token")
	disabled.Disabled = true
	views := buildCallerViews([]config.CallerConfig{
		{ID: "alice", Token: "tok-alice"},
		storedCaller("dbuser", "gct-dbuser-token"),
		disabled,
	})
	assert.Eq(t, 3, len(views))
	assert.Eq(t, "", views[0].Source, "a file caller keeps the source absent")
	assert.True(t, views[0].TokenSet, "a file caller's plaintext token is a credential")
	assert.Eq(t, "", views[0].TokenHint, "the hint exists only for a stored caller")
	assert.Eq(t, config.CallerSourceDB, views[1].Source)
	assert.True(t, views[1].TokenSet, "a stored caller holds a credential even though the view never shows it")
	assert.True(t, strings.HasPrefix(views[1].TokenHint, "gct_…"), "the hint rides the view: %q", views[1].TokenHint)
	assert.True(t, views[2].Disabled, "a disabled stored caller is reported as disabled")
}
