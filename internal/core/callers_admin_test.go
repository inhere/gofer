package core

// 用户管理的组装层（配置入库 P1，计划 T6）：the CALLER-side write rules of Core —
// what a token is, which ids are refused, what happens to a caller a file entry took over, and
// which of the two paths (a caller row, or the config file for a file caller) a limit change
// takes. The HTTP binding and the caller-kind gate are httpapi's tests (callers_handler_test.go).

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// callerAdminCore builds a Core over a real config file (one root administrator) and a fresh
// metadata store, which is the wiring serve gives the caller routes.
func callerAdminCore(t *testing.T) (*Core, *config.Config, string) {
	t.Helper()
	cfg, path := loadedCallerConfig(t)
	st, _ := openCallerStore(t)
	cr, err := Build(cfg, WithConfigPath(path), WithAgentDetector(agent.NoopDetector{}), WithStore(st))
	assert.NoErr(t, err)
	t.Cleanup(func() { _ = cr.Close() })
	return cr, cfg, path
}

// TestAddCallerKeepsOnlyTheHashAndAnswersTheTokenOnce: the credential exists in the answer and
// in the caller's hands; the database holds its sha256 and a hint, the audit line holds neither
// (SR1301).
func TestAddCallerKeepsOnlyTheHashAndAnswersTheTokenOnce(t *testing.T) {
	cr, _, _ := callerAdminCore(t)
	change, err := cr.AddCaller(config.CallerSpec{
		ID: "ci", Name: "CI", Ceiling: &config.CallerCeiling{Projects: []string{"self"}},
	}, "operator")
	assert.NoErr(t, err)
	assert.True(t, strings.HasPrefix(change.Token, "gct_"), "token: %q", change.Token)
	assert.Eq(t, config.CallerStateActive, change.Caller.State)
	assert.Eq(t, config.CallerSourceDB, change.Caller.Source)

	row, ok, err := cr.Store.GetStoredCaller("ci")
	assert.NoErr(t, err)
	assert.True(t, ok)
	sum := sha256.Sum256([]byte(change.Token))
	assert.Eq(t, hex.EncodeToString(sum[:]), row.TokenHash, "the row holds the sha256 of the token")
	assert.Eq(t, "gct_…"+change.Token[len(change.Token)-4:], row.TokenHint)
	assert.Eq(t, "operator", row.CreatedBy)

	entries, err := cr.Store.ListConfigAudit(callerAuditPath+"ci", 0)
	assert.NoErr(t, err)
	assert.Len(t, entries, 1)
	for _, text := range []string{entries[0].BeforeMasked, entries[0].AfterMasked} {
		assert.False(t, strings.Contains(text, change.Token), "an audit line must never carry the token: %s", text)
		assert.False(t, strings.Contains(text, row.TokenHash), "an audit line must never carry the hash: %s", text)
	}
	// The token is not the hash: presenting the stored hash authenticates nothing.
	assert.True(t, change.Token != row.TokenHash)
}

// TestAddCallerRefusesEveryTakenID: ids are never reused, and a caller this API could not make
// visible (a file caller, a worker) is refused rather than silently hidden.
func TestAddCallerRefusesEveryTakenID(t *testing.T) {
	cr, cfg, _ := callerAdminCore(t)
	_, err := cr.AddCaller(config.CallerSpec{ID: "ci", Ceiling: &config.CallerCeiling{}},
		"operator")
	assert.ErrIs(t, err, config.ErrCallerInvalid) // an empty ceiling is not a ceiling (D16)

	_, err = cr.AddCaller(config.CallerSpec{ID: "noc"}, "operator")
	assert.ErrIs(t, err, config.ErrCallerInvalid) // no ceiling and no no_ceiling

	_, err = cr.AddCaller(config.CallerSpec{ID: "default", NoCeiling: true}, "operator")
	assert.ErrIs(t, err, config.ErrCallerInvalid) // reserved

	_, err = cr.AddCaller(config.CallerSpec{ID: "operator", NoCeiling: true}, "operator")
	assert.ErrIs(t, err, config.ErrCallerIDTaken) // a file caller's id

	// A worker binding's id is refused too.
	werr := cr.Update(func(next *config.Config) error {
		if next.Server.Workers == nil {
			next.Server.Workers = map[string]config.WorkerAuthConfig{}
		}
		next.Server.Workers["w-ci"] = config.WorkerAuthConfig{Token: "w-token"}
		return nil
	})
	assert.NoErr(t, werr)
	_, err = cr.AddCaller(config.CallerSpec{ID: "w-ci", NoCeiling: true}, "operator")
	assert.ErrIs(t, err, config.ErrCallerIDTaken)

	// A disabled (retired) row keeps its id.
	_, err = cr.AddCaller(config.CallerSpec{ID: "old", NoCeiling: true}, "operator")
	assert.NoErr(t, err)
	_, err = cr.DisableCaller("old", "operator")
	assert.NoErr(t, err)
	_, err = cr.AddCaller(config.CallerSpec{ID: "old", NoCeiling: true}, "operator")
	assert.ErrIs(t, err, config.ErrCallerIDTaken)
	_ = cfg
}

