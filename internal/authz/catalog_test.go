package authz

import (
	"slices"
	"testing"
)

// designRow is one row of the design table (§1), typed. Column order matches the table:
// 权限, 风险, 授予会话, 范围, 来源, 种入版本.
type designRow struct {
	name        string
	risk        Risk
	session     bool
	scope       Scope
	source      Source
	seedVersion int
}

// designTable freezes design §1 row by row, in the order of the design table (minus the
// explanatory `pty.recordings` row, which is not a permission). Changing the catalog
// means changing this table too — that is the point of the test below.
var designTable = []designRow{
	{PermJobReadOthers, RiskMedium, true, ScopeYes, SourceSeed, SeedV1},
	{PermJobSubmit, RiskDangerous, false, ScopeYes, SourceSeed, SeedV1},
	{PermJobCancelOthers, RiskMedium, true, ScopeYes, SourceSeed, SeedV1},
	{PermJobManageOthers, RiskHigh, false, ScopeYes, SourceSeed, SeedV1},
	{PermJobDeleteOthers, RiskHigh, false, ScopeYes, SourceRoot, NotSeeded},
	{PermJobAttach, RiskMedium, true, ScopeYes, SourceConfig, NotSeeded},
	{PermJobAttachOthers, RiskHigh, false, ScopeYes, SourceRoot, NotSeeded},
	{PermJobReview, RiskMedium, false, ScopeYes, SourceConfig, NotSeeded},
	{PermJobWakeOthers, RiskMedium, true, ScopeYes, SourceConfig, NotSeeded},
	{PermInteractionAnswer, RiskMedium, true, ScopeYes, SourceConfig, NotSeeded},
	{PermSessionReadOthers, RiskMedium, true, ScopeNo, SourceSeed, SeedV1},
	{PermSessionReplyOthers, RiskHigh, true, ScopeNo, SourceConfig, NotSeeded},
	{PermSessionManageOthers, RiskMedium, false, ScopeNo, SourceSeed, SeedV1},
	{PermSessionPermissionAnswer, RiskHigh, false, ScopeNo, SourceRoot, NotSeeded},
	{PermSessionTransfer, RiskHigh, false, ScopeNo, SourceRoot, NotSeeded},
	{PermPlanManageOthers, RiskMedium, false, ScopeYes, SourceSeed, SeedV1},
	{PermPlanSuperviseOthers, RiskMedium, false, ScopeYes, SourceRoot, NotSeeded},
	{PermScheduleManageOthers, RiskMedium, false, ScopeYes, SourceSeed, SeedV1},
	{PermStewardManage, RiskHigh, false, ScopeNo, SourceSeed, SeedV1},
	{PermMemoryWrite, RiskDangerous, false, ScopeYes, SourceSeed, SeedV1},
	{PermTrackerSync, RiskLow, false, ScopeYes, SourceSeed, SeedV1},
	{PermTrackerDelete, RiskHigh, false, ScopeYes, SourceConfig, NotSeeded},
	{PermTunnelOpen, RiskMedium, true, ScopePhase2, SourceConfig, NotSeeded},
	{PermTunnelHosted, RiskHigh, false, ScopeNo, SourceSeed, SeedV1},
	{PermTunnelManageOthers, RiskHigh, false, ScopeNo, SourceRoot, NotSeeded},
	{PermXferWrite, RiskDangerous, false, ScopeYes, SourceSeed, SeedV1},
	{PermXferManageOthers, RiskHigh, false, ScopeYes, SourceRoot, NotSeeded},
	{PermSecretScanAll, RiskHigh, false, ScopeNo, SourceRoot, NotSeeded},
	{PermUpdateCheck, RiskLow, true, ScopeNo, SourceSeed, SeedV1},
	{PermUpdateApply, RiskHigh, true, ScopeNo, SourceConfig, NotSeeded},
	{PermWorkerUpgrade, RiskHigh, true, ScopeNo, SourceConfig, NotSeeded},
	{PermWorkerUpgradeUpload, RiskDangerous, false, ScopeNo, SourceConfig, NotSeeded},
	{PermWorkerManage, RiskDangerous, false, ScopeNo, SourceConfig, NotSeeded},
	{PermConfigRead, RiskLow, true, ScopeNo, SourceSeed, SeedV1},
	{PermMcpTest, RiskMedium, false, ScopeNo, SourceSeed, SeedV1},
	{PermConfigWrite, RiskDangerous, false, ScopeNo, SourceConfig, NotSeeded},
	{PermProjectWrite, RiskDangerous, false, ScopeNo, SourceConfig, NotSeeded},
	{PermRulesWrite, RiskDangerous, false, ScopeNo, SourceConfig, NotSeeded},
	{PermSkillsWrite, RiskDangerous, false, ScopeNo, SourceConfig, NotSeeded},
	{PermAuthManage, RiskRoot, false, ScopeNo, SourceRoot, NotSeeded},
}

