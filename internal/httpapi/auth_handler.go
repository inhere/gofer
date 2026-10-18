// 授权管理接口（设计 docs/design/2026-10-14-db-capabilities-and-session-grants-design.md
// 「接口」一节、§2「授权模型与判定」与 §4「会话授权」，计划 T2.1）。
//
// The design's interfaces live together here, because they are views of ONE model: a grant is a
// stored pattern, and everything asked about it — the caller's own effective permissions
// (whoami), the catalog the pattern expands against, the routes that ask for a permission, the
// grants themselves and the audit of what was decided — is that same model seen from a
// different side.
//
// What is deliberately NOT here:
//
//   - the permission judgment: authz_resolve.go resolves a caller's effective permissions, and
//     the route gate and the handlers ask through s.can (G021: no entry-layer business logic in
//     the decision itself);
//   - the audit WRITE side (授权的增删、被拒的请求与使用计数are the audit task's, plan T2.4).
//     This file reads auth_audit; the store offers the insert seam.
//
// `auth.manage` is the catalog's one root-risk permission, and no grant can ever match it (that
// is the security property: internal/authz refuses it in Expand, so `*` cannot hand it out).
// The gate therefore leaves the five management routes to the root administrator alone,
// whatever the governance switches say. A worker token and a job credential are refused by the
// caller-kind check below: the route gate deliberately does not judge a non-user caller (design
// §2「只对 user 类调用方计算」), and a job credential may READ every GET route through SEC-01
// (jobcredential.go), which must not open the grant list to a job.
package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/jobstore"
)

// sessionHeader is the request header a gofer client uses to name the terminal session a
// request belongs to (design §4「会话 id 的携带」). whoami reports what the request carried, so
// an operator can see which session it was attached to; the client-side injection point (the
// explicit GOFER_SESSION_ID and the unique-candidate rule) is the session-grant task's.
const sessionHeader = "X-Gofer-Session-Id"

// authSessionGrantMaxSec is the ceiling of a session grant's validity (design §4「有效期」:
// 「可选 1 / 4 / 24 小时，或「会话结束」。「会话结束」也一律带上不超过 24 小时的 expires」).
const authSessionGrantMaxSec = 24 * 60 * 60

// configGrantPrefix marks the ids of the config-derived, read-only rows of the grant list: they
// are not rows of auth_grants and can be neither revoked nor activated.
const configGrantPrefix = "config:"

// authStore is the metadata store the grant interfaces work on. nil (mcp, a bare test server)
// makes every one of them answer 503 rather than pretend there are no grants.
func (s *Server) authStore() *jobstore.Store {
	if s.jobs == nil {
		return nil
	}
	return s.jobs.Meta()
}

// grantAdminOnly refuses a caller that is not a person BEFORE any grant is looked at. The route
// gate already required `auth.manage` (which only a root administrator holds) of a user-class
// caller; this is the other half — the kind half — of the same rule, and it has to live here
// because the gate does not judge a non-user caller at all.
func (s *Server) grantAdminOnly(c *rux.Context) bool {
	if callerKindFromCtx(c) == callerKindUser {
		return true
	}
	writeError(c, http.StatusForbidden, "not permitted for this caller",
		"only a person's credential may manage permission grants")
	return false
}

// authzRoots returns the root administrator caller ids of the live configuration, sorted (empty
// when this deployment has none: nobody can manage grants there).
func (s *Server) authzRoots() []string {
	d := s.authzDerivation()
	if d == nil {
		return nil
	}
	return d.Roots()
}

// ---- whoami ----

// authPermissionView is one entry of a caller's effective permissions: what it is, where it came
// from (root | config | db) and — when a database grant is behind it — which grant.
type authPermissionView struct {
	Name    string `json:"name"`
	Source  string `json:"source"`
	GrantID string `json:"grant_id,omitempty"`
}

// authCeilingView is the caller's own scope ceiling (上限) as GET /v1/auth/whoami reports it:
// what the token is limited to. The three switches are the EFFECTIVE values (the strict
// defaults of a present ceiling are already applied: allow_exec / global_memory / read_only
// are false unless the configuration says otherwise), so a console needs no second guess —
// read_only hides the write actions, the lists drive which project / runner / agent options
// it offers. An omitted list means that dimension is not restricted, EXCEPT runners: an
// omitted runners list means the machine runner is NOT allowed (worker runners only).
type authCeilingView struct {
	Projects     []string `json:"projects,omitempty"`
	Runners      []string `json:"runners,omitempty"`
	Workers      []string `json:"workers,omitempty"`
	Agents       []string `json:"agents,omitempty"`
	AllowExec    bool     `json:"allow_exec"`
	GlobalMemory bool     `json:"global_memory"`
	ReadOnly     bool     `json:"read_only"`
}

// callerCeilingView projects the live configuration's ceiling for callerID, or nil when the
// caller has none (the key is then omitted from the answer). It reads the same config
// snapshot the permission decision reads, so a reload applies to the next request.
func (s *Server) callerCeilingView(callerID string) *authCeilingView {
	cfg := s.serverCfg()
	if cfg == nil {
		return nil
	}
	ceil, ok := cfg.CallerCeiling(callerID)
	if !ok {
		return nil
	}
	return &authCeilingView{
		Projects:     ceil.Projects,
		Runners:      ceil.Runners,
		Workers:      ceil.Workers,
		Agents:       ceil.Agents,
		AllowExec:    ceil.ExecAllowed(),
		GlobalMemory: ceil.GlobalMemoryAllowed(),
		ReadOnly:     ceil.IsReadOnly(),
	}
}

