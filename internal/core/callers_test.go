package core

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// openCallerStore opens a metadata store in a temp dir and returns it with its path.
func openCallerStore(t *testing.T) (*jobstore.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gofer.db")
	st, err := jobstore.Open(path)
	assert.NoErr(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st, path
}

// insertStoredCaller writes one enabled stored caller.
func insertStoredCaller(t *testing.T, st *jobstore.Store, id string) {
	t.Helper()
	assert.NoErr(t, st.WithCallerTx(func(tx jobstore.CallerTx) error {
		return tx.InsertCallerTx(jobstore.StoredCaller{
			ID: id, Name: "CI", TokenHash: strings.Repeat("ab", 32), TokenHint: "gct_…beef",
			CreatedBy: "operator",
		})
	}))
}

// loadedCallerConfig is a config read from a real file (so it carries the file source map
// and the defaults) that declares one root administrator.
func loadedCallerConfig(t *testing.T) (*config.Config, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeConfig(t, path, &config.Config{
		Storage: config.StorageConfig{Root: t.TempDir()},
		Server: config.ServerConfig{Addr: "127.0.0.1:1", Callers: []config.CallerConfig{
			{ID: "operator", Token: "root-token", CanAdmin: true},
		}},
	})
	cfg, _, err := config.Load(path)
	assert.NoErr(t, err)
	return cfg, path
}

// TestPrepareCallerStoreRefusesStoreMarkerWithoutCreatingDB (D11): a configuration carrying
// the P2 marker is refused BEFORE the database is opened — no empty database appears beside
// the operator's real one.
func TestPrepareCallerStoreRefusesStoreMarkerWithoutCreatingDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "gofer.db")
	cfg := &config.Config{
		Storage: config.StorageConfig{
			Root:   dir,
			DBPath: dbPath,
		},
	}
	assert.True(t, strings.Contains(config.StorageConfig{}.DBPath, ""), "sanity")
	// The marker can only come from a document, so write one.
	path := filepath.Join(dir, "config.yaml")
	assert.NoErr(t, os.WriteFile(path, []byte("config_store:\n  id: 42\nstorage:\n  db_path: "+dbPath+"\n"), 0o600))
	marked, _, err := config.Load(path)
	assert.NoErr(t, err)
	assert.True(t, marked.HasStoreMarker())

	store, err := PrepareCallerStore(marked, CallerMergeServer)
	assert.Err(t, err, ErrStoreMarker)
	assert.Nil(t, store)
	_, statErr := os.Stat(dbPath)
	assert.True(t, os.IsNotExist(statErr), "the refused start must not create %s", dbPath)
	_ = cfg
}

// TestMergeStoredCallersReadFailureIsFatal: a callers read that FAILED is never read as "no
// stored callers" — the merge answers the error and the configuration stays as the file left
// it (the reverse verification of T3: returning an empty set here loses every stored
// identity silently).
func TestMergeStoredCallersReadFailureIsFatal(t *testing.T) {
	st, _ := openCallerStore(t)
	assert.NoErr(t, st.Close())
	cfg := &config.Config{}
	err := MergeStoredCallers(cfg, st, CallerMergeServer)
	assert.Err(t, err)
	assert.Len(t, cfg.Server.Callers, 0)
}

