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
	"slices"
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
	// sessionGrants records permissions whose only source was this request's session grant.
	sessionGrants map[string]string
}

// newDecision returns an empty decision for callerID.
func newDecision(callerID string) *decision {
	return &decision{
		callerID:      callerID,
		perms:         map[string]struct{}{},
		sources:       map[string]string{},
		grants:        map[string]string{},
		sessionGrants: map[string]string{},
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

// GrantID names the database grant that gave perm, or "" when no database grant did (the
// permission comes from the configuration, from the root administrator's everything, or the
// caller does not hold it).
func (d *decision) GrantID(perm string) string {
	if d == nil {
		return ""
	}
	return d.grants[perm]
}

// GrantIDs returns the database grant ids the decision rests on, deduped and sorted. It is
// the dependency set of design §6「长连接的撤销」 (T2.3 registers long connections against
// it) — the config-derived part has no stable id, which is why T2.3 compares callers across
// a reload instead.
// SessionGrantID reports the grant that solely supplied perm through this request.
func (d *decision) SessionGrantID(perm string) string {
	if d == nil {
		return ""
	}
	return d.sessionGrants[perm]
}

// GrantIDs returns the database grant ids the decision rests on. It is
// the dependency set of design §6「长连接撤销」.
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

// Database grants: the cache, the per-subject scopes and the read that fills them (design §2
// 「有效权限」and §3「一致性」, plan T2.1).

// defaultGrantCacheTTL is the invalidation window of the database half of the effective
// permissions (design §3: 「本进程写入后立即刷新，其他进程按 5 秒 TTL 失效」). Another
// process's write — or any write made straight through the store — becomes visible within it;
// a write that goes through THIS server refreshes the picture at once (invalidateDBGrants).
const defaultGrantCacheTTL = 5 * time.Second

// grantScope is the expanded permission set of ONE grant subject — `users`, or one
// `caller:<id>` — plus the grant every permission came from (the dependency set of design §6).
// It is what the resolver merges into a decision. A session subject has a scope of its own
// shape (it also carries the grant's owner and incarnation, which are compared per request),
// so the session-grant task builds that one.
type grantScope struct {
	// subject is the stored subject the scope was expanded from (`users`, `caller:<id>`).
	subject string
	// perms holds the permission names in catalog order.
	perms []string
	// grants maps each permission to the grant row that gave it (the dependency set).
	grants map[string]string
}

// dbGrants is the database half of a user-class caller's effective permissions (design §2
// 「有效权限」): the live grants of every subject the resolver reads, grouped by subject, plus
// the grant-table generation they were read at. The whole value is swapped in one assignment
// under grantMu, so a request never observes half a refresh.
type dbGrants struct {
	// version is auth_meta.grants_version at the moment of the read (design 「一致性」): the
	// generation of the grant table this picture came from. whoami and the grant list report
	// it, and it is what tells two reads apart.
	version int64
	// loadedAt is when the picture was read; it ages out after the server's TTL.
	loadedAt time.Time
	// pinned marks a picture installed by the tests (setDBGrants): no store stands behind it to
	// re-read, so it never ages out.
	pinned bool
	// users is the `users` subject; callers holds the `caller:<id>` scopes by caller id; sessions
	// holds the live `session:<sid>` rows by session id (they are judged per request, see
	// sessionGrantScope).
	users    *grantScope
	callers  map[string]*grantScope
	sessions map[string][]jobstore.AuthGrant
}

// expandGrantsFor expands the live rows of ONE subject into a scope (design §2「展开规则」). It
// is a pure function, so the rules below have one home and a test that needs no database:
//
//   - a row of another subject is ignored;
//   - a revoked, orphaned or expired row contributes nothing (the query already filters the
//     first two; the check is repeated here so the rule is visible and testable);
//   - a row whose pattern matches no permission any more (the catalog can shrink) is ignored
//     with a log line: it grants nothing, and failing the whole read because of it would be
//     worse;
//   - the expansion applies the safety rules of authz.Expand (no pattern ever reaches
//     auth.manage, a wildcard never reaches a dangerous permission, a session subject only
//     reaches what may be granted to a session).
func expandGrantsFor(rows []jobstore.AuthGrant, subject string, kind authz.SubjectKind, now int64) *grantScope {
	out := &grantScope{subject: subject, grants: map[string]string{}}
	for _, row := range rows {
		if row.Subject != subject {
			continue
		}
		if row.RevokedAt != 0 || row.OrphanedAt != 0 {
			continue
		}
		if row.ExpiresAt != 0 && row.ExpiresAt <= now {
			continue
		}
		exp, err := authz.Expand(row.Pattern, kind)
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

// userGrantsFrom expands the grants of the `users` subject — the ones that hold for every
// user-class caller.
func userGrantsFrom(rows []jobstore.AuthGrant, now int64) *grantScope {
	return expandGrantsFor(rows, jobstore.SubjectUsers, authz.SubjectUsers, now)
}

// loadDBGrants reads every live grant and groups it by subject — ONE query for all subjects,
// not one per caller. A read failure is logged and yields an empty picture: a caller then
// keeps only the config-derived grants, which is the fail-closed half of the design (a
// database hiccup must not widen anybody's authority).
func (s *Server) loadDBGrants() *dbGrants {
	out := &dbGrants{loadedAt: time.Now(), callers: map[string]*grantScope{}, sessions: map[string][]jobstore.AuthGrant{}}
	st := s.authStore()
	if st == nil {
		return out
	}
	if v, err := st.AuthGrantsVersion(); err != nil {
		slog.Warn("httpapi: reading the grant-table generation failed; reporting version 0", "err", err)
	} else {
		out.version = v
	}
	// ListActiveAuthGrantsAll filters the revoked and the orphaned rows in the query; the
	// subjects `session:<sid>` (only the session-grant task reads them) are skipped below.
	rows, err := st.ListActiveAuthGrantsAll()
	if err != nil {
		slog.Warn("httpapi: reading the permission grants failed; only the config-derived grants apply", "err", err)
		return out
	}
	now := time.Now().Unix()
	out.users = userGrantsFrom(rows, now)
	for _, row := range rows {
		kind, id, err := authz.ParseSubject(row.Subject)
		if err != nil {
			continue
		}
		switch kind {
		case authz.SubjectCaller:
			if _, ok := out.callers[id]; ok {
				continue
			}
			out.callers[id] = expandGrantsFor(rows, row.Subject, kind, now)
		case authz.SubjectSession:
			// Session grants are not expanded here: which of them may apply depends on the session
			// the REQUEST names (ownership, incarnation, whether it has ended), so the rows are
			// kept as they are and sessionGrantScope decides per request.
			out.sessions[id] = append(out.sessions[id], row)
		}
	}
	return out
}

// dbGrantsNow returns the live database half of the effective permissions, re-reading it when
// the current picture has aged past the server's TTL.
func (s *Server) dbGrantsNow() *dbGrants {
	s.dbGrantMu.Lock()
	defer s.dbGrantMu.Unlock()
	if c := s.dbGrant; c != nil && (c.pinned || time.Since(c.loadedAt) < s.grantTTL()) {
		return c
	}
	s.dbGrant = s.loadDBGrants()
	return s.dbGrant
}

// grantTTL is the cache TTL in force, with the shipped default when none was set.
func (s *Server) grantTTL() time.Duration {
	if s.grantCacheTTL > 0 {
		return s.grantCacheTTL
	}
	return defaultGrantCacheTTL
}

// invalidateDBGrants drops the picture so the next request re-reads it. It is what a write of
// THIS process does (design §3: 「本进程写入后立即刷新」) and what a configuration reload does
// (a reload can remove a caller, whose grants become orphans).
func (s *Server) invalidateDBGrants() {
	s.dbGrantMu.Lock()
	s.dbGrant = nil
	s.dbGrantMu.Unlock()
}

// grantsVersion is the grant-table generation of the current picture.
func (s *Server) grantsVersion() int64 { return s.dbGrantsNow().version }

// setDBGrants installs a database half directly and pins it. It exists for the tests that need
// a caller to hold exactly some database permissions without building a store (the InHandler
// and tunnel reverse cases); production code reads the picture from the store (dbGrantsNow).
func (s *Server) setDBGrants(scope *grantScope) {
	s.dbGrantMu.Lock()
	defer s.dbGrantMu.Unlock()
	g := &dbGrants{loadedAt: time.Now(), pinned: true, callers: map[string]*grantScope{}}
	if scope != nil {
		g.users = scope
	}
	s.dbGrant = g
}

// declaredCallerIDList returns the caller ids the LIVE configuration declares, sorted: the ids
// of `server.callers` plus the legacy single token's `default` (buildCallers resolves it
// whenever a token is live, whether or not a callers entry spells it out).
//
// It is the set a `caller:<id>` grant may belong to (design §2): a grant whose caller is not in
// it is an orphan. A caller declared without a resolvable token still counts — the
// configuration declares it, and its grants must survive an unset `token_env`.
func (s *Server) declaredCallerIDList() []string {
	seen := map[string]struct{}{}
	if snap := s.snap.Load(); snap != nil && snap.cfg != nil {
		for _, cc := range snap.cfg.Callers {
			if id := strings.TrimSpace(cc.ID); id != "" {
				seen[id] = struct{}{}
			}
		}
	}
	if s.token != "" {
		seen[authz.DefaultCallerID] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// callerNames maps caller id to display name for the callers the live configuration names
// (config callers[].name; the main-token caller `default` is named by an `id: default`
// entry). Display only: no decision reads it. Nil when nobody has a name.
func (s *Server) callerNames() map[string]string {
	snap := s.snap.Load()
	if snap == nil || snap.cfg == nil {
		return nil
	}
	if names := snap.cfg.CallerNames(); len(names) > 0 {
		return names
	}
	return nil
}

// isDeclaredCaller reports whether the live configuration declares caller id (see
// declaredCallerIDList).
func (s *Server) isDeclaredCaller(id string) bool {
	return slices.Contains(s.declaredCallerIDList(), id)
}

// orphanUnknownCallerGrants marks every live `caller:<id>` grant whose caller the
// configuration no longer declares as an orphan (design §2「孤儿授权」): it stops applying, and
// the id coming back in the configuration does NOT clear the mark — only the activate endpoint
// does, so a token reused by somebody else cannot inherit the grants of the previous holder.
//
// It runs when the caller set changes: at construction and on every configuration reload.
func (s *Server) orphanUnknownCallerGrants() {
	st := s.authStore()
	if st == nil {
		return
	}
	rows, err := st.ListActiveAuthGrantsAll()
	if err != nil {
		slog.Warn("httpapi: reading the permission grants failed; no grant was marked orphan", "err", err)
		return
	}
	var ids []string
	for _, row := range rows {
		kind, id, err := authz.ParseSubject(row.Subject)
		if err != nil || kind != authz.SubjectCaller || s.isDeclaredCaller(id) {
			continue
		}
		ids = append(ids, row.ID)
	}
	if len(ids) == 0 {
		return
	}
	n, err := st.OrphanAuthGrants(ids)
	if err != nil {
		slog.Warn("httpapi: marking the grants of unknown callers as orphans failed", "err", err)
		return
	}
	if n > 0 {
		slog.Info("httpapi: grants of callers that are no longer configured are now orphans",
			"grants", n, "hint", "a root administrator reactivates them on the permissions page")
		s.invalidateDBGrants()
	}
}

// decisionOf returns the decision of THIS request, resolving it once and caching it in the
// request context. It is the single read point of the permission model for a request.
func (s *Server) decisionOf(c *rux.Context) *decision {
	if v, ok := c.Get(ctxAuthzDecision); ok {
		if d, ok := v.(*decision); ok && d != nil {
			return d
		}
	}
	d := s.effective(callerFromCtx(c), requestSessionID(c))
	c.Set(ctxAuthzDecision, d)
	return d
}

// requestSessionID is the terminal session a request names (design §4「会话 id 的携带」): the
// `X-Gofer-Session-Id` header the client's single injection point attaches, empty when the request
// carries none. It is read ONCE per request, here, and the answer is part of the decision.
func requestSessionID(c *rux.Context) string {
	if c == nil || c.Req == nil {
		return ""
	}
	return strings.TrimSpace(c.Req.Header.Get(sessionHeader))
}

// effective resolves the effective permissions of one user-class caller (design §2
// 「有效权限」): everything for a root administrator, otherwise the union of the config-derived
// grants, the `users` grants of the database, the database grants of the caller's own
// `caller:<id>` subject, and — added by T2.2 — the grants of the session THIS request names.
//
// The database half is read through the versioned cache (dbGrantsNow): another process's write
// shows up within the TTL, this process's own write at once. sessionID is the request's
// `X-Gofer-Session-Id` (empty when it carries none), and it is the only thing that can bring a
// `session:<sid>` grant into the picture.
func (s *Server) effective(callerID, sessionID string) *decision {
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
	g := s.dbGrantsNow()
	if g == nil {
		return d
	}
	// `users` first: when both subjects grant the same permission, the dependency set names the
	// wider one, and the source is `db` either way.
	d.addScope(g.users)
	d.addScope(g.callers[callerID])
	ss := s.sessionGrantScope(g, callerID, sessionID)
	d.addScope(ss)
	if ss != nil {
		for perm, id := range ss.grants {
			if d.Source(perm) == sourceDB && d.GrantID(perm) == id {
				d.sessionGrants[perm] = id
			}
		}
	}
	return d
}

// sessionGrantScope returns the `session:<sid>` grants that are IN FORCE for this request, or nil
// when none is. Every condition of design §4「生效条件」 is decided here:
//
//   - the request names a session, and names it as a user-class caller with an id of its own;
//   - the session's CURRENT owner, the owner the grant recorded and the caller are the same
//     three-way agreement (a hand-over therefore ends every grant of the previous owner);
//   - the session's CURRENT incarnation equals the one the grant recorded — a session that ended
//     and came back, or that was taken over after it ended, has a new one, so the grants of its
//     previous life never apply again;
//   - the session has not ended: its state is not `ended` AND its `ended_at` is empty (the second
//     safety net of design §4: a take-over keeps the stamp of the session that had ended);
//   - the grant is not revoked and not orphaned (the query behind dbGrants already dropped those)
//     and has not expired.
//
// A read failure of the session row grants nothing: an unreachable store must never widen
// anybody's permissions.
func (s *Server) sessionGrantScope(g *dbGrants, callerID, sessionID string) *grantScope {
	if g == nil || callerID == "" || sessionID == "" {
		return nil
	}
	rows := g.sessions[sessionID]
	if len(rows) == 0 {
		return nil
	}
	st := s.authStore()
	if st == nil {
		return nil
	}
	anchor, ok, err := st.SessionGrantAnchor(sessionID)
	if err != nil {
		slog.Warn("httpapi: reading the session a grant belongs to failed; its session grants do not apply",
			"session", sessionID, "err", err)
		return nil
	}
	if !ok || anchor.Owner != callerID || anchor.Ended() {
		return nil
	}
	now := time.Now().Unix()
	// util.CapSum is not needed here: len(rows) is a count of rows, not a length sum.
	live := make([]jobstore.AuthGrant, 0, len(rows))
	for _, row := range rows {
		if !sessionGrantRowInForce(row, anchor, callerID, now) {
			continue
		}
		live = append(live, row)
	}
	if len(live) == 0 {
		return nil
	}
	return expandGrantsFor(live, authz.SubjectOf(authz.SubjectSession, sessionID), authz.SubjectSession, now)
}

// sessionGrantRowInForce is the per-row half of design §4「生效条件」, shared by the enforcement
// path (sessionGrantScope) and the preview GET /v1/auth/grants reports as `effective`, so the
// two cannot drift: the grant's recorded owner is the caller, its recorded incarnation is the
// session's current one, and it has not expired. The caller-agreement, ended and
// revoked/orphaned conditions are decided by the callers around it (the anchor's Owner /
// Ended(), and the grants query).
func sessionGrantRowInForce(row jobstore.AuthGrant, anchor jobstore.SessionGrantAnchor, callerID string, now int64) bool {
	if row.SessionOwner != callerID || row.SessionIncarnation != anchor.Incarnation {
		return false
	}
	return row.ExpiresAt == 0 || row.ExpiresAt > now
}

// sessionGrantEffective answers, for the grants listing, whether a stored session grant would be
// in force for its session's current owner right now. It runs the same conditions as
// sessionGrantScope: the row is active (not revoked / orphaned / expired / unmatched), the
// session exists and has not ended, and sessionGrantRowInForce holds with the session's current
// owner as the caller. A read failure answers false, like enforcement does.
func (s *Server) sessionGrantEffective(row jobstore.AuthGrant, active bool, now int64) bool {
	if !active || row.RevokedAt != 0 || row.OrphanedAt != 0 {
		return false
	}
	kind, sid, err := authz.ParseSubject(row.Subject)
	if err != nil || kind != authz.SubjectSession {
		return false
	}
	st := s.authStore()
	if st == nil {
		return false
	}
	anchor, ok, err := st.SessionGrantAnchor(sid)
	if err != nil || !ok || anchor.Owner == "" || anchor.Ended() {
		return false
	}
	return sessionGrantRowInForce(row, anchor, anchor.Owner, now)
}

// addScope merges one database scope into the decision (design §2「有效权限」): a permission the
// caller does not hold yet is added with source `db`, and the grant it came from is recorded
// even when the configuration grants the permission as well — revoking that grant must be
// re-evaluated (design §6), and the re-evaluation then finds the config floor and keeps the
// connection.
func (d *decision) addScope(scope *grantScope) {
	if scope == nil {
		return
	}
	for _, p := range scope.perms {
		if _, ok := d.perms[p]; !ok {
			d.perms[p] = struct{}{}
			d.sources[p] = sourceDB
		}
		if id := scope.grants[p]; id != "" && d.grants[p] == "" {
			d.grants[p] = id
		}
	}
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
		if held && d.SessionGrantID(p) != "" {
			s.recordSessionGrantUse(c, p, d.SessionGrantID(p))
		}
		ok = ok && held
	}
	return ok
}

// writePermissionDenied answers a caller that misses one of the permissions a route
// requires. The body names the missing permission and where to get it; the plan's batch 1
// wording is 「请让根管理员授予 <perm>（gofer auth whoami 查看当前权限）」and batch 2 will
// point at the web page (design §3「403 文案分批」).
func (s *Server) recordSessionGrantUse(c *rux.Context, perm, grantID string) {
	st := s.authStore()
	if st == nil {
		return
	}
	if err := st.MarkAuthGrantUsed(grantID, time.Now().Unix()); err != nil {
		slog.Warn("httpapi: recording session grant use failed", "grant", grantID, "err", err)
		return
	}
	subject := authz.SubjectOf(authz.SubjectSession, requestSessionID(c))
	if grant, ok, err := st.GetAuthGrant(grantID); err == nil && ok {
		subject = grant.Subject
	}
	_ = st.InsertAuthAudit(jobstore.AuthAuditEntry{Actor: callerFromCtx(c), Action: "request.session_grant", Subject: subject, Permission: perm, SessionID: requestSessionID(c), GrantID: grantID, Method: c.Req.Method, Path: c.Req.URL.Path, Status: http.StatusOK})
}

func (s *Server) recordPermissionDenied(c *rux.Context, perm string) {
	st := s.authStore()
	if st == nil {
		return
	}
	_ = st.InsertAuthAudit(jobstore.AuthAuditEntry{Actor: callerFromCtx(c), Action: "request.denied", Subject: authz.SubjectOf(authz.SubjectCaller, callerFromCtx(c)), Permission: perm, SessionID: requestSessionID(c), Method: c.Req.Method, Path: c.Req.URL.Path, Status: http.StatusForbidden, Reason: "permission required"})
}

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
	for _, perm := range missing {
		s.recordPermissionDenied(c, perm)
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