// authWhoamiView is GET /v1/auth/whoami: who the request authenticated as, whether it is a root
// administrator, the permissions it really holds with their sources, and the session it was
// attached to. roots and hint answer the two questions a caller immediately has on a deployment
// that refuses it: who can grant me this, and — when nobody can — what is wrong.
type authWhoamiView struct {
	CallerID string `json:"caller_id"`
	// CallerName is the caller's configured display name (callers[].name); empty when none.
	CallerName string `json:"caller_name,omitempty"`
	// CallerNames maps caller id to display name for every named caller, so a console can
	// label the caller ids shown on jobs, sessions, plans and work items. Display only.
	CallerNames map[string]string `json:"caller_names,omitempty"`
	// Kind is user | worker | job (design §2「调用方类型」).
	Kind string `json:"kind"`
	// Root reports the root administrator of design §2: it holds every permission.
	Root bool `json:"root"`
	// Permissions is the effective set, in catalog order, each with its source.
	Permissions []authPermissionView `json:"permissions"`
	// SessionID is the session the request named, when it named one.
	SessionID string `json:"session_id,omitempty"`
	// GrantsVersion is the generation of the grant table this answer was computed from.
	GrantsVersion int64 `json:"grants_version"`
	// Roots lists this deployment's root administrators (empty: nobody can manage grants).
	Roots []string `json:"roots,omitempty"`
	// Ceiling is this caller's own scope ceiling (上限, `server.callers[].ceiling`);
	// omitted when the caller has none.
	Ceiling *authCeilingView `json:"ceiling,omitempty"`
	// Hint explains an empty or unattached answer (no root administrator, or a non-user
	// credential that permissions do not govern).
	Hint string `json:"hint,omitempty"`
}

// handleAuthWhoami answers GET /v1/auth/whoami.
func (s *Server) handleAuthWhoami(c *rux.Context) {
	out := authWhoamiView{
		CallerID:      callerFromCtx(c),
		CallerName:    s.callerNames()[callerFromCtx(c)],
		CallerNames:   s.callerNames(),
		Kind:          callerKindFromCtx(c),
		Permissions:   []authPermissionView{},
		SessionID:     requestSessionID(c),
		GrantsVersion: s.grantsVersion(),
		Roots:         s.authzRoots(),
		Ceiling:       s.callerCeilingView(callerFromCtx(c)),
	}
	if out.Kind != callerKindUser {
		// A worker token and a job credential are not governed by permissions at all: their own
		// allowlists are (workercredential.go / jobcredential.go), so an empty permission list
		// here means "permissions say nothing about this caller", not "it may do nothing".
		out.Hint = "this credential is not a permission subject; its own allowlist decides what it may call"
		c.JSON(http.StatusOK, out)
		return
	}
	d := s.decisionOf(c)
	out.Root = d.Root()
	for _, name := range d.Perms() {
		out.Permissions = append(out.Permissions, authPermissionView{
			Name: name, Source: d.Source(name), GrantID: d.GrantID(name),
		})
	}
	if len(out.Roots) == 0 {
		out.Hint = "this deployment has no root administrator, so nobody can grant permissions; " +
			"declare a caller with `can_admin: true`"
	}
	c.JSON(http.StatusOK, out)
}

// ---- catalog and routes ----

// authPermissionCatalogView is one row of GET /v1/auth/catalog: a catalog entry exactly as the
// code registers it (design §1).
type authPermissionCatalogView struct {
	Name             string `json:"name"`
	Description      string `json:"description"`
	Risk             string `json:"risk"`
	RiskLabel        string `json:"risk_label"`
	SessionGrantable bool   `json:"session_grantable"`
	Scope            string `json:"scope"`
	ScopeLabel       string `json:"scope_label"`
	Source           string `json:"source"`
	SourceLabel      string `json:"source_label"`
	// SeedVersion is the seed migration that plants the permission for the `users` subject
	// (0 = never seeded).
	SeedVersion int `json:"seed_version,omitempty"`
}

type authCatalogView struct {
	Permissions []authPermissionCatalogView `json:"permissions"`
	// Presets are the design's quick-fill patterns for the console (§2「预设」), stored as the
	// patterns they are.
	Presets []authPresetView `json:"presets"`
}

// authPresetView is one quick-fill of the design's preset list: the patterns a console fills in,
// and what they really hand out.
type authPresetView struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Patterns []string `json:"patterns"`
	// Permissions is the expansion of Patterns for a user-class caller.
	Permissions []string `json:"permissions"`
}

// authPresets are design §2「预设」as written there (the console fills the patterns; what is
// stored is still a pattern).
var authPresets = []authPresetView{
	{Key: "tunnel", Label: "隧道使用", Patterns: []string{authz.PermTunnelOpen}},
	{Key: "release", Label: "发版升级", Patterns: []string{"update.*", authz.PermWorkerUpgrade}},
	{Key: "answer", Label: "代答（人用）", Patterns: []string{authz.PermInteractionAnswer, authz.PermSessionReplyOthers}},
	{Key: "readonly", Label: "只读他人", Patterns: []string{
		authz.PermJobReadOthers, authz.PermSessionReadOthers, authz.PermConfigRead,
	}},
}

