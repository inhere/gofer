package jobstore

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/authz"
)

// storedCallerRow is a valid row for the tests below (the hash is 64 hex digits because a
// runtime row always has one).
func storedCallerRow(id string) StoredCaller {
	return StoredCaller{
		ID: id, Name: "CI", TokenHash: strings.Repeat("ab", 32), TokenHint: "gct_…beef",
		CreatedBy: "operator",
	}
}

// TestCallerTablesOnFreshDB: the two P1 tables and their indexes exist on a brand-new
// database, with every column the rest of P1 reads (they are the read-only preflight's
// contract, so a missing column is a broken upgrade path, not a cosmetic issue).
func TestCallerTablesOnFreshDB(t *testing.T) {
	s := openTest(t)
	for _, col := range []string{"id", "name", "token_hash", "token_hint", "limits_json",
		"ceiling_yaml", "disabled_at", "superseded_at", "created_by", "created_at",
		"rotated_at", "updated_at"} {
		assert.True(t, tableHasColumn(t, s, "auth_callers", col), "auth_callers.%s", col)
	}
	for _, col := range []string{"id", "actor", "path", "before_masked", "after_masked", "at"} {
		assert.True(t, tableHasColumn(t, s, "config_audit", col), "config_audit.%s", col)
	}
	assert.True(t, indexExists(t, s, "idx_config_audit_at"))
}

// TestCallerTxIsAtomic: a caller row and its audit line are ONE unit. A callback that fails
// after both writes leaves neither behind; a callback that succeeds leaves both.
func TestCallerTxIsAtomic(t *testing.T) {
	s := openTest(t)
	boom := errors.New("boom")
	assert.ErrIs(t, s.WithCallerTx(func(tx CallerTx) error {
		if err := tx.InsertCallerTx(storedCallerRow("ci")); err != nil {
			return err
		}
		if err := tx.InsertConfigAudit(ConfigAuditEntry{Actor: "operator", Path: "server.callers.ci"}); err != nil {
			return err
		}
		return boom
	}), boom)
	rows, err := s.ListStoredCallers()
	assert.NoErr(t, err)
	assert.Len(t, rows, 0, "a rolled back transaction must leave no caller row")
	audit, err := s.ListConfigAudit("", 0)
	assert.NoErr(t, err)
	assert.Len(t, audit, 0, "a rolled back transaction must leave no audit row")

	assert.NoErr(t, s.WithCallerTx(func(tx CallerTx) error {
		if err := tx.InsertCallerTx(storedCallerRow("ci")); err != nil {
			return err
		}
		return tx.InsertConfigAudit(ConfigAuditEntry{
			Actor: "operator", Path: "server.callers.ci", BeforeMasked: "none", AfterMasked: "name=\"CI\"",
		})
	}))
	rows, err = s.ListStoredCallers()
	assert.NoErr(t, err)
	assert.Len(t, rows, 1)
	assert.Eq(t, "ci", rows[0].ID)
	audit, err = s.ListConfigAudit("server.callers.", 0)
	assert.NoErr(t, err)
	assert.Len(t, audit, 1)
	assert.Eq(t, "server.callers.ci", audit[0].Path)
}

// TestCallerTxSeesItsOwnWrite: the transaction re-reads the rows it just wrote, which is
// what lets the caller write path re-merge from authoritative data.
func TestCallerTxSeesItsOwnWrite(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.WithCallerTx(func(tx CallerTx) error {
		if err := tx.InsertCallerTx(storedCallerRow("ci")); err != nil {
			return err
		}
		row, ok, err := tx.GetCaller("ci")
		if err != nil {
			return err
		}
		assert.True(t, ok)
		row.DisabledAt = tx.Now()
		return tx.UpdateCallerTx(row)
	}))
	rows, err := s.ListStoredCallers()
	assert.NoErr(t, err)
	assert.Len(t, rows, 1)
	assert.True(t, rows[0].DisabledAt != 0, "the update inside the transaction must be visible after commit")
}

// TestCallerTxOrphansGrantsInTheSameTransaction: the takeover of a superseded caller marks
// the `caller:<id>` grants orphaned inside the same transaction (design D10), so a rollback
// re-activates them.
func TestCallerTxOrphansGrantsInTheSameTransaction(t *testing.T) {
	s := openTest(t)
	// Any real permission name works: the grant is only the thing the takeover orphans.
	perm := authz.Seeded(authz.SeedV1)[0].Name
	g, err := s.CreateAuthGrant(AuthGrant{Subject: "caller:ci", Pattern: perm})
	assert.NoErr(t, err)
	boom := errors.New("boom")
	assert.ErrIs(t, s.WithCallerTx(func(tx CallerTx) error {
		ids, err := tx.ActiveGrantIDs("caller:ci")
		if err != nil {
			return err
		}
		if _, err := tx.OrphanGrants(ids); err != nil {
			return err
		}
		return boom
	}), boom)
	got, ok, err := s.GetAuthGrant(g.ID)
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, int64(0), got.OrphanedAt, "a rolled back transaction must leave the grant active")

	assert.NoErr(t, s.WithCallerTx(func(tx CallerTx) error {
		ids, err := tx.ActiveGrantIDs("caller:ci")
		if err != nil {
			return err
		}
		_, err = tx.OrphanGrants(ids)
		return err
	}))
	got, ok, err = s.GetAuthGrant(g.ID)
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.True(t, got.OrphanedAt != 0, "the takeover must orphan the grant")
}