// TestCallerStateWritesClassifyTheirTarget: a file caller cannot be rotated / disabled /
// enabled (its token lives in the file), an unknown id is a 404-class error, and a stored caller
// round-trips through both states.
func TestCallerStateWritesClassifyTheirTarget(t *testing.T) {
	cr, _, _ := callerAdminCore(t)
	_, err := cr.AddCaller(config.CallerSpec{ID: "ci", Ceiling: &config.CallerCeiling{Projects: []string{"self"}}}, "operator")
	assert.NoErr(t, err)

	for name, call := range map[string]func() error{
		"rotate":  func() error { _, err := cr.RotateCaller("operator", "operator"); return err },
		"disable": func() error { _, err := cr.DisableCaller("operator", "operator"); return err },
		"enable":  func() error { _, err := cr.EnableCaller("operator", "operator"); return err },
	} {
		assert.ErrIs(t, call(), config.ErrCallerFileSource, name)
	}
	_, err = cr.RotateCaller("nobody", "operator")
	assert.ErrIs(t, err, config.ErrCallerNotFound)

	rot, err := cr.RotateCaller("ci", "operator")
	assert.NoErr(t, err)
	assert.True(t, rot.Token != "", "a rotation answers the new token")
	assert.True(t, rot.Caller.RotatedAt != 0)

	off, err := cr.DisableCaller("ci", "operator")
	assert.NoErr(t, err)
	assert.Eq(t, config.CallerStateDisabled, off.Caller.State)
	assert.True(t, cr.Config().OwnerBlocked("ci"), "a disabled caller blocks its owner's work")

	// Disabling twice is a no-op: no second audit line.
	entries, err := cr.Store.ListConfigAudit(callerAuditPath+"ci", 0)
	assert.NoErr(t, err)
	before := len(entries)
	_, err = cr.DisableCaller("ci", "operator")
	assert.NoErr(t, err)
	entries, err = cr.Store.ListConfigAudit(callerAuditPath+"ci", 0)
	assert.NoErr(t, err)
	assert.Eq(t, before, len(entries), "a repeated disable writes nothing")

	on, err := cr.EnableCaller("ci", "operator")
	assert.NoErr(t, err)
	assert.Eq(t, config.CallerStateActive, on.Caller.State)
	assert.False(t, cr.Config().OwnerBlocked("ci"))
}

