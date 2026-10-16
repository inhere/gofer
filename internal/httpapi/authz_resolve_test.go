package httpapi

import (
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// 有效权限解析器的测试（设计 §2「有效权限」、§3「配置推出的授权」与「数据库种入」，计划 T1.4）：
//
//   - 根管理员拥有目录里的全部权限；
//   - 其他调用方 = 配置推出 ∪ 数据库里 subject=users 的授权（来源分别是 config / db）；
//   - 其他主体（caller: / session:）与失效的授权行不参与；
//   - 种入之后，非根调用方照旧能提交 job（设计「有意不保持现状」之外的路径不变）。

// TestEffectiveRootHoldsEverything: the root administrator holds every catalog permission,
// every one of them sourced `root`, including auth.manage — which no derived or database
// grant can ever reach.
func TestEffectiveRootHoldsEverything(t *testing.T) {
	t.Parallel()
	// The single-token deployment: `default` is the root (design §2).
	s := newTestServerCfg(t, config.ServerConfig{Token: "legacy"})
	d := s.effective(authz.DefaultCallerID, "")
	if !d.Root() {
		t.Fatalf("%s must be a root administrator of a single-token deployment", authz.DefaultCallerID)
	}
	if got, want := d.Perms(), catalogNames(); !slices.Equal(got, want) {
		t.Errorf("root permissions = %v, want the whole catalog", got)
	}
	if got := d.Source(authz.PermAuthManage); got != sourceRoot {
		t.Errorf("auth.manage source = %q, want %q", got, sourceRoot)
	}
	if d := s.effective("alice", ""); d.Root() {
		t.Error("an undeclared caller must not be a root administrator")
	}
}

// TestEffectiveNonRootUnionsConfigAndDatabase: a non-root caller's permissions are the
// config-derived grants (design §3's truth table) plus the `users` grants of the database
// (the v1 seed), each permission tagged with the half it came from.
func TestEffectiveNonRootUnionsConfigAndDatabase(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, config.ServerConfig{Token: "legacy", Callers: []config.CallerConfig{
		{ID: "alice", Token: "tok-alice"},
		{ID: "bob", Token: "tok-bob"},
	}})
	d := s.effective("alice", "")
	if d.Root() {
		t.Fatal("alice declares no can_admin, so she is not a root administrator")
	}
	if d.CallerID() != "alice" {
		t.Errorf("caller id = %q, want alice", d.CallerID())
	}
	// Config-derived: every gate is closed, so the admin group reaches the `users` subject.
	if got := d.Source(authz.PermConfigWrite); got != sourceConfig {
		t.Errorf("%s source = %q, want %q", authz.PermConfigWrite, got, sourceConfig)
	}
	// Database: the v1 seed.
	if got := d.Source(authz.PermJobSubmit); got != sourceDB {
		t.Errorf("%s source = %q, want %q", authz.PermJobSubmit, got, sourceDB)
	}
	if !slices.Contains(d.GrantIDs(), "seed:users:"+authz.PermJobSubmit) {
		t.Errorf("the decision does not depend on the seeded grant: %v", d.GrantIDs())
	}
	// Root-only permissions are held by nobody else, whatever the switches say.
	for _, p := range []string{authz.PermAuthManage, authz.PermJobDeleteOthers, authz.PermXferManageOthers} {
		if d.Has(p) {
			t.Errorf("a non-root caller must not hold %s", p)
		}
		if got := d.Source(p); got != "" {
			t.Errorf("%s source = %q, want empty", p, got)
		}
	}
	// The seed reaches every user-class caller, alice and bob alike.
	if !s.effective("bob", "").Has(authz.PermJobSubmit) {
		t.Error("the `users` grants must reach every user-class caller")
	}
}