// addedRows are the catalog rows that came after design §1 was frozen, each pinned with the
// design row it follows in the catalog (the jobs group stays together in the generated
// reference). `job.review.others` is the 审阅 batch of
// docs/design/2026-10-18-caller-scope-ceilings-design.md: 中风险, ScopeYes, seeded to
// `users` in SeedV2 so an upgrade keeps today's behaviour.
var addedRows = []struct {
	after string
	row   designRow
}{
	{PermJobReview, designRow{PermJobReviewOthers, RiskMedium, false, ScopeYes, SourceSeed, SeedV2}},
}

// expectedCatalog returns the design table with addedRows spliced in where the catalog puts
// them: design order, plus the later additions after the row they must stay next to.
func expectedCatalog() []designRow {
	out := make([]designRow, 0, len(designTable)+len(addedRows))
	for _, row := range designTable {
		out = append(out, row)
		for _, add := range addedRows {
			if add.after == row.name {
				out = append(out, add.row)
			}
		}
	}
	return out
}

// TestCatalogMatchesDesignTable pins the catalog to the design table: same names, same
// order, and every column equal, plus every later addition where expectedCatalog puts it.
// It is the test the plan calls「目录逐行与设计一致」.
func TestCatalogMatchesDesignTable(t *testing.T) {
	want := expectedCatalog()
	got := Permissions()
	if len(got) != len(want) {
		t.Fatalf("catalog has %d rows, the frozen design table plus the later additions has %d", len(got), len(want))
	}
	seen := make(map[string]struct{}, len(got))
	for i, expected := range want {
		p := got[i]
		if p.Name != expected.name {
			t.Errorf("row %d: name = %q, want %q — the catalog must keep the design table order", i+1, p.Name, expected.name)
		}
		if _, dup := seen[p.Name]; dup {
			t.Errorf("row %d: permission %q appears more than once", i+1, p.Name)
		}
		seen[p.Name] = struct{}{}
		if p.Risk != expected.risk {
			t.Errorf("%s: risk = %s, want %s", p.Name, p.Risk, expected.risk)
		}
		if p.SessionGrantable != expected.session {
			t.Errorf("%s: session-grantable = %t, want %t", p.Name, p.SessionGrantable, expected.session)
		}
		if p.Scope != expected.scope {
			t.Errorf("%s: scope = %s, want %s", p.Name, p.Scope, expected.scope)
		}
		if p.Source != expected.source {
			t.Errorf("%s: source = %s, want %s", p.Name, p.Source, expected.source)
		}
		if p.SeedVersion != expected.seedVersion {
			t.Errorf("%s: seed version = %d, want %d", p.Name, p.SeedVersion, expected.seedVersion)
		}
		if p.Description == "" {
			t.Errorf("%s: description is empty", p.Name)
		}
		if err := p.Validate(); err != nil {
			t.Errorf("%s: %v", p.Name, err)
		}
	}
}

// TestPermissionConstantsMatchCatalog makes the name constants the single spelling of
// the catalog: every constant is in the catalog, in the same order, and no two
// constants share a value.
func TestPermissionConstantsMatchCatalog(t *testing.T) {
	constNames := []string{
		PermJobReadOthers, PermJobSubmit, PermJobCancelOthers, PermJobManageOthers,
		PermJobDeleteOthers, PermJobAttach, PermJobAttachOthers, PermJobReview,
		PermJobReviewOthers, PermJobWakeOthers, PermInteractionAnswer,
		PermSessionReadOthers, PermSessionReplyOthers, PermSessionManageOthers,
		PermSessionPermissionAnswer, PermSessionTransfer,
		PermPlanManageOthers, PermPlanSuperviseOthers, PermScheduleManageOthers,
		PermStewardManage, PermMemoryWrite, PermTrackerSync, PermTrackerDelete,
		PermTunnelOpen, PermTunnelHosted, PermTunnelManageOthers,
		PermXferWrite, PermXferManageOthers, PermSecretScanAll,
		PermUpdateCheck, PermUpdateApply, PermWorkerUpgrade, PermWorkerUpgradeUpload,
		PermWorkerManage, PermConfigRead, PermMcpTest, PermConfigWrite, PermProjectWrite,
		PermRulesWrite, PermSkillsWrite, PermAuthManage,
	}
	if len(constNames) != len(expectedCatalog()) {
		t.Fatalf("the constant list has %d names, the catalog has %d", len(constNames), len(expectedCatalog()))
	}
	if got := allNames(); !slices.Equal(constNames, got) {
		t.Errorf("permission constants and catalog differ:\n constants: %v\n catalog:   %v", constNames, got)
	}
}