// handleAuthCatalog answers GET /v1/auth/catalog.
func (s *Server) handleAuthCatalog(c *rux.Context) {
	perms := authz.Permissions()
	out := authCatalogView{
		Permissions: make([]authPermissionCatalogView, 0, len(perms)),
		Presets:     append([]authPresetView(nil), authPresets...),
	}
	for _, p := range perms {
		out.Permissions = append(out.Permissions, authPermissionCatalogView{
			Name: p.Name, Description: p.Description,
			Risk: string(p.Risk), RiskLabel: p.Risk.Label(),
			SessionGrantable: p.SessionGrantable,
			Scope:            string(p.Scope), ScopeLabel: p.Scope.Label(),
			Source: string(p.Source), SourceLabel: p.Source.Label(),
			SeedVersion: p.SeedVersion,
		})
	}
	// Each preset's expansion is sent along, so the console can show what a quick-fill really
	// hands out (a pattern is only meaningful after the rules of authz.Expand).
	for i := range out.Presets {
		out.Presets[i].Permissions = presetPermissions(out.Presets[i].Patterns)
	}
	c.JSON(http.StatusOK, out)
}

// presetPermissions expands the patterns of one preset into the permission names a user-class
// caller ends up holding. A pattern that means nothing contributes nothing (it is a preset, not
// a grant: nothing is stored yet, so nothing has to be refused with an error).
func presetPermissions(patterns []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, pattern := range patterns {
		exp, err := authz.Expand(pattern, authz.SubjectUsers)
		if err != nil {
			continue
		}
		for _, p := range exp.Permissions {
			if _, dup := seen[p.Name]; dup {
				continue
			}
			seen[p.Name] = struct{}{}
			out = append(out, p.Name)
		}
	}
	return out
}

// authExpandPermView is one permission of the expansion preview.
type authExpandPermView struct {
	Name             string `json:"name"`
	Description      string `json:"description"`
	Risk             string `json:"risk"`
	RiskLabel        string `json:"risk_label"`
	SessionGrantable bool   `json:"session_grantable"`
}

// authExpandExcludedView is a permission the pattern reaches but the expansion rules keep out;
// reason is root_only | dangerous_wildcard | not_session_grantable (authz.Excluded*).
type authExpandExcludedView struct {
	Name   string `json:"name"`
	Risk   string `json:"risk"`
	Reason string `json:"reason"`
}

// authExpandView is GET /v1/auth/expand: what a pattern means for a subject kind, exactly as
// internal/authz.Expand computes it (the console previews with this instead of re-implementing
// the rules). A pattern that matches nothing answers 400 with the same body shape (error,
// detail and problem added), so the console can still say what kept the permissions out.
type authExpandView struct {
	Pattern     string                   `json:"pattern"`
	Subject     string                   `json:"subject"`
	Kind        string                   `json:"kind"`
	Permissions []authExpandPermView     `json:"permissions"`
	Excluded    []authExpandExcludedView `json:"excluded"`
	// Error / Detail / Problem are set only on the 400 for an empty expansion. Problem is
	// no_match | only_root | only_dangerous_wildcard | only_not_session_grantable.
	Error   string `json:"error,omitempty"`
	Detail  string `json:"detail,omitempty"`
	Problem string `json:"problem,omitempty"`
}

// handleAuthExpand answers GET /v1/auth/expand?pattern=&subject=. It is read-only and changes no
// decision: it only exposes authz.Explain.
func (s *Server) handleAuthExpand(c *rux.Context) {
	if !s.grantAdminOnly(c) {
		return
	}
	q := c.Req.URL.Query()
	pattern := strings.TrimSpace(q.Get("pattern"))
	if pattern == "" {
		writeError(c, http.StatusBadRequest, "invalid pattern", "pattern is required, e.g. `tunnel.*`")
		return
	}
	subject := strings.TrimSpace(q.Get("subject"))
	if subject == "" {
		subject = string(authz.SubjectUsers)
	}
	kind, id, err := authz.ParseSubject(subject)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid subject",
			"subject must be `users`, `caller:<id>` or `session:<sid>`")
		return
	}
	exp, excluded, err := authz.Explain(pattern, kind)
	out := authExpandView{
		Pattern: pattern, Subject: authz.SubjectOf(kind, id), Kind: string(kind),
		Permissions: []authExpandPermView{}, Excluded: make([]authExpandExcludedView, 0, len(excluded)),
	}
	for _, e := range excluded {
		out.Excluded = append(out.Excluded, authExpandExcludedView{
			Name: e.Permission.Name, Risk: string(e.Permission.Risk), Reason: e.Reason,
		})
	}
	if err != nil {
		if !errors.Is(err, authz.ErrNoMatch) {
			writeError(c, http.StatusInternalServerError, "cannot expand the pattern", err.Error())
			return
		}
		out.Error, out.Detail, out.Problem = "the pattern matches no permission", err.Error(), expandProblem(excluded)
		c.JSON(http.StatusBadRequest, out)
		return
	}
	for _, p := range exp.Permissions {
		out.Permissions = append(out.Permissions, authExpandPermView{
			Name: p.Name, Description: p.Description, Risk: string(p.Risk),
			RiskLabel: p.Risk.Label(), SessionGrantable: p.SessionGrantable,
		})
	}
	c.JSON(http.StatusOK, out)
}

// expandProblem names why an empty expansion is empty, from what the filters kept out.
func expandProblem(excluded []authz.Excluded) string {
	has := func(reason string) bool {
		for _, e := range excluded {
			if e.Reason == reason {
				return true
			}
		}
		return false
	}
	switch {
	case has(authz.ExcludedNotSessionGrantable):
		return "only_not_session_grantable"
	case has(authz.ExcludedDangerousWildcard):
		return "only_dangerous_wildcard"
	case has(authz.ExcludedRoot):
		return "only_root"
	}
	return "no_match"
}