// TestMergeStoredCallersTakeover (D10): when the FILE declares an id the database also has,
// the stored row is hidden, disabled, its `caller:<id>` grants is orphaned and exactly one
// audit line records it — and a second merge does not repeat any of it.
func TestMergeStoredCallersTakeover(t *testing.T) {
	st, _ := openCallerStore(t)
	insertStoredCaller(t, st, "ci")
	perm := authz.Seeded(authz.SeedV1)[0].Name
	grant, err := st.CreateAuthGrant(jobstore.AuthGrant{Subject: "caller:ci", Pattern: perm})
	assert.NoErr(t, err)

	cfg := &config.Config{Server: config.ServerConfig{Callers: []config.CallerConfig{
		{ID: "operator", Token: "root-token", CanAdmin: true},
		{ID: "ci", Token: "file-token"},
	}}}
	assert.NoErr(t, MergeStoredCallers(cfg, st, CallerMergeServer))
	assert.Len(t, cfg.Server.Callers, 2, "only the file callers stay")

	row, ok, err := st.GetStoredCaller("ci")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.True(t, row.SupersededAt != 0, "the stored row must be marked superseded")
	assert.True(t, row.DisabledAt != 0, "the stored row must be disabled")
	assert.True(t, row.SupersededAt >= row.RotatedAt, "the takeover stamp must not predate the last rotation")

	got, ok, err := st.GetAuthGrant(grant.ID)
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.True(t, got.OrphanedAt != 0, "the takeover must orphan the caller's grants")

	audit, err := st.ListConfigAudit("server.callers.ci", 0)
	assert.NoErr(t, err)
	assert.Len(t, audit, 1)
	assert.Eq(t, fileTakeoverActor, audit[0].Actor)

	// Again (a reload, or a restart): nothing is repeated.
	cfg2 := &config.Config{Server: config.ServerConfig{Callers: []config.CallerConfig{
		{ID: "operator", Token: "root-token", CanAdmin: true},
		{ID: "ci", Token: "file-token"},
	}}}
	assert.NoErr(t, MergeStoredCallers(cfg2, st, CallerMergeServer))
	audit, err = st.ListConfigAudit("server.callers.ci", 0)
	assert.NoErr(t, err)
	assert.Len(t, audit, 1, "a second merge must not audit the takeover again")
}

// TestMergeStoredCallersReadOnlyWritesNothing: the standalone mcp path hides the superseded
// row in memory only — the database stays untouched (the CLI does not write the database).
func TestMergeStoredCallersReadOnlyWritesNothing(t *testing.T) {
	st, _ := openCallerStore(t)
	insertStoredCaller(t, st, "ci")
	cfg := &config.Config{Server: config.ServerConfig{Callers: []config.CallerConfig{
		{ID: "ci", Token: "file-token"},
	}}}
	assert.NoErr(t, MergeStoredCallers(cfg, st, CallerMergeReadOnly))
	assert.Len(t, cfg.Server.Callers, 1)
	row, _, err := st.GetStoredCaller("ci")
	assert.NoErr(t, err)
	assert.Eq(t, int64(0), row.SupersededAt, "a read-only merge must not take over")
	assert.Eq(t, int64(0), row.DisabledAt)
	audit, err := st.ListConfigAudit("", 0)
	assert.NoErr(t, err)
	assert.Len(t, audit, 0)
}

// TestUpdateCallersPublishesWithoutSavingTheFile (D4): a caller write goes through the
// caller transaction, is visible in the very next snapshot, never calls saveConfig — so a
// deployment without a config file does not get one — and a rejected candidate leaves the
// database untouched.
func TestUpdateCallersPublishesWithoutSavingTheFile(t *testing.T) {
	cfg, path := loadedCallerConfig(t)
	st, _ := openCallerStore(t)
	cr, err := Build(cfg, WithConfigPath(path), WithAgentDetector(agent.NoopDetector{}), WithStore(st))
	assert.NoErr(t, err)
	defer func() { _ = cr.Close() }()

	before, err := os.ReadFile(path)
	assert.NoErr(t, err)

	snap, err := cr.UpdateCallers(func(next *config.Config, tx jobstore.CallerTx) error {
		return tx.InsertCallerTx(jobstore.StoredCaller{
			ID: "ci", Name: "CI", TokenHash: strings.Repeat("ab", 32), TokenHint: "gct_…beef",
			CreatedBy: "operator",
		})
	})
	assert.NoErr(t, err)
	assert.True(t, snap.Rev > 1, "a caller write must publish a new generation")
	ids := cr.Config().Server.StoredCallerIDs()
	assert.Len(t, ids, 1)
	assert.Eq(t, "ci", ids[0])

	after, err := os.ReadFile(path)
	assert.NoErr(t, err)
	assert.Eq(t, string(before), string(after), "a caller write must not rewrite the config file")

	// The next write only sees the row it wrote; the stored caller is not duplicated.
	snap2, err := cr.UpdateCallers(func(next *config.Config, tx jobstore.CallerTx) error {
		row, ok, err := tx.GetCaller("ci")
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("row disappeared")
		}
		row.DisabledAt = tx.Now()
		return tx.UpdateCallerTx(row)
	})
	assert.NoErr(t, err)
	assert.Eq(t, snap.Rev+1, snap2.Rev)
	assert.Len(t, cr.Config().Server.StoredCallerIDs(), 1)
	assert.True(t, cr.Config().OwnerBlocked("ci"), "the disabled caller must block its owner")
}