// TestUserGrantsIgnoreOtherSubjectsAndDeadRows: the pure expansion of ONE subject keeps the
// batch-1 contract — rows of another subject (`caller:<id>` / `session:<sid>`) contribute
// nothing to it, and neither does a revoked, orphaned, expired or no-longer-matching row. A `*`
// grant of another subject must not leak either. (The other two subjects have their own
// expansions: expandGrantsFor per caller, and sessionGrantScope per request.)
func TestUserGrantsIgnoreOtherSubjectsAndDeadRows(t *testing.T) {
	t.Parallel()
	now := time.Now().Unix()
	rows := []jobstore.AuthGrant{
		{ID: "keep", Subject: jobstore.SubjectUsers, Pattern: authz.PermJobSubmit},
		{ID: "wildcard", Subject: jobstore.SubjectUsers, Pattern: "xfer.*"},
		{ID: "caller-subject", Subject: "caller:alice", Pattern: "*"},
		{ID: "session-subject", Subject: "session:s1", Pattern: "*"},
		{ID: "revoked", Subject: jobstore.SubjectUsers, Pattern: authz.PermXferWrite, RevokedAt: now},
		{ID: "orphaned", Subject: jobstore.SubjectUsers, Pattern: authz.PermXferWrite, OrphanedAt: now},
		{ID: "expired", Subject: jobstore.SubjectUsers, Pattern: authz.PermTrackerSync, ExpiresAt: now - 1},
		{ID: "unknown-pattern", Subject: jobstore.SubjectUsers, Pattern: "not.a.permission"},
	}
	g := userGrantsFrom(rows, now)
	want := []string{authz.PermJobSubmit, authz.PermXferManageOthers}
	if !slices.Equal(g.perms, want) {
		t.Fatalf("expanded `users` permissions = %v, want %v", g.perms, want)
	}
	if got := g.grants[authz.PermJobSubmit]; got != "keep" {
		t.Errorf("job.submit came from grant %q, want keep", got)
	}
	if got := g.grants[authz.PermXferManageOthers]; got != "wildcard" {
		t.Errorf("xfer.manage.others came from grant %q, want wildcard", got)
	}
	// The wildcard grant is a `users` row, so its expansion obeys the catalog's safety
	// rules: a dangerous permission behind a wildcard stays out (design §2「展开规则」).
	if _, ok := g.grants[authz.PermXferWrite]; ok {
		t.Error("a wildcard grant must not reach a dangerous permission")
	}
}

// TestCanIsTrueForNonUserCallers: a worker token and a job credential are not governed by
// permissions — their own tables are (design §2: 「只对 user 类调用方计算」) — so can does
// not refuse them; the design's JobCred / WorkerSelf annotations never add a second gate.
func TestCanIsTrueForNonUserCallers(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, config.ServerConfig{Token: "legacy"})
	for _, kind := range []string{callerKindWorker, callerKindJob} {
		rec := serveProbe(t, s, kind, routeDecl{user: Require(authz.PermAuthManage)})
		if rec.Code != http.StatusOK {
			t.Errorf("a %s caller was refused by a permission gate: status %d", kind, rec.Code)
		}
	}
}

// TestSeededNonRootCallerMaySubmitJob is the acceptance case of plan T1.4: with the v1 seed
// in place, a non-root caller still submits a job (design §3: the seed keeps today's
// behaviour), so the permission model has not narrowed anything it did not mean to.
func TestSeededNonRootCallerMaySubmitJob(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, config.ServerConfig{Callers: []config.CallerConfig{
		{ID: "alice", Token: "tok-alice"},
		{ID: "bob", Token: "tok-bob"},
	}})
	if s.rootAdmin("alice") {
		t.Fatal("alice declares no can_admin, so she must not be a root administrator")
	}
	if d := s.effective("alice", ""); !d.Has(authz.PermJobSubmit) {
		t.Fatalf("alice does not hold the seeded %s: %v", authz.PermJobSubmit, d.Perms())
	}

	created, status := createJob(t, s, "tok-alice")
	if status != http.StatusOK {
		t.Fatalf("POST /v1/jobs as a non-root caller: status = %d, want 200", status)
	}
	if created.ID == "" {
		t.Fatal("the created job has no id")
	}
	final := waitDoneTok(t, s, created.ID, "tok-alice")
	if final.CallerID != "alice" {
		t.Errorf("persisted caller_id = %q, want alice", final.CallerID)
	}
}

// catalogNames returns the whole catalog's permission names, in design order.
func catalogNames() []string {
	perms := authz.Permissions()
	out := make([]string, 0, len(perms))
	for _, p := range perms {
		out = append(out, p.Name)
	}
	return out
}