// authRouteView is one row of GET /v1/auth/routes: a route of the running router with its
// declaration (design「路由声明」) — the same picture docs/reference/permissions.md renders, for
// a client that wants to ask the binary instead of reading the repository.
type authRouteView struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	// User is the user-class category: require | in_handler | public | no_user_surface |
	// pending.
	User string `json:"user"`
	// Mode is InHandler's mode (deny | filter): what the handler answers a caller that does not
	// hold the permission for someone else's resource.
	Mode string `json:"mode,omitempty"`
	// Permissions are the permissions the declaration names.
	Permissions []string `json:"permissions,omitempty"`
	// JobCredential is the SEC-01 route key a job credential reaches the route under.
	JobCredential string `json:"job_credential,omitempty"`
	// WorkerSelf marks a route of the worker's own allowlist.
	WorkerSelf bool `json:"worker_self,omitempty"`
	// External names the credential the route authenticates itself with.
	External string `json:"external,omitempty"`
	// Static marks the console's SPA fallback.
	Static bool `json:"static,omitempty"`
}

// handleAuthRoutes answers GET /v1/auth/routes.
func (s *Server) handleAuthRoutes(c *rux.Context) {
	keys := sortedRouteKeys()
	out := struct {
		Routes []authRouteView `json:"routes"`
	}{Routes: make([]authRouteView, 0, len(keys))}
	for _, key := range keys {
		decl := routeDecls[key]
		method, path, _ := strings.Cut(key, " ")
		out.Routes = append(out.Routes, authRouteView{
			Method: method, Path: path,
			User:          declUserCategory(decl),
			Mode:          declMode(decl),
			Permissions:   decl.user.perms,
			JobCredential: decl.jobCred,
			WorkerSelf:    decl.workerSelf,
			External:      decl.external,
			Static:        decl.static,
		})
	}
	c.JSON(http.StatusOK, out)
}

// declUserCategory names a declaration's user-class category the way the JSON asks for it.
func declUserCategory(decl routeDecl) string {
	switch decl.user.kind {
	case userRequire:
		return "require"
	case userInHandler:
		return "in_handler"
	case userPublic:
		return "public"
	case userNone:
		return "no_user_surface"
	case userPending:
		return "pending"
	default:
		return "unknown"
	}
}

// declMode is the InHandler mode of a declaration, empty for the categories that have none.
func declMode(decl routeDecl) string {
	if decl.user.kind != userInHandler {
		return ""
	}
	return string(decl.user.mode)
}

// ---- grants ----

// authGrantView is one grant of GET /v1/auth/grants (a row of auth_grants, or a config-derived
// read-only entry). state says why the grant does or does not apply right now; permissions is
// what the pattern means TODAY (the catalog can have grown since it was saved), and
// new_permissions is the「有新权限待确认」list of design §2.
type authGrantView struct {
	ID      string `json:"id"`
	Subject string `json:"subject"`
	// Source is db | config.
	Source  string `json:"source"`
	Pattern string `json:"pattern,omitempty"`
	Scope   string `json:"scope,omitempty"`
	// State is active | orphaned | revoked | expired | unmatched.
	State  string `json:"state"`
	Active bool   `json:"active"`
	// Effective is whether the grant is in force right now. For a session grant it is decided
	// with every condition of design §4 (state, expiry, session not ended, owner match, session
	// incarnation equal to the grant's) through the path enforcement uses; for other rows it
	// equals Active.
	Effective  bool   `json:"effective"`
	ExpiresAt  int64  `json:"expires_at,omitempty"`
	GrantedBy  string `json:"granted_by,omitempty"`
	GrantedAt  int64  `json:"granted_at,omitempty"`
	RevokedAt  int64  `json:"revoked_at,omitempty"`
	RevokedBy  string `json:"revoked_by,omitempty"`
	OrphanedAt int64  `json:"orphaned_at,omitempty"`
	// Revocable is false for a config-derived entry and for a grant that was already revoked.
	Revocable bool `json:"revocable"`
	// ReadOnly marks the config-derived entries (source=config): they follow the configuration
	// and cannot be revoked here.
	ReadOnly bool `json:"read_only,omitempty"`
	// SessionOwner / SessionIncarnation are set for a session subject: the owner and the
	// incarnation the grant was bound to (design §4).
	SessionOwner       string `json:"session_owner,omitempty"`
	SessionIncarnation string `json:"session_incarnation,omitempty"`
	// Permissions is the pattern's expansion today, in catalog order.
	Permissions []string `json:"permissions"`
	// Snapshot is what the pattern meant when the grant was saved.
	Snapshot []string `json:"snapshot,omitempty"`
	// NewPermissions is what it means now that the snapshot did not contain (「有新权限待确认」).
	NewPermissions []string `json:"new_permissions,omitempty"`
	Note           string   `json:"note,omitempty"`
	LastUsedAt     int64    `json:"last_used_at,omitempty"`
	UseCount       int64    `json:"use_count"`
}

// Grant states, in the order the resolution checks them: a revoked row stays revoked whatever
// else happened to it, and a row whose pattern (or subject) the catalog can no longer read
// grants nothing at all.
const (
	grantStateRevoked   = "revoked"
	grantStateOrphaned  = "orphaned"
	grantStateExpired   = "expired"
	grantStateUnmatched = "unmatched"
	grantStateActive    = "active"
)

