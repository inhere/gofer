// 有效权限的解析（设计 docs/design/2026-10-14-db-capabilities-and-session-grants-design.md
// §2「授权模型与判定」（「有效权限」）、§3「配置推出的授权（真值表）」，计划 T1.4）。
//
// ONLY a user-class caller has effective permissions. A worker token and a job credential
// are confined by their own route tables (workercredential.go / jobcredential.go), which
// stay authoritative, so nothing here applies to them.
//
// A user-class caller's effective permissions are the union of three parts (design §2):
//
//   - the root administrator: every permission in the catalog;
//   - the config-derived grants (`source: config`, design §3's truth table; recomputed on
//     every configuration load, read-only in the UI, no stable id);
//   - the database grants of the subject `users` that are not revoked (`source: db`).
//     The first batch loads them ONCE at startup (plan T1.4: 「启动时加载一次即可，第一批
//     没有写接口」); the write interface and the versioned cache arrive with T2.1.
//
// The subjects `caller:<id>` and `session:<sid>` — written by the second batch — and any
// column this batch does not know are IGNORED, so rolling the second batch back to this one
// never reads a grant it did not come from (plan T1.4: 「忽略 caller: / session: 与不认识
// 的字段，这样第二批回滚到第一批时不会误读」).
//
// The one decision path future tasks use is can(): T1.5a–i replace the ~30 hand-written
// caller checks of the handlers with it, and the calling-assertion test (design 「调用断言」)
// sees exactly the permissions a request decided.

package httpapi

import (
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/jobstore"
)

// Sources a permission of a decision can come from (design §2: `source config | db`, plus
// the root administrator's everything and the seed, which is a database row as well).
const (
	// sourceRoot: the caller is a root administrator, which holds every permission.
	sourceRoot = "root"
	// sourceConfig: derived from the old capability bits / governance switches (design §3).
	sourceConfig = "config"
	// sourceDB: a grant row of the subject `users`.
	sourceDB = "db"
)

// ctxAuthzDecision is the rux context key under which ONE request's decision is cached:
// a request takes one configuration snapshot and resolves its permissions once, no matter
// how many times the handlers ask (design §3「一致性」).
const ctxAuthzDecision = "authz_decision"

// decision is the resolved effective permission set of one user-class caller: the
// permission names, where each came from, and the database grants it rests on.
type decision struct {
	callerID string
	root     bool
	perms    map[string]struct{}
	sources  map[string]string
	grants   map[string]string // permission -> the database grant id that gave it
}

// newDecision returns an empty decision for callerID.
func newDecision(callerID string) *decision {
	return &decision{
		callerID: callerID,
		perms:    map[string]struct{}{},
		sources:  map[string]string{},
		grants:   map[string]string{},
	}
}

// Has reports whether the caller holds perm.
func (d *decision) Has(perm string) bool {
	if d == nil {
		return false
	}
	_, ok := d.perms[perm]
	return ok
}

// Root reports whether the caller is a root administrator (it holds everything).
func (d *decision) Root() bool { return d != nil && d.root }

// CallerID is the caller the decision was resolved for.
func (d *decision) CallerID() string {
	if d == nil {
		return ""
	}
	return d.callerID
}

// Source names where perm came from (sourceRoot / sourceConfig / sourceDB), or "" when the
// caller does not hold it. When both the configuration and a database grant grant it, the
// configuration is reported: that floor holds whatever the database says.
func (d *decision) Source(perm string) string {
	if d == nil {
		return ""
	}
	return d.sources[perm]
}

// Perms returns the held permission names in catalog order (design §1 order), so callers
// and tests see one canonical order.
func (d *decision) Perms() []string {
	if d == nil {
		return nil
	}
	out := make([]string, 0, len(d.perms))
	for _, p := range authz.Permissions() {
		if _, ok := d.perms[p.Name]; ok {
			out = append(out, p.Name)
		}
	}
	return out
}

// GrantIDs returns the database grant ids the decision rests on, deduped and sorted. It is
// the dependency set of design §6「长连接的撤销」 (T2.3 registers long connections against
// it) — the config-derived part has no stable id, which is why T2.3 compares callers across
// a reload instead.
func (d *decision) GrantIDs() []string {
	if d == nil || len(d.grants) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(d.grants))
	out := make([]string, 0, len(d.grants))
	for _, id := range d.grants {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	// One order for the dependency set (T2.3 compares it across requests).
	sort.Strings(out)
	return out
}

// userGrants is the database half of a caller's effective permissions: the expanded
// permission names of the subject `users`, plus the grant each came from.
type userGrants struct {
	perms  []string
	grants map[string]string // permission -> grant id
}

// loadUserGrants reads the live grants of the subject `users` from the metadata store and
// expands them (design §2「展开规则」). It is called once at startup by New.
func loadUserGrants(st *jobstore.Store) (*userGrants, error) {
	if st == nil {
		return nil, nil
	}
	rows, err := st.ListActiveAuthGrants(jobstore.SubjectUsers)
	if err != nil {
		return nil, err
	}
	return userGrantsFrom(rows, time.Now().Unix()), nil
}