// TestEnableTakenOverCallerNeedsARotation (D10) walks the rule the way an operator meets it:
//
//	file declares the id  → reload takes the row over (superseded_at + disabled_at)
//	file entry removed   → reload; the row is STILL taken over
//	enable               → refused (rotate first): the token from before the takeover is dead
//	rotate, enable       → the row is back and both marks are cleared
//
// The enable that fails while the file entry is still there is the OTHER rule (the id is taken,
// ErrCallerIDTaken): a test that stopped there would never reach the rotation check.
func TestEnableTakenOverCallerNeedsARotation(t *testing.T) {
	cr, _, path := callerAdminCore(t)
	original := readCallerCfg(t, path)
	_, err := cr.AddCaller(config.CallerSpec{ID: "ci", Ceiling: &config.CallerCeiling{Projects: []string{"self"}}}, "operator")
	assert.NoErr(t, err)

	// The file declares the same id, and a reload merges it: the takeover.
	declareCallerInFile(t, cr, path, "ci", "file-token")
	row, _, err := cr.Store.GetStoredCaller("ci")
	assert.NoErr(t, err)
	assert.True(t, row.SupersededAt != 0 && row.DisabledAt != 0, "the reload must take the row over")

	// While the file still declares the id, enable is refused because the id is TAKEN (a row
	// enabled here would be hidden again by the next merge).
	_, err = cr.EnableCaller("ci", "operator")
	assert.ErrIs(t, err, config.ErrCallerIDTaken)

	// The file entry goes away: the row is still taken over, so enabling WITHOUT a rotation must
	// be refused by the rotation rule itself — the token of the take-over era must not come back.
	writeCallerCfg(t, path, original)
	_, err = cr.ReloadConfigReport()
	assert.NoErr(t, err)
	_, err = cr.EnableCaller("ci", "operator")
	assert.ErrIs(t, err, config.ErrCallerRotationRequired,
		"after the file entry is gone, enable without a rotation is the rotation rule's refusal")
	row, _, err = cr.Store.GetStoredCaller("ci")
	assert.NoErr(t, err)
	assert.True(t, row.SupersededAt != 0,
		"the refused enable must leave the takeover mark in place (got superseded_at=%d)", row.SupersededAt)
	assert.True(t, row.DisabledAt != 0, "the refused enable must leave the caller disabled")

	// A rotation (strictly newer than the takeover, D10) makes the row usable again.
	_, err = cr.RotateCaller("ci", "operator")
	assert.NoErr(t, err)
	on, err := cr.EnableCaller("ci", "operator")
	assert.NoErr(t, err)
	assert.Eq(t, config.CallerStateActive, on.Caller.State)
	row, _, err = cr.Store.GetStoredCaller("ci")
	assert.NoErr(t, err)
	assert.Eq(t, int64(0), row.SupersededAt, "enabling clears the takeover mark")
	assert.Eq(t, int64(0), row.DisabledAt)
	assert.True(t, len(cr.Config().Server.StoredCallerIDs()) == 1, "the enabled row is in the snapshot again")
}

// TestSecondTakeoverAfterEnableRunsAgain (plan T3's leftover for T6): clearing the marks on
// enable is what makes the NEXT collision a fresh takeover — the row is disabled again, its new
// `caller:<id>` grant is orphaned again, and one more audit line records it. Reverse: an enable
// that leaves `superseded_at` in place makes the second merge skip the row (it looks taken over
// already) and this case fails on the audit count.
func TestSecondTakeoverAfterEnableRunsAgain(t *testing.T) {
	cr, _, path := callerAdminCore(t)
	original := readCallerCfg(t, path)
	_, err := cr.AddCaller(config.CallerSpec{ID: "ci", Ceiling: &config.CallerCeiling{Projects: []string{"self"}}}, "operator")
	assert.NoErr(t, err)
	perm := authz.Seeded(authz.SeedV1)[0].Name

	declareCallerInFile(t, cr, path, "ci", "file-token")
	first, _, err := cr.Store.GetStoredCaller("ci")
	assert.NoErr(t, err)
	assert.True(t, first.SupersededAt != 0, "the first takeover stamped the row")

	// The row comes back the only way it may: the file entry is gone, then rotate, then enable.
	writeCallerCfg(t, path, original)
	_, err = cr.ReloadConfigReport()
	assert.NoErr(t, err)
	_, err = cr.RotateCaller("ci", "operator")
	assert.NoErr(t, err)
	_, err = cr.EnableCaller("ci", "operator")
	assert.NoErr(t, err)
	row, _, err := cr.Store.GetStoredCaller("ci")
	assert.NoErr(t, err)
	assert.Eq(t, int64(0), row.SupersededAt, "enabling cleared the takeover mark")

	// A grant the caller holds while it is back in force.
	grant, err := cr.Store.CreateAuthGrant(jobstore.AuthGrant{Subject: "caller:ci", Pattern: perm})
	assert.NoErr(t, err)
	beforeSecond := callerAuditCount(t, cr, "ci")

	// The file declares the id AGAIN: a second takeover must run as if it were the first.
	declareCallerInFile(t, cr, path, "ci", "file-token-2")
	second, _, err := cr.Store.GetStoredCaller("ci")
	assert.NoErr(t, err)
	assert.True(t, second.SupersededAt != 0, "the second takeover stamped the row again")
	assert.True(t, second.DisabledAt != 0, "the second takeover disabled the row again")
	assert.Eq(t, beforeSecond+1, callerAuditCount(t, cr, "ci"),
		"the second takeover writes exactly one more audit line")
	stored, ok, err := cr.Store.GetAuthGrant(grant.ID)
	assert.NoErr(t, err)
	assert.True(t, ok && stored.OrphanedAt != 0, "the second takeover orphans the caller's grants again")
	assert.Len(t, cr.Config().Server.StoredCallerIDs(), 0, "the taken-over row is hidden again")
}

