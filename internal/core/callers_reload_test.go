package core

import (
	"os"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// reloadMarkerConfig writes the store marker into an existing config file without touching
// the rest of its bytes (a marker has no Config field, so it cannot be produced by Save).
func reloadMarkerConfig(t *testing.T, path string) {
	t.Helper()
	orig, err := os.ReadFile(path)
	assert.NoErr(t, err)
	marked := "config_store:\n  id: 42\n" + string(orig)
	assert.NoErr(t, os.WriteFile(path, []byte(marked), 0o600))
}

// buildWithCallerStore is the serve-shaped Core of these tests: the store is passed in
// (core.WithStore), so the reload path merges the stored callers.
func buildWithCallerStore(t *testing.T, cfg *config.Config, path string, st *jobstore.Store) *Core {
	t.Helper()
	cr, err := Build(cfg, WithConfigPath(path), WithAgentDetector(agent.NoopDetector{}), WithStore(st))
	assert.NoErr(t, err)
	t.Cleanup(func() { _ = cr.Close() })
	return cr
}

// TestReloadRefusesStoreMarkerAndKeepsSnapshot (D11): a reload of a file carrying the P2
// store marker fails with ErrStoreMarker and the live generation is untouched — the process
// keeps running on the configuration it validated at startup.
func TestReloadRefusesStoreMarkerAndKeepsSnapshot(t *testing.T) {
	cfg, path := loadedCallerConfig(t)
	st, _ := openCallerStore(t)
	cr := buildWithCallerStore(t, cfg, path, st)

	before := cr.Snapshot()
	reloadMarkerConfig(t, path)

	result, err := cr.ReloadDetailed(path)
	assert.Err(t, err, ErrStoreMarker)
	assert.Contains(t, result.Error, "migrated into the database")

	after := cr.Snapshot()
	assert.Eq(t, before.Rev, after.Rev, "a refused reload must not publish a generation")
	assert.True(t, before.Cfg == after.Cfg, "a refused reload must keep the same config object")
}

// TestReloadFailsWhenStoredCallersCannotBeRead: a callers read that failed is fatal for the
// reload — the old snapshot stays, no new generation is published (so the httpapi orphan scan,
// which runs on a published caller set, never runs on an incomplete one) and the caller's
// grants are still active.
func TestReloadFailsWhenStoredCallersCannotBeRead(t *testing.T) {
	cfg, path := loadedCallerConfig(t)
	st, dbPath := openCallerStore(t)
	insertStoredCaller(t, st, "ci")
	perm := authz.Seeded(authz.SeedV1)[0].Name
	grant, err := st.CreateAuthGrant(jobstore.AuthGrant{Subject: "caller:ci", Pattern: perm})
	assert.NoErr(t, err)
	cr := buildWithCallerStore(t, cfg, path, st)

	before := cr.Snapshot()
	// Make auth_callers unreadable: a CLOSED store answers every read with an error, which is
	// the failure the merge must never treat as "no stored callers".
	assert.NoErr(t, st.Close())

	_, err = cr.ReloadDetailed(path)
	if err == nil {
		t.Fatal("a failed callers read must fail the reload")
	}
	assert.Contains(t, err.Error(), "read stored callers")

	after := cr.Snapshot()
	assert.Eq(t, before.Rev, after.Rev, "a failed reload must not publish a generation")
	assert.True(t, before.Cfg == after.Cfg)

	// The grant is untouched: it is not an orphan of a caller set the failed merge produced.
	insp, err := jobstore.OpenExistingForRepair(dbPath)
	assert.NoErr(t, err)
	defer func() { _ = insp.Close() }()
	got, ok, err := insp.GetAuthGrant(grant.ID)
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, int64(0), got.OrphanedAt, "a failed reload must not orphan anything")
}

// TestReloadTakesOverStoredCallerDeclaredInTheFile (D10): the file adding an id the database
// also has takes that identity over DURING the reload — the stored row is disabled and
// superseded, its grants orphaned and one audit line written — and a second reload repeats
// none of it.
func TestReloadTakesOverStoredCallerDeclaredInTheFile(t *testing.T) {
	cfg, path := loadedCallerConfig(t)
	st, _ := openCallerStore(t)
	insertStoredCaller(t, st, "ci")
	perm := authz.Seeded(authz.SeedV1)[0].Name
	grant, err := st.CreateAuthGrant(jobstore.AuthGrant{Subject: "caller:ci", Pattern: perm})
	assert.NoErr(t, err)
	cr := buildWithCallerStore(t, cfg, path, st)
	rev := cr.Snapshot().Rev

	// The file now declares the stored id itself: the file wins.
	writeConfig(t, path, &config.Config{
		Storage: config.StorageConfig{Root: t.TempDir()},
		Server: config.ServerConfig{Addr: "127.0.0.1:1", Callers: []config.CallerConfig{
			{ID: "operator", Token: "root-token", CanAdmin: true},
			{ID: "ci", Token: "file-token"},
		}},
	})
	result, err := cr.ReloadDetailed(path)
	assert.NoErr(t, err)
	assert.True(t, result.Rev > rev)
	assert.Len(t, cr.Config().Server.StoredCallerIDs(), 0, "the file entry hides the stored caller")
	assert.Len(t, cr.Config().Server.FileCallers(), 2)

	row, ok, err := st.GetStoredCaller("ci")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.True(t, row.DisabledAt != 0, "the takeover must disable the stored row")
	assert.True(t, row.SupersededAt != 0, "the takeover must mark the stored row superseded")

	got, ok, err := st.GetAuthGrant(grant.ID)
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.True(t, got.OrphanedAt != 0, "the takeover must orphan the caller's grants")

	audit, err := st.ListConfigAudit("server.callers.ci", 0)
	assert.NoErr(t, err)
	if len(audit) != 1 {
		t.Fatalf("the takeover must write exactly one audit line, got %d", len(audit))
	}
	assert.Eq(t, fileTakeoverActor, audit[0].Actor)
	assert.False(t, strings.Contains(audit[0].AfterMasked, row.TokenHash), "the audit must not carry the token hash")

	// A second reload of the same file does not repeat the takeover.
	_, err = cr.ReloadDetailed(path)
	assert.NoErr(t, err)
	audit, err = st.ListConfigAudit("server.callers.ci", 0)
	assert.NoErr(t, err)
	assert.Len(t, audit, 1, "a second reload must not audit the takeover again")
}