// TestUpdateCallersValidationFailureRollsBack: a candidate the loader refuses is not
// published and its row is rolled back — a half-applied caller write would leave the running
// server and the database disagreeing.
func TestUpdateCallersValidationFailureRollsBack(t *testing.T) {
	cfg, path := loadedCallerConfig(t)
	st, _ := openCallerStore(t)
	cr, err := Build(cfg, WithConfigPath(path), WithAgentDetector(agent.NoopDetector{}), WithStore(st))
	assert.NoErr(t, err)
	defer func() { _ = cr.Close() }()

	rev := cr.Snapshot().Rev
	boom := errors.New("boom")
	_, err = cr.UpdateCallers(func(next *config.Config, tx jobstore.CallerTx) error {
		if err := tx.InsertCallerTx(jobstore.StoredCaller{
			ID: "ci", TokenHash: strings.Repeat("ab", 32), CreatedBy: "operator",
		}); err != nil {
			return err
		}
		return boom
	})
	assert.Err(t, err, boom)
	assert.Eq(t, rev, cr.Snapshot().Rev, "a failed write must not publish")
	rows, err := st.ListStoredCallers()
	assert.NoErr(t, err)
	assert.Len(t, rows, 0, "a failed write must roll back its row")

	// A candidate the validation refuses (a reserved id) rolls back too.
	_, err = cr.UpdateCallers(func(next *config.Config, tx jobstore.CallerTx) error {
		return tx.InsertCallerTx(jobstore.StoredCaller{
			ID: "ci", TokenHash: strings.Repeat("ab", 32), CeilingYAML: "projects: [\"[\"]", CreatedBy: "operator",
		})
	})
	assert.Err(t, err)
	rows, err = st.ListStoredCallers()
	assert.NoErr(t, err)
	assert.Len(t, rows, 0, "an unusable stored row must roll back")
	assert.Eq(t, rev, cr.Snapshot().Rev)
}

// TestBuildWithStoreDoesNotOpenTheDatabase: Build takes the store it is given (and closes it
// through Core.Close), instead of resolving and opening one — that is what lets serve merge
// the stored callers before the components are built.
func TestBuildWithStoreDoesNotOpenTheDatabase(t *testing.T) {
	cfg, path := loadedCallerConfig(t)
	st, dbPath := openCallerStore(t)
	insertStoredCaller(t, st, "ci")
	assert.NoErr(t, MergeStoredCallers(cfg, st, CallerMergeServer))
	assert.Len(t, cfg.Server.StoredCallerIDs(), 1)

	cr, err := Build(cfg, WithConfigPath(path), WithAgentDetector(agent.NoopDetector{}), WithStore(st))
	assert.NoErr(t, err)
	assert.Eq(t, st, cr.Store)
	assert.Len(t, cr.Config().Server.StoredCallerIDs(), 1, "the merged caller must reach the snapshot")
	assert.True(t, fileExists(dbPath))
	assert.NoErr(t, cr.Close())
}

// fileExists reports whether path exists (a small helper so the assertions read).
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