// grantViewPermissions resolves what a stored grant means TODAY and what it grew into since it
// was saved. It is a pure function of the row, so the catalog-upgrade rule of design §2 can be
// exercised against a fabricated snapshot, and the list handler keeps thin.
//
// ok is false when the row cannot be read at all — an unknown subject or a pattern that matches
// no permission any more (the catalog can shrink): the row grants nothing, and the caller says
// so rather than pretending it still means what it meant when it was saved. pending is the
// 「有新权限待确认」list: what the pattern means now that the stored snapshot did not contain.
func grantViewPermissions(row jobstore.AuthGrant) (current, pending []string, ok bool) {
	kind, _, err := authz.ParseSubject(row.Subject)
	if err != nil {
		return nil, nil, false
	}
	exp, err := authz.Expand(row.Pattern, kind)
	if err != nil {
		return nil, nil, false
	}
	current = exp.Names()
	if grown, err := authz.NewSince(row.Pattern, kind, row.ExpandSnapshot); err == nil {
		for _, p := range grown {
			pending = append(pending, p.Name)
		}
	}
	return current, pending, true
}

// authGrantViewOf projects one stored grant. now is the caller's clock (unix seconds), because
// expiry is judged against it, not against the moment the row was written.
func authGrantViewOf(row jobstore.AuthGrant, now int64) authGrantView {
	v := authGrantView{
		ID: row.ID, Subject: row.Subject, Source: sourceDB, Pattern: row.Pattern, Scope: row.Scope,
		ExpiresAt: row.ExpiresAt, GrantedBy: row.GrantedBy, GrantedAt: row.GrantedAt,
		RevokedAt: row.RevokedAt, RevokedBy: row.RevokedBy, OrphanedAt: row.OrphanedAt,
		SessionOwner: row.SessionOwner, SessionIncarnation: row.SessionIncarnation,
		Permissions: []string{}, Snapshot: row.ExpandSnapshot, Note: row.Note,
		LastUsedAt: row.LastUsedAt, UseCount: row.UseCount,
		Revocable: row.RevokedAt == 0,
	}
	switch {
	case row.RevokedAt != 0:
		v.State = grantStateRevoked
	case row.OrphanedAt != 0:
		v.State = grantStateOrphaned
	case row.ExpiresAt != 0 && row.ExpiresAt <= now:
		v.State = grantStateExpired
	default:
		v.State = grantStateActive
	}
	current, pending, ok := grantViewPermissions(row)
	if !ok {
		// A revoked or orphaned row keeps the state it was given: that is the more useful
		// fact about it.
		if v.State == grantStateActive {
			v.State = grantStateUnmatched
		}
		return v
	}
	if current != nil {
		v.Permissions = current
	}
	v.Active = v.State == grantStateActive
	if v.Active {
		v.NewPermissions = pending
	}
	return v
}

// authDerivedView is one config-derived grant set (design §3): the read-only floor a
// configuration gives a subject. It has no id of its own (nothing is stored) and cannot be
// revoked — changing the configuration is what changes it.
type authDerivedView struct {
	ID      string `json:"id"`
	Subject string `json:"subject"`
	Source  string `json:"source"`
	// ReadOnly is always true: the whole point of this half.
	ReadOnly    bool     `json:"read_only"`
	Permissions []string `json:"permissions"`
}

type authGrantsView struct {
	// Version is the grant-table generation the answer was computed from.
	Version int64 `json:"version"`
	// Roots lists the root administrators: they hold everything and are the only subject that
	// may change any of this.
	Roots []string `json:"roots,omitempty"`
	// Grants are the stored rows, newest last, including the revoked and the orphaned ones
	// (the console has to be able to show what was taken away).
	Grants []authGrantView `json:"grants"`
	// Derived are the config-derived sets, read-only.
	Derived []authDerivedView `json:"derived"`
	// CallerNames maps caller id to display name (callers[].name) for the `caller:<id>`
	// subjects and the granted_by / roots ids. Display only.
	CallerNames map[string]string `json:"caller_names,omitempty"`
}

// handleAuthListGrants answers GET /v1/auth/grants.
func (s *Server) handleAuthListGrants(c *rux.Context) {
	if !s.grantAdminOnly(c) {
		return
	}
	st := s.authStore()
	if st == nil {
		writeError(c, http.StatusServiceUnavailable, "grants unavailable", "this server has no metadata store")
		return
	}
	rows, err := st.ListAuthGrants()
	if err != nil {
		writeError(c, http.StatusInternalServerError, "cannot list grants", err.Error())
		return
	}
	now := time.Now().Unix()
	out := authGrantsView{
		Roots:       s.authzRoots(),
		Grants:      make([]authGrantView, 0, len(rows)),
		Derived:     s.derivedGrants(),
		CallerNames: s.callerNames(),
	}
	if v, err := st.AuthGrantsVersion(); err == nil {
		out.Version = v
	}
	for _, row := range rows {
		v := authGrantViewOf(row, now)
		if kind, _, err := authz.ParseSubject(row.Subject); err == nil && kind == authz.SubjectSession {
			v.Effective = s.sessionGrantEffective(row, v.Active, now)
		} else {
			v.Effective = v.Active
		}
		out.Grants = append(out.Grants, v)
	}
	c.JSON(http.StatusOK, out)
}