// TestLookupAndCatalogIsFrozen checks the read paths: Lookup finds every catalog entry
// and nothing else, and Permissions hands out a copy (a caller sorting or editing the
// result must not corrupt the catalog for the next caller).
func TestLookupAndCatalogIsFrozen(t *testing.T) {
	for _, p := range Permissions() {
		got, ok := Lookup(p.Name)
		if !ok {
			t.Errorf("Lookup(%q): not found", p.Name)
			continue
		}
		if got != p {
			t.Errorf("Lookup(%q) = %+v, want %+v", p.Name, got, p)
		}
	}
	for _, name := range []string{"", "job", "job.nope", "auth.Manage", PermJobSubmit + " "} {
		if _, ok := Lookup(name); ok {
			t.Errorf("Lookup(%q): unexpectedly found", name)
		}
	}
	list := Permissions()
	list[0].Name = "mutated"
	list = append(list, Permission{Name: "extra"})
	if Permissions()[0].Name != PermJobReadOthers || len(Permissions()) != len(expectedCatalog()) {
		t.Fatalf("Permissions() leaks the catalog: %q, %d entries", Permissions()[0].Name, len(Permissions()))
	}
}

// TestSeededIsTheDesignSeedSet checks the seeding contract used by the migration: seed
// version 1 is exactly the 16 permissions of design §3, seed version 2 is the one permission
// added for 审阅 afterwards, only seeded rows carry a seed version, and no later version has
// anything to plant yet.
func TestSeededIsTheDesignSeedSet(t *testing.T) {
	want := []string{
		PermJobReadOthers, PermJobSubmit, PermJobCancelOthers, PermJobManageOthers,
		PermSessionReadOthers, PermSessionManageOthers, PermPlanManageOthers,
		PermScheduleManageOthers, PermStewardManage, PermMemoryWrite, PermTrackerSync,
		PermTunnelHosted, PermXferWrite, PermConfigRead, PermUpdateCheck, PermMcpTest,
	}
	got := Seeded(SeedV1)
	if len(got) != 16 {
		t.Fatalf("seed version 1 plants %d permissions, design §3 lists 16", len(got))
	}
	names := make([]string, 0, len(got))
	for _, p := range got {
		names = append(names, p.Name)
		if p.Source != SourceSeed {
			t.Errorf("%s: seeded but its source is %s", p.Name, p.Source)
		}
	}
	// Compare as sets: the design fixes the seeded group, its listing order is free.
	slices.Sort(names)
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Errorf("seed version 1 = %v, want %v", names, want)
	}
	for _, p := range Permissions() {
		seeded := p.SeedVersion != NotSeeded
		if seeded != (p.Source == SourceSeed) {
			t.Errorf("%s: seed version %d with source %s — only a seeded permission carries a version, and every seeded one must", p.Name, p.SeedVersion, p.Source)
		}
	}
	if got := Seeded(NotSeeded); got != nil {
		t.Errorf("Seeded(NotSeeded) = %v, want nothing", got)
	}
	// SeedV2 is the batch added after the design's seed was frozen: exactly the permissions
	// that keep an upgrade behaviour-compatible, one row here.
	v2 := Seeded(SeedV2)
	if len(v2) != 1 || v2[0].Name != PermJobReviewOthers {
		t.Errorf("seed version 2 = %v, want only %s", v2, PermJobReviewOthers)
	}
	if got := Seeded(SeedV2 + 1); len(got) != 0 {
		t.Errorf("Seeded(%d) = %v, want nothing", SeedV2+1, got)
	}
}

// TestCatalogRiskInvariants checks the invariants the expansion rules lean on:
// auth.manage is the only root-risk permission (so「任何授权都匹配不到 auth.manage」covers
// every root-risk entry), nothing dangerous may be granted to a session, and no
// root-only permission is session-grantable either.
func TestCatalogRiskInvariants(t *testing.T) {
	root := catalogNames(func(p Permission) bool { return p.Risk == RiskRoot })
	if !slices.Equal(root, []string{PermAuthManage}) {
		t.Errorf("root-risk permissions = %v, want only %s", root, PermAuthManage)
	}
	if p, _ := Lookup(PermAuthManage); p.SessionGrantable {
		t.Errorf("%s must not be grantable to a session", PermAuthManage)
	}
	for _, p := range Permissions() {
		if p.Risk == RiskDangerous && p.SessionGrantable {
			t.Errorf("%s is dangerous and session-grantable — design §1 forbids that", p.Name)
		}
		if p.Source == SourceRoot && p.SessionGrantable {
			t.Errorf("%s belongs to the root administrator only, so it cannot be a session grant", p.Name)
		}
	}
}

// allNames returns the catalog names in design order.
func allNames() []string {
	out := make([]string, 0, len(catalog))
	for _, p := range catalog {
		out = append(out, p.Name)
	}
	return out
}

// catalogNames returns the names of the catalog entries matching pred, in design order.
func catalogNames(pred func(Permission) bool) []string {
	var out []string
	for _, p := range catalog {
		if pred(p) {
			out = append(out, p.Name)
		}
	}
	return out
}

// dangerousNames returns the dangerous permissions of the catalog, in design order.
func dangerousNames() []string {
	return catalogNames(func(p Permission) bool { return p.Risk == RiskDangerous })
}

// sessionGrantableNames returns the session-grantable permissions, in design order.
func sessionGrantableNames() []string {
	return catalogNames(func(p Permission) bool { return p.SessionGrantable })
}