// userGrantsFrom expands grant rows for the `users` subject. It is a pure function so the
// rules below have one home and a test that needs no database:
//
//   - a row of another subject (`caller:<id>`, `session:<sid>` — the second batch's) is
//     ignored;
//   - a revoked, orphaned or expired row contributes nothing (the query already filters
//     the first two; the check is repeated here so the rule is visible and testable);
//   - a row whose pattern matches no permission any more (the catalog can shrink) is
//     ignored with a log line: it grants nothing, and failing the whole load because of it
//     would be worse;
//   - the expansion applies the safety rules of authz.Expand (no pattern ever reaches
//     auth.manage, a wildcard never reaches a dangerous permission).
func userGrantsFrom(rows []jobstore.AuthGrant, now int64) *userGrants {
	out := &userGrants{grants: map[string]string{}}
	for _, row := range rows {
		if row.Subject != jobstore.SubjectUsers {
			continue
		}
		if row.RevokedAt != 0 || row.OrphanedAt != 0 {
			continue
		}
		if row.ExpiresAt != 0 && row.ExpiresAt <= now {
			continue
		}
		exp, err := authz.Expand(row.Pattern, authz.SubjectUsers)
		if err != nil {
			slog.Warn("httpapi: ignoring an auth grant whose pattern matches no permission",
				"grant", row.ID, "pattern", row.Pattern, "err", err)
			continue
		}
		for _, p := range exp.Permissions {
			if _, dup := out.grants[p.Name]; dup {
				continue
			}
			out.grants[p.Name] = row.ID
		}
	}
	out.perms = make([]string, 0, len(out.grants))
	for _, p := range authz.Permissions() {
		if _, ok := out.grants[p.Name]; ok {
			out.perms = append(out.perms, p.Name)
		}
	}
	return out
}

// decisionOf returns the decision of THIS request, resolving it once and caching it in the
// request context. It is the single read point of the permission model for a request.
func (s *Server) decisionOf(c *rux.Context) *decision {
	if v, ok := c.Get(ctxAuthzDecision); ok {
		if d, ok := v.(*decision); ok && d != nil {
			return d
		}
	}
	d := s.effective(callerFromCtx(c))
	c.Set(ctxAuthzDecision, d)
	return d
}

// effective resolves the effective permissions of one user-class caller (design §2
// 「有效权限」): everything for a root administrator, otherwise the union of the
// config-derived grants and the `users` grants of the database.
func (s *Server) effective(callerID string) *decision {
	d := newDecision(callerID)
	derived := s.authzDerivation()
	if derived != nil && derived.IsRoot(callerID) {
		d.root = true
		for _, p := range authz.Permissions() {
			d.perms[p.Name] = struct{}{}
			d.sources[p.Name] = sourceRoot
		}
		return d
	}
	if derived != nil {
		for _, p := range derived.Effective(callerID) {
			d.perms[p] = struct{}{}
			d.sources[p] = sourceConfig
		}
	}
	if g := s.authzGrants.Load(); g != nil {
		for _, p := range g.perms {
			if _, ok := d.perms[p]; !ok {
				d.perms[p] = struct{}{}
				d.sources[p] = sourceDB
			}
			// The grant id is recorded even when the configuration grants the permission
			// as well: revoking the grant must be re-evaluated (design §6), and the
			// re-evaluation finds the config floor and keeps the connection.
			d.grants[p] = g.grants[p]
		}
	}
	return d
}

// can reports whether the caller of this request may perform an action that needs EVERY
// one of perms, and records each decision for the calling-assertion test.
//
// A caller that is not a user-class caller is not governed by permissions at all — a
// worker token and a job credential are confined by workercredential.go's whitelist and
// jobcredential.go's SEC-01 tables — so can reports true for them (and records the
// decision as taken, because it was: their own table answered it).
func (s *Server) can(c *rux.Context, perms ...string) bool {
	if callerKindFromCtx(c) != callerKindUser {
		for _, p := range perms {
			recordDecision(c, p, true)
		}
		return true
	}
	d := s.decisionOf(c)
	ok := true
	for _, p := range perms {
		held := d.Has(p)
		recordDecision(c, p, held)
		ok = ok && held
	}
	return ok
}

// writePermissionDenied answers a caller that misses one of the permissions a route
// requires. The body names the missing permission and where to get it; the plan's batch 1
// wording is 「请让根管理员授予 <perm>（gofer auth whoami 查看当前权限）」and batch 2 will
// point at the web page (design §3「403 文案分批」).
func (s *Server) writePermissionDenied(c *rux.Context, perms ...string) {
	d := s.decisionOf(c)
	missing := make([]string, 0, len(perms))
	for _, p := range perms {
		if !d.Has(p) {
			missing = append(missing, p)
		}
	}
	if len(missing) == 0 {
		return
	}
	writeError(c, http.StatusForbidden,
		"permission required: "+strings.Join(missing, ", "),
		"ask a root administrator to grant `"+strings.Join(missing, "`, `")+"` — `gofer auth whoami` shows the permissions you hold")
}

// decisionHook is the test seam of the design's third safety net (「调用断言」): with the
// hook installed — TestMain does that, so nothing is compiled differently for tests and no
// build tag is involved — every decision can, the permission and the outcome are visible
// to the test, which asserts that a route decided exactly the permissions it declares. It
// is nil in production, where recordDecision is one nil check.
var decisionHook func(c *rux.Context, perm string, granted bool)

// recordDecision hands one decision to the test hook, if one is installed.
func recordDecision(c *rux.Context, perm string, granted bool) {
	if decisionHook != nil {
		decisionHook(c, perm, granted)
	}
}