// derivedGrants renders the config-derived half (design §3「配置推出的授权」): the `users` set
// and each declared caller's own set, each read-only. A configuration that derives nothing for a
// caller produces no row for it, so the list stays readable.
func (s *Server) derivedGrants() []authDerivedView {
	d := s.authzDerivation()
	if d == nil {
		return nil
	}
	var out []authDerivedView
	if perms := d.Users(); len(perms) > 0 {
		out = append(out, authDerivedView{
			ID: configGrantPrefix + jobstore.SubjectUsers, Subject: jobstore.SubjectUsers,
			Source: sourceConfig, ReadOnly: true, Permissions: perms,
		})
	}
	for _, id := range s.declaredCallerIDList() {
		if perms := d.For(id); len(perms) > 0 {
			out = append(out, authDerivedView{
				ID:      configGrantPrefix + authz.SubjectOf(authz.SubjectCaller, id),
				Subject: authz.SubjectOf(authz.SubjectCaller, id),
				Source:  sourceConfig, ReadOnly: true, Permissions: perms,
			})
		}
	}
	return out
}

// authGrantCreateReq is the body of POST /v1/auth/grants. The subject is written the way it is
// stored (`users`, `caller:<id>`, `session:<sid>`); the pattern is what will be expanded and
// snapshotted. expires_sec is required for a session subject and optional elsewhere (0 = no
// expiry).
type authGrantCreateReq struct {
	Subject    string `json:"subject"`
	Pattern    string `json:"pattern"`
	Scope      string `json:"scope"`
	ExpiresSec int64  `json:"expires_sec"`
	Note       string `json:"note"`
}

// handleAuthCreateGrant answers POST /v1/auth/grants. Everything is validated BEFORE the row is
// written, so a refused request changes nothing (a grant is only meaningful if it applies):
//
//   - the subject must be one of the three the model has, and a `caller:<id>` must name a caller
//     the configuration declares (a grant for a caller nobody has is a typo, not a plan — the
//     orphan state exists for a caller that went AWAY);
//   - a `session:<sid>` must name a session that exists and has not ended, and must carry an
//     expiry of at most 24 hours (design §4「有效期」); the row records the owner and the
//     incarnation the session had at this moment;
//   - scope is `*` in this phase (design decision 10);
//   - the pattern must mean something: at least one catalog permission (design §2). A pattern
//     reaching nothing — or one that only reaches auth.manage, or a dangerous permission behind a
//     wildcard — is a 400, not a stored no-op.
func (s *Server) handleAuthCreateGrant(c *rux.Context) {
	if !s.grantAdminOnly(c) {
		return
	}
	st := s.authStore()
	if st == nil {
		writeError(c, http.StatusServiceUnavailable, "grants unavailable", "this server has no metadata store")
		return
	}
	var req authGrantCreateReq
	if err := c.BindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	kind, id, err := authz.ParseSubject(req.Subject)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid subject",
			"subject must be `users`, `caller:<id>` or `session:<sid>`")
		return
	}
	if pattern := strings.TrimSpace(req.Pattern); pattern == "" {
		writeError(c, http.StatusBadRequest, "invalid pattern", "pattern is required, e.g. `tunnel.*`")
		return
	}
	if scope := strings.TrimSpace(req.Scope); scope != "" && scope != "*" {
		writeError(c, http.StatusBadRequest, "invalid scope",
			"scope "+scope+" is not supported: every grant applies to everything")
		return
	}
	if kind == authz.SubjectCaller && !s.isDeclaredCaller(id) {
		writeError(c, http.StatusBadRequest, "unknown caller",
			"caller "+id+" is not declared in server.callers")
		return
	}
	grant := jobstore.AuthGrant{
		Subject:   authz.SubjectOf(kind, id),
		Pattern:   strings.TrimSpace(req.Pattern),
		Scope:     "*",
		GrantedBy: callerFromCtx(c),
		Note:      strings.TrimSpace(req.Note),
	}
	now := time.Now().Unix()
	if kind == authz.SubjectSession {
		if req.ExpiresSec <= 0 || req.ExpiresSec > authSessionGrantMaxSec {
			writeError(c, http.StatusBadRequest, "invalid expires_sec",
				"a session grant needs expires_sec between 1 and "+strconv.Itoa(authSessionGrantMaxSec)+" seconds (24 hours)")
			return
		}
		anchor, ok, err := st.SessionGrantAnchor(id)
		if err != nil {
			writeError(c, http.StatusInternalServerError, "cannot read the session", err.Error())
			return
		}
		if !ok {
			writeError(c, http.StatusNotFound, "unknown session", "session "+id+" is not registered")
			return
		}
		if anchor.Ended() {
			writeError(c, http.StatusConflict, "the session has ended",
				"a grant on an ended session would never apply: hand the session over or wait for it to come back")
			return
		}
		grant.SessionOwner, grant.SessionIncarnation = anchor.Owner, anchor.Incarnation
		grant.ExpiresAt = now + req.ExpiresSec
	}
	created, err := st.CreateAuthGrant(grant)
	if err != nil {
		status, msg := http.StatusInternalServerError, "cannot create the grant"
		if errors.Is(err, authz.ErrNoMatch) {
			status, msg = http.StatusBadRequest, "the pattern matches no permission"
		}
		writeError(c, status, msg, err.Error())
		return
	}
	// A write of THIS process refreshes the resolver at once (design §3「一致性」), so the
	// caller that was just granted something can use it on its next request.
	s.invalidateDBGrants()
	_ = st.InsertAuthAudit(jobstore.AuthAuditEntry{Actor: callerFromCtx(c), Action: "grant.create", Subject: created.Subject, Permission: created.Pattern, GrantID: created.ID})
	c.JSON(http.StatusCreated, authGrantViewOf(created, now))
}