// readCallerCfg reads the fixture's configuration file.
func readCallerCfg(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	assert.NoErr(t, err)
	return string(raw)
}

// writeCallerCfg replaces the fixture's configuration file.
func writeCallerCfg(t *testing.T, path, text string) {
	t.Helper()
	assert.NoErr(t, os.WriteFile(path, []byte(text), 0o600))
}

// declareCallerInFile writes a caller entry into the configuration file and RELOADS: the merge
// that hides a same-id stored row runs on the reload path, not on a write transaction.
func declareCallerInFile(t *testing.T, cr *Core, path, id, token string) {
	t.Helper()
	text := readCallerCfg(t, path)
	entry := "  callers:\n  - id: " + id + "\n    token: " + token + "\n"
	declared := strings.Replace(text, "  callers:\n", entry, 1)
	assert.True(t, declared != text, "the fixture has a server.callers block to extend")
	writeCallerCfg(t, path, declared)
	_, err := cr.ReloadConfigReport()
	assert.NoErr(t, err)
}

// callerAuditCount counts the audit lines of one caller (every write of it adds one).
func callerAuditCount(t *testing.T, cr *Core, id string) int {
	t.Helper()
	entries, err := cr.Store.ListConfigAudit(callerAuditPath+id, 0)
	assert.NoErr(t, err)
	return len(entries)
}

// TestSetCallerCeilingAndLimitsTakeTheRightPath: a stored caller's limits are written in its
// row (no file write at all), a file caller's in the configuration file through Core.Update
// (D12) — and the quota of a caller is live right away.
func TestSetCallerCeilingAndLimitsTakeTheRightPath(t *testing.T) {
	cr, _, path := callerAdminCore(t)
	_, err := cr.AddCaller(config.CallerSpec{ID: "ci", NoCeiling: true}, "operator")
	assert.NoErr(t, err)

	// A ceiling cannot apply to a root administrator (the loader refuses it), so the file-side
	// half of this test needs a plain file caller.
	assert.NoErr(t, cr.Update(func(next *config.Config) error {
		next.Server.Callers = append(next.Server.Callers, config.CallerConfig{ID: "alice", Token: "tok-alice"})
		return nil
	}))

	before, err := os.ReadFile(path)
	assert.NoErr(t, err)
	_, err = cr.SetCallerCeiling("ci", nil, true, "operator")
	assert.NoErr(t, err)
	_, err = cr.SetCallerLimits("ci", config.CallerLimits{MaxConcurrentJobs: 2, RateLimit: 1.5, RateBurst: 3}, "operator")
	assert.NoErr(t, err)
	after, err := os.ReadFile(path)
	assert.NoErr(t, err)
	assert.Eq(t, string(before), string(after), "a stored caller's limits never touch the configuration file")
	ceil, ok := cr.Config().Server.CallerCeiling("ci")
	assert.False(t, ok && ceil != nil, "no ceiling stays no ceiling")
	entry := storedEntryOf(t, cr, "ci")
	assert.Eq(t, 2, entry.MaxConcurrentJobs)
	assert.Eq(t, 1.5, entry.RateLimit)

	// Setting a ceiling adds it to the same entry, live.
	_, err = cr.SetCallerCeiling("ci", &config.CallerCeiling{Projects: []string{"self"}}, false, "operator")
	assert.NoErr(t, err)
	ceil, ok = cr.Config().Server.CallerCeiling("ci")
	assert.True(t, ok && ceil != nil, "the ceiling is in the live snapshot")
	assert.Eq(t, []string{"self"}, ceil.Projects)

	// A file caller's ceiling goes to the file (and needs a full validation of the result).
	_, err = cr.SetCallerCeiling("alice", &config.CallerCeiling{Projects: []string{"self"}}, false, "operator")
	assert.NoErr(t, err)
	after, err = os.ReadFile(path)
	assert.NoErr(t, err)
	assert.True(t, string(after) != string(before), "a file caller's ceiling is written to the file")
	assert.True(t, strings.Contains(string(after), "ceiling"), "the file carries the ceiling")

	// The audit of the file write happened after the save (D12).
	entries, err := cr.Store.ListConfigAudit(callerAuditPath+"alice", 0)
	assert.NoErr(t, err)
	assert.Len(t, entries, 1)
}

