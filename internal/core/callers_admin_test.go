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
	assert.Err(t, err, config.ErrCallerInvalid) // an empty ceiling is not a ceiling (D16)

	_, err = cr.AddCaller(config.CallerSpec{ID: "noc"}, "operator")
	assert.Err(t, err, config.ErrCallerInvalid) // no ceiling and no no_ceiling

	_, err = cr.AddCaller(config.CallerSpec{ID: "default", NoCeiling: true}, "operator")
	assert.Err(t, err, config.ErrCallerInvalid) // reserved

	_, err = cr.AddCaller(config.CallerSpec{ID: "operator", NoCeiling: true}, "operator")
	assert.Err(t, err, config.ErrCallerIDTaken) // a file caller's id

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
	assert.Err(t, err, config.ErrCallerIDTaken)

	// A disabled (retired) row keeps its id.
	_, err = cr.AddCaller(config.CallerSpec{ID: "old", NoCeiling: true}, "operator")
	assert.NoErr(t, err)
	_, err = cr.DisableCaller("old", "operator")
	assert.NoErr(t, err)
	_, err = cr.AddCaller(config.CallerSpec{ID: "old", NoCeiling: true}, "operator")
	assert.Err(t, err, config.ErrCallerIDTaken)
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
		assert.Err(t, call(), config.ErrCallerFileSource, name)
	}
	_, err = cr.RotateCaller("nobody", "operator")
	assert.Err(t, err, config.ErrCallerNotFound)

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

// TestEnableTakenOverCallerNeedsARotation (D10): the row a file entry took over may only come
// back after a rotation, and enabling clears both marks so a second takeover runs again.
func TestEnableTakenOverCallerNeedsARotation(t *testing.T) {
	cr, _, path := callerAdminCore(t)
	_, err := cr.AddCaller(config.CallerSpec{ID: "ci", Ceiling: &config.CallerCeiling{Projects: []string{"self"}}}, "operator")
	assert.NoErr(t, err)

	// The file declares the same id, and a reload merges it.
	text, err := os.ReadFile(path)
	assert.NoErr(t, err)
	declared := strings.Replace(string(text), "  callers:\n  - id: operator",
		"  callers:\n  - id: ci\n    token: file-token\n  - id: operator", 1)
	assert.True(t, declared != string(text), "the fixture gained the same-id entry")
	assert.NoErr(t, os.WriteFile(path, []byte(declared), 0o600))
	_, err = cr.ReloadConfigReport()
	assert.NoErr(t, err)

	_, err = cr.EnableCaller("ci", "operator")
	assert.Err(t, err, config.ErrCallerRotationRequired)

	// The file entry goes away; a rotation makes the row usable again.
	assert.NoErr(t, os.WriteFile(path, text, 0o600))
	_, err = cr.ReloadConfigReport()
	assert.NoErr(t, err)
	_, err = cr.RotateCaller("ci", "operator")
	assert.NoErr(t, err)
	on, err := cr.EnableCaller("ci", "operator")
	assert.NoErr(t, err)
	assert.Eq(t, config.CallerStateActive, on.Caller.State)
	row, _, err := cr.Store.GetStoredCaller("ci")
	assert.NoErr(t, err)
	assert.Eq(t, int64(0), row.SupersededAt, "enabling clears the takeover mark")
	assert.Eq(t, int64(0), row.DisabledAt)
	assert.True(t, len(cr.Config().Server.StoredCallerIDs()) == 1, "the enabled row is in the snapshot again")
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
	assert.Err(t, err, config.ErrCallerIDTaken)
}