// handleAuthRevokeGrant answers DELETE /v1/auth/grants/{id}: the row stays (audit and「撤销后
// 重启不还原」both need it) and only gets revoked_at / revoked_by. Revoking twice is a no-op
// that still answers 200 with the row, so a console that retries is harmless.
func (s *Server) handleAuthRevokeGrant(c *rux.Context) {
	if !s.grantAdminOnly(c) {
		return
	}
	id := strings.TrimSpace(c.Param("id"))
	if strings.HasPrefix(id, configGrantPrefix) {
		writeError(c, http.StatusConflict, "the grant cannot be revoked",
			"config-derived grants are read-only: change the configuration instead")
		return
	}
	st := s.authStore()
	if st == nil {
		writeError(c, http.StatusServiceUnavailable, "grants unavailable", "this server has no metadata store")
		return
	}
	row, ok, err := st.RevokeAuthGrant(id, callerFromCtx(c))
	if err != nil {
		writeError(c, http.StatusInternalServerError, "cannot revoke the grant", err.Error())
		return
	}
	if !ok {
		writeError(c, http.StatusNotFound, "unknown grant", "grant "+id+" does not exist")
		return
	}
	s.invalidateDBGrants()
	_ = st.InsertAuthAudit(jobstore.AuthAuditEntry{Actor: callerFromCtx(c), Action: "grant.revoke", Subject: row.Subject, Permission: row.Pattern, GrantID: row.ID})
	// Revoking ends the long connections that rested on this grant (design §6「授权被撤销」): a
	// tunnel, an attach relay, an SSE stream or the push channel whose permission came from it is
	// re-judged at once, and one that still holds the permission from another source (the
	// configuration, another grant, its own job) stays.
	s.recheckLiveConns("grant revoked")
	c.JSON(http.StatusOK, authGrantViewOf(row, time.Now().Unix()))
}

// handleAuthActivateGrant answers POST /v1/auth/grants/{id}/activate: it clears the orphan mark of
// a grant whose caller came back (design §2 — the id returning to the configuration does not
// restore it by itself, so a token reused by somebody else cannot inherit old grants).
func (s *Server) handleAuthActivateGrant(c *rux.Context) {
	if !s.grantAdminOnly(c) {
		return
	}
	id := strings.TrimSpace(c.Param("id"))
	if strings.HasPrefix(id, configGrantPrefix) {
		writeError(c, http.StatusConflict, "the grant cannot be activated",
			"config-derived grants are not orphaned; they follow the configuration")
		return
	}
	st := s.authStore()
	if st == nil {
		writeError(c, http.StatusServiceUnavailable, "grants unavailable", "this server has no metadata store")
		return
	}
	row, ok, err := st.ActivateAuthGrant(id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "cannot activate the grant", err.Error())
		return
	}
	if !ok {
		writeError(c, http.StatusNotFound, "unknown grant", "grant "+id+" does not exist")
		return
	}
	s.invalidateDBGrants()
	_ = st.InsertAuthAudit(jobstore.AuthAuditEntry{Actor: callerFromCtx(c), Action: "grant.activate", Subject: row.Subject, Permission: row.Pattern, GrantID: row.ID})
	c.JSON(http.StatusOK, authGrantViewOf(row, time.Now().Unix()))
}

// authGrantRenewReq is the body of POST /v1/auth/grants/{id}/renew: how much longer the grant
// should last, counted from now. There is no other field: renewal moves ONLY the deadline, so
// there is nothing else a caller could ask for.
type authGrantRenewReq struct {
	ExpiresSec int64 `json:"expires_sec"`
}

// handleAuthRenewGrant answers POST /v1/auth/grants/{id}/renew: it moves the deadline of a LIVE
// session grant to now + expires_sec (设计 §4「有效期」: 会话授权最长 24 小时，到期后不重新授权就
// 没得用). Extending the same row — instead of writing a second grant — is what keeps the
// permission scope where it was: the pattern, the expansion snapshot, the grantor, the session
// owner and the incarnation all stay, so a renewal can neither widen nor move a grant.
//
// What is refused, and why each refusal happens BEFORE the store is asked to write:
//
//   - a config-derived id: 409 — those entries follow the configuration and are read-only,
//     exactly as for revoke and activate;
//   - an id that is no stored row: 404;
//   - a subject that is not a session: 400 — a users / caller row has no deadline worth
//     renewing (0 there means 「no expiry」, not「expires now」);
//   - a grant that is not in force right now: 409 with the condition that failed. The verdict is
//     the listing's own (authGrantViewOf for the row state plus sessionGrantEffective for the
//     session half — the same pair GET /v1/auth/grants reports as `effective`), so what a console
//     shows as 生效 is exactly what may be renewed. A revoked, orphaned, expired or
//     no-longer-matching row, an ended session, and a session whose owner or incarnation moved on
//     are all refused: those need a NEW grant, not a longer one (设计 §4「生效条件」).
//
// The row write, the grant-table generation bump and the `grant.renew` audit entry (carrying the
// old and the new deadline) are the store's single transaction, so there is deliberately no
// second InsertAuthAudit here.
func (s *Server) handleAuthRenewGrant(c *rux.Context) {
	if !s.grantAdminOnly(c) {
		return
	}
	id := strings.TrimSpace(c.Param("id"))
	if strings.HasPrefix(id, configGrantPrefix) {
		writeError(c, http.StatusConflict, "the grant cannot be renewed",
			"config-derived grants are read-only: change the configuration instead")
		return
	}
	st := s.authStore()
	if st == nil {
		writeError(c, http.StatusServiceUnavailable, "grants unavailable", "this server has no metadata store")
		return
	}
	var req authGrantRenewReq
	if err := c.BindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if req.ExpiresSec <= 0 || req.ExpiresSec > authSessionGrantMaxSec {
		writeError(c, http.StatusBadRequest, "invalid expires_sec",
			"expires_sec must be between 1 and "+strconv.Itoa(authSessionGrantMaxSec)+" seconds (24 hours)")
		return
	}
	row, ok, err := st.GetAuthGrant(id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "cannot read the grant", err.Error())
		return
	}
	if !ok {
		writeError(c, http.StatusNotFound, "unknown grant", "grant "+id+" does not exist")
		return
	}
	if kind, _, err := authz.ParseSubject(row.Subject); err != nil || kind != authz.SubjectSession {
		writeError(c, http.StatusBadRequest, "not a session grant",
			"only a session subject has a deadline to renew; grant "+id+" is held by "+row.Subject)
		return
	}
	now := time.Now().Unix()
	view := authGrantViewOf(row, now)
	view.Effective = s.sessionGrantEffective(row, view.Active, now)
	if !view.Effective {
		writeError(c, http.StatusConflict, "the grant is not in force",
			s.grantRenewRefusal(st, row, view))
		return
	}
	updated, err := st.RenewAuthGrant(id, now+req.ExpiresSec, callerFromCtx(c))
	if err != nil {
		// The grant was in force a moment ago, so between the read above and this write it can
		// only have been revoked or have passed its deadline. The store reports both with its own
		// sentinel, and both mean the same thing to the caller: grant it again.
		switch {
		case errors.Is(err, jobstore.ErrAuthGrantNotFound):
			writeError(c, http.StatusNotFound, "unknown grant", "grant "+id+" does not exist")
		case errors.Is(err, jobstore.ErrAuthGrantRevoked), errors.Is(err, jobstore.ErrAuthGrantExpired):
			writeError(c, http.StatusConflict, "the grant is not in force", err.Error())
		default:
			writeError(c, http.StatusInternalServerError, "cannot renew the grant", err.Error())
		}
		return
	}
	// A write of THIS process refreshes the resolver at once (design §3「一致性」), so the session
	// keeps its permissions without waiting for the cache TTL.
	s.invalidateDBGrants()
	out := authGrantViewOf(updated, now)
	out.Effective = s.sessionGrantEffective(updated, out.Active, now)
	c.JSON(http.StatusOK, out)
}