// storedEntryOf returns the live caller entry of id.
func storedEntryOf(t *testing.T, cr *Core, id string) config.CallerConfig {
	t.Helper()
	for _, cc := range cr.Config().Server.Callers {
		if cc.ID == id {
			return cc
		}
	}
	t.Fatalf("caller %q is not in the live snapshot", id)
	return config.CallerConfig{}
}

// TestListCallersShowsEveryState: the list is the one place an operator can see a caller the
// snapshot hides (a disabled one, and one a file entry took over), with its source and hint.
func TestListCallersShowsEveryState(t *testing.T) {
	cr, _, _ := callerAdminCore(t)
	_, err := cr.AddCaller(config.CallerSpec{ID: "ci", Ceiling: &config.CallerCeiling{Projects: []string{"self"}}}, "operator")
	assert.NoErr(t, err)
	_, err = cr.AddCaller(config.CallerSpec{ID: "gone", NoCeiling: true}, "operator")
	assert.NoErr(t, err)
	_, err = cr.DisableCaller("gone", "operator")
	assert.NoErr(t, err)

	views, err := cr.ListCallers()
	assert.NoErr(t, err)
	byID := map[string]config.CallerView{}
	for _, v := range views {
		byID[v.ID] = v
	}
	assert.Eq(t, config.CallerSourceFile, byID["operator"].Source)
	assert.True(t, byID["operator"].TokenSet)
	assert.Eq(t, config.CallerStateActive, byID["ci"].State)
	assert.Eq(t, config.CallerSourceDB, byID["ci"].Source)
	assert.Eq(t, []string{"self"}, byID["ci"].Ceiling.Projects)
	assert.Eq(t, config.CallerStateDisabled, byID["gone"].State)
	assert.True(t, byID["gone"].Ceiling == nil, "an unlimited caller reports no ceiling")
	// No ceiling + governance off: the caller can still edit agents.*.command, and is told so.
	found := false
	for _, w := range byID["gone"].Warnings {
		if strings.Contains(w, "no ceiling") {
			found = true
		}
	}
	assert.True(t, found, "an unlimited caller carries the warning: %v", byID["gone"].Warnings)

	audit, err := cr.ListCallerAudit(0)
	assert.NoErr(t, err)
	assert.True(t, len(audit) >= 3, "every write left a line: %+v", audit)
}

// TestUpdateCallersRefusesToHideAnActiveRow: the re-merge of a write must not silently drop a
// caller that a NEW id collision hides (the review requirement); an already superseded or
// disabled row stays hidden without failing every later write.
func TestUpdateCallersRefusesToHideAnActiveRow(t *testing.T) {
	cr, _, _ := callerAdminCore(t)
	_, err := cr.AddCaller(config.CallerSpec{ID: "ci", Ceiling: &config.CallerCeiling{Projects: []string{"self"}}}, "operator")
	assert.NoErr(t, err)

	// A row still in the store but hidden by a file entry of the same id: writes keep working.
	assert.NoErr(t, cr.Store.WithCallerTx(func(tx jobstore.CallerTx) error {
		row, ok, err := tx.GetCaller("ci")
		if err != nil || !ok {
			return err
		}
		row.SupersededAt, row.DisabledAt = tx.Now(), tx.Now()
		return tx.UpdateCallerTx(row)
	}))
	_, err = cr.AddCaller(config.CallerSpec{ID: "other", NoCeiling: true}, "operator")
	assert.NoErr(t, err, "an already superseded row must not block later writes")

	// A file entry appears in memory only (as the write clone sees it): the merge of the write
	// would hide the ACTIVE row — that is refused instead of silently losing the caller.
	active, err := cr.AddCaller(config.CallerSpec{ID: "live", NoCeiling: true}, "operator")
	assert.NoErr(t, err)
	assert.True(t, active.Caller.ID == "live")
	_, err = cr.UpdateCallers(func(next *config.Config, _ jobstore.CallerTx) error {
		next.Server.Callers = append(next.Server.Callers, config.CallerConfig{ID: "live", Token: "file-token"})
		return nil
	})
	assert.ErrIs(t, err, config.ErrCallerIDTaken)
}