// TestPruneConfigAuditWithoutJobPolicy: the audit retention does not depend on the job
// retention policy (D8) — a deployment that prunes no job still expires old audit rows.
func TestPruneConfigAuditWithoutJobPolicy(t *testing.T) {
	s := openTest(t)
	now := time.Now().Unix()
	old := now - int64((91*24*time.Hour)/time.Second)
	fresh := now - int64(24*time.Hour/time.Second)
	assert.NoErr(t, s.InsertConfigAudit(ConfigAuditEntry{At: old, Actor: "operator", Path: "server.callers.old"}))
	assert.NoErr(t, s.InsertConfigAudit(ConfigAuditEntry{At: fresh, Actor: "operator", Path: "server.callers.fresh"}))

	deleted, _, err := s.PruneJobs(RetentionPolicy{}, now)
	assert.NoErr(t, err)
	assert.Eq(t, 0, deleted)

	audit, err := s.ListConfigAudit("", 0)
	assert.NoErr(t, err)
	assert.Len(t, audit, 1)
	assert.Eq(t, "server.callers.fresh", audit[0].Path)
}

// TestOpenExistingForRepairMissingPathCreatesNoFile: the offline repair must never CREATE a
// database (a typo'd path would otherwise leave an empty one, which a later `Open` would
// happily adopt).
func TestOpenExistingForRepairMissingPathCreatesNoFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope", "gofer.db")
	s, err := OpenExistingForRepair(path)
	assert.Err(t, err)
	assert.Nil(t, s)
	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "the repair open must not create %s", path)
	_, dirErr := os.Stat(filepath.Dir(path))
	assert.True(t, os.IsNotExist(dirErr), "the repair open must not create the parent directory")
}

// TestOpenExistingForRepairExistingDB: an existing database opens without any migration and
// reads its rows.
func TestOpenExistingForRepairExistingDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gofer.db")
	s, err := Open(path)
	assert.NoErr(t, err)
	assert.NoErr(t, s.WithCallerTx(func(tx CallerTx) error {
		return tx.InsertCallerTx(storedCallerRow("ci"))
	}))
	assert.NoErr(t, s.Close())

	repair, err := OpenExistingForRepair(path)
	assert.NoErr(t, err)
	defer func() { _ = repair.Close() }()
	assert.Eq(t, path, repair.DBPath())
	rows, err := repair.ListStoredCallers()
	assert.NoErr(t, err)
	assert.Len(t, rows, 1)
}

// TestOpenExistingForRepairWithoutCallerTable: a database from before this feature has no
// auth_callers table, so the repair refuses instead of inventing one.
func TestOpenExistingForRepairWithoutCallerTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gofer.db")
	s, err := Open(path)
	assert.NoErr(t, err)
	_, dropErr := s.db.Exec(`DROP TABLE auth_callers`)
	assert.NoErr(t, dropErr)
	assert.NoErr(t, s.Close())

	repair, err := OpenExistingForRepair(path)
	assert.Err(t, err)
	assert.Nil(t, repair)
}

// TestRepairCallerLocksBadCeiling: an unusable ceiling is replaced by the caller's locked
// ceiling, the quotas are cleared, the row is disabled and the change is audited — and the
// row is NOT deleted.
func TestRepairCallerLocksBadCeiling(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.WithCallerTx(func(tx CallerTx) error {
		row := storedCallerRow("ci")
		row.CeilingYAML = "projects: [\"[\""
		row.LimitsJSON = "{"
		return tx.InsertCallerTx(row)
	}))

	res, err := s.RepairCaller("ci", "offline:operator", RepairOptions{
		ValidCeiling:      func(s string) error { return errors.New("bad ceiling") },
		ValidLimits:       func(s string) error { return errors.New("bad limits") },
		ValidName:         func(string) error { return nil },
		LockedCeilingYAML: "read_only: true\n",
	})
	assert.NoErr(t, err)
	assert.True(t, res.Found)
	assert.True(t, res.Disabled)
	assert.Contains(t, strings.Join(res.Changed, ","), "ceiling")
	assert.Contains(t, strings.Join(res.Changed, ","), "limits")

	row, ok, err := s.GetStoredCaller("ci")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "read_only: true\n", row.CeilingYAML)
	assert.Eq(t, "", row.LimitsJSON)
	assert.True(t, row.DisabledAt != 0)
	assert.True(t, row.TokenHash != "", "the repair must never touch the token hash")

	audit, err := s.ListConfigAudit("server.callers.ci", 0)
	assert.NoErr(t, err)
	assert.Len(t, audit, 1)
	assert.Eq(t, "offline:operator", audit[0].Actor)
	assert.False(t, strings.Contains(audit[0].AfterMasked, row.TokenHash), "the audit must not carry the token hash")
}

// TestRepairCallerLeavesAHealthyRowAlone: a row that passes every check is not touched (no
// audit line, no change), so a repair is idempotent.
func TestRepairCallerLeavesAHealthyRowAlone(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.WithCallerTx(func(tx CallerTx) error {
		return tx.InsertCallerTx(storedCallerRow("ci"))
	}))
	res, err := s.RepairCaller("ci", "offline:operator", RepairOptions{
		ValidCeiling: func(string) error { return nil },
		ValidLimits:  func(string) error { return nil },
		ValidName:    func(string) error { return nil },
	})
	assert.NoErr(t, err)
	assert.Len(t, res.Changed, 0)
	row, _, err := s.GetStoredCaller("ci")
	assert.NoErr(t, err)
	assert.Eq(t, int64(0), row.DisabledAt)
	audit, err := s.ListConfigAudit("", 0)
	assert.NoErr(t, err)
	assert.Len(t, audit, 0)
}