// grantRenewRefusal names the condition of design §4 that keeps a stored session grant out of
// force, for the 409 body. The verdict itself is NOT decided here — the caller already has it
// from the listing's pair (authGrantViewOf + sessionGrantEffective); this only turns「不生效」into a
// reason an operator can act on, asking the session for its current facts once.
func (s *Server) grantRenewRefusal(st *jobstore.Store, row jobstore.AuthGrant, view authGrantView) string {
	switch view.State {
	case grantStateRevoked:
		return "grant " + row.ID + " was revoked: grant it again instead"
	case grantStateOrphaned:
		return "grant " + row.ID + " is orphaned: activate it first"
	case grantStateExpired:
		return "grant " + row.ID + " expired at " + strconv.FormatInt(row.ExpiresAt, 10) + ": grant it again instead"
	case grantStateUnmatched:
		return "grant " + row.ID + " no longer matches any permission: grant it again instead"
	}
	// The row itself is active, so the session half of design §4 refused it: the session's own
	// facts say which condition failed.
	_, sid, _ := authz.ParseSubject(row.Subject)
	anchor, ok, err := st.SessionGrantAnchor(sid)
	switch {
	case err != nil:
		return "the session " + sid + " cannot be read: " + err.Error()
	case !ok:
		return "session " + sid + " is not registered any more: grant it again instead"
	case anchor.Ended():
		return "session " + sid + " has ended: grant it again instead"
	case anchor.Owner == "":
		return "session " + sid + " has no owner: grant it again instead"
	case anchor.Owner != row.SessionOwner:
		return "session " + sid + " now belongs to " + anchor.Owner + ": grant it again instead"
	case anchor.Incarnation != row.SessionIncarnation:
		return "session " + sid + " is a new life (its incarnation changed): grant it again instead"
	}
	// Unreachable while the two judgments agree — and honest if they ever stop agreeing.
	return "grant " + row.ID + " is not in force"
}

// ---- audit ----

// handleAuthAudit answers GET /v1/auth/audit. The filters are the design's (`subject`,
// `permission`, `grant_id`), and the answer is newest first. The rows are written by the audit
// task (plan T2.4): an empty list here means nothing was recorded yet, not that nothing happened.
func (s *Server) handleAuthAudit(c *rux.Context) {
	if !s.grantAdminOnly(c) {
		return
	}
	st := s.authStore()
	if st == nil {
		writeError(c, http.StatusServiceUnavailable, "audit unavailable", "this server has no metadata store")
		return
	}
	q := c.Req.URL.Query()
	filter := jobstore.AuthAuditFilter{
		Subject: q.Get("subject"), Permission: q.Get("permission"), GrantID: q.Get("grant_id"),
	}
	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			writeError(c, http.StatusBadRequest, "invalid limit",
				"limit must be a positive integer (at most "+strconv.Itoa(jobstore.MaxAuthAuditLimit)+")")
			return
		}
		filter.Limit = n
	}
	entries, err := st.ListAuthAudit(filter)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "cannot list the audit", err.Error())
		return
	}
	if entries == nil {
		entries = []jobstore.AuthAuditEntry{}
	}
	c.JSON(http.StatusOK, struct {
		Entries []jobstore.AuthAuditEntry `json:"entries"`
		Count   int                       `json:"count"`
	}{Entries: entries, Count: len(entries)})
}
