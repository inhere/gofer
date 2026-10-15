// Derivation of the root administrator set and of the config-derived grants
// (design §2「根管理员」 and §3「配置推出的授权（真值表）」of
// docs/design/2026-10-14-db-capabilities-and-session-grants-design.md).
//
// Before this design a caller's authority lived in four capability bits and four
// governance switches, read at ~30 places through the config helpers
// (ServerConfig.CallerCanAnswer and friends). Derive restates exactly those decisions in
// the new vocabulary — permissions — so that from T1.4 on there is ONE decision path:
// the derived grants are the read-only floor (source=config, design §3), database grants
// (T2.1) and session grants (T2.2) add to them, and the root administrator has
// everything.
//
// The truth table of design §3 is implemented in Derive and pinned cell by cell against
// the old helper functions by derive_truth_test.go.
//
// Only a user-class caller has permissions: worker and job credentials are confined by
// their own route tables (workercredential.go / jobcredential.go) and never consult a
// Derivation.
package authz

import (
	"slices"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/util"
)

// DefaultCallerID is the caller id of the legacy single server token (httpapi
// buildCallers): a deployment that declares no `server.callers` has exactly this one
// authenticating caller, and design §2 makes it the root administrator.
const DefaultCallerID = "default"

// Release markers of the deprecated capability bits and governance switches (G032: a
// compatibility path carries `DEPRECATED(v<in>): remove in v<in+3>` and a startup hint).
// The keys themselves are marked in internal/config/model.go; Deprecations reports them
// while they are still in use.
const (
	DeprecatedIn       = "v0.161"
	DeprecatedRemoveIn = "v0.164"
)

// Gate groups — the permission sets of the four config gates, in design §1 order. Each
// group belongs to one `require_X_capability` switch (design §3):
//
//   - gate CLOSED (the shipped default): the group is granted to the `users` subject, so
//     every user-class caller keeps today's behaviour;
//   - gate OPEN: only the declared callers holding the matching capability bit get it,
//     and the root administrator gets it anyway (it has every permission).
//
// The admin group differs in one point: with the gate OPEN nobody but the root
// administrator keeps it, so there is no capability column for it to land in.
var (
	// answerGroup is design §3's「answer 组」: gated by require_answer_capability.
	answerGroup = []string{PermJobReview, PermJobWakeOthers, PermInteractionAnswer}
	// replyGroup is `session.reply.others`: gated by the same switch, but with no `users`
	// grant at all — with the gate closed nobody gains it (a non-owner is refused today).
	replyGroup = []string{PermSessionReplyOthers}
	// attachGroup is `job.attach`: gated by require_attach_capability.
	attachGroup = []string{PermJobAttach}
	// tunnelGroup is `tunnel.open`: gated by require_tunnel_capability.
	tunnelGroup = []string{PermTunnelOpen}
	// adminGroup is design §3's「admin 组」: gated by require_admin_capability.
	adminGroup = []string{
		PermConfigWrite, PermProjectWrite, PermRulesWrite, PermSkillsWrite,
		PermWorkerManage, PermWorkerUpgrade, PermWorkerUpgradeUpload, PermUpdateApply,
		PermTrackerDelete,
	}
)

// rootOnlyGroup is design §3's「严格检查的那组」: the permissions only the root
// administrator has, whatever the switches say. It is read off the catalog's
// `SourceRoot` rows (auth.manage is source=root too, but it is the grant-management
// permission itself, never a route grant), so the list has exactly one definition. The
// derivation never grants any of them: only the root administrator has them, which is
// the `Effective(root)` branch plus the truth-table test's strict-action cells.
func rootOnlyGroup() []string {
	var out []string
	for _, p := range catalog {
		if p.Source == SourceRoot && p.Name != PermAuthManage {
			out = append(out, p.Name)
		}
	}
	return out
}

// Derivation is the permission set one configuration derives, plus the root
// administrators of that configuration. It is immutable after Derive returns, and it is
// one half of the httpapi configuration snapshot (design §3「一致性」).
type Derivation struct {
	roots      []string            // root administrator caller ids, sorted
	rootSet    map[string]struct{} // the same ids, for lookup
	users      []string            // permissions derived for the `users` subject
	perCaller  map[string][]string // caller id -> permissions derived for that caller
	deprecated []Deprecated        // deprecated capability keys still in use
}

// Deprecated is one deprecated capability key the configuration still uses. Caller names
// the caller that sets it for the per-caller bits, and is empty for the governance
// switches.
type Deprecated struct {
	Key    string
	Caller string
}

// Derive computes the root administrators and the config-derived grants of cfg (design
// §2 / §3).
//
// legacyToken reports whether the deployment's single server token is live — the httpapi
// entry layer passes the effective token it authenticates with, which may come from the
// config, the environment or a flag and is therefore invisible to cfg. It only matters
// for a deployment that declares no callers at all: that `default` caller is the root
// administrator (design §2). Which deployments have a root at all is
// config.ServerConfig.HasRootAdmin — an allow_empty_token deployment that declares no
// callers has none, so the id is not invented for it.
//
// A nil cfg yields an empty derivation: no root, no grant.
func Derive(cfg *config.ServerConfig, legacyToken bool) *Derivation {
	d := &Derivation{rootSet: map[string]struct{}{}, perCaller: map[string][]string{}}
	if cfg == nil {
		return d
	}
	if cfg.HasRootAdmin() {
		if ids := cfg.RootAdminCallers(); len(ids) > 0 {
			d.roots = append(d.roots, ids...)
		} else if legacyToken {
			// The single-token deployment (design §2): `default` is the root.
			d.roots = append(d.roots, DefaultCallerID)
		}
		slices.Sort(d.roots)
		for _, id := range d.roots {
			d.rootSet[id] = struct{}{}
		}
	}

	g := cfg.Governance
	// design §3, answer row: closed -> `users`; open -> the can_answer callers.
	if g.RequireAnswerCapability {
		d.grantToCapable(cfg, answerGroup, func(cc config.CallerConfig) bool { return cc.CanAnswer })
		// design §3, `session.reply.others` row: open -> the can_answer callers.
		d.grantToCapable(cfg, replyGroup, func(cc config.CallerConfig) bool { return cc.CanAnswer })
	} else {
		d.users = append(d.users, answerGroup...)
	}
	// design §3, `job.attach` row.
	if g.RequireAttachCapability {
		d.grantToCapable(cfg, attachGroup, func(cc config.CallerConfig) bool { return cc.CanAttach })
	} else {
		d.users = append(d.users, attachGroup...)
	}
	// design §3, `tunnel.open` row.
	if g.RequireTunnelCapability {
		d.grantToCapable(cfg, tunnelGroup, func(cc config.CallerConfig) bool { return cc.CanTunnel })
	} else {
		d.users = append(d.users, tunnelGroup...)
	}
	// design §3, admin row: closed -> `users`; open -> only the root administrator, which
	// already has everything, so nothing is granted here.
	if !g.RequireAdminCapability {
		d.users = append(d.users, adminGroup...)
	}
	d.users = catalogOrder(d.users)
	for id, names := range d.perCaller {
		d.perCaller[id] = catalogOrder(names)
	}

	d.deprecated = deprecatedKeys(cfg)
	return d
}

// grantToCapable grants perms to every DECLARED caller the predicate accepts. A declared
// caller without a resolvable token cannot authenticate and its grant is inert; the
// derivation stays a pure function of the configuration.
func (d *Derivation) grantToCapable(cfg *config.ServerConfig, perms []string, ok func(config.CallerConfig) bool) {
	for _, cc := range cfg.Callers {
		if cc.ID == "" || !ok(cc) {
			continue
		}
		d.perCaller[cc.ID] = append(d.perCaller[cc.ID], perms...)
	}
}

// deprecatedKeys reports the capability bits and governance switches of the old style
// that this configuration still uses (G032: they are marked DEPRECATED in
// internal/config/model.go and are removed in DeprecatedRemoveIn). can_admin is NOT one
// of them: it is what names the root administrator.
func deprecatedKeys(cfg *config.ServerConfig) []Deprecated {
	var out []Deprecated
	bits := []struct {
		key string
		set func(config.CallerConfig) bool
	}{
		{"server.callers[].can_answer", func(cc config.CallerConfig) bool { return cc.CanAnswer }},
		{"server.callers[].can_attach", func(cc config.CallerConfig) bool { return cc.CanAttach }},
		{"server.callers[].can_tunnel", func(cc config.CallerConfig) bool { return cc.CanTunnel }},
	}
	for _, bit := range bits {
		for _, cc := range cfg.Callers {
			if bit.set(cc) {
				out = append(out, Deprecated{Key: bit.key, Caller: cc.ID})
			}
		}
	}
	gates := []struct {
		key string
		on  bool
	}{
		{"server.governance.require_answer_capability", cfg.Governance.RequireAnswerCapability},
		{"server.governance.require_admin_capability", cfg.Governance.RequireAdminCapability},
		{"server.governance.require_attach_capability", cfg.Governance.RequireAttachCapability},
		{"server.governance.require_tunnel_capability", cfg.Governance.RequireTunnelCapability},
	}
	for _, gate := range gates {
		if gate.on {
			out = append(out, Deprecated{Key: gate.key})
		}
	}
	return out
}

// Roots returns the caller ids of the root administrators, sorted. Empty means the
// deployment has none (design: the permission page is read-only then).
func (d *Derivation) Roots() []string { return slices.Clone(d.roots) }

// IsRoot reports whether callerID is a root administrator, which holds every permission
// and is the only subject that may manage grants.
func (d *Derivation) IsRoot(callerID string) bool {
	if d == nil || callerID == "" {
		return false
	}
	_, ok := d.rootSet[callerID]
	return ok
}

// Users returns the permissions the configuration derives for the `users` subject — the
// grants that hold for every user-class caller — in design §1 order.
func (d *Derivation) Users() []string {
	if d == nil {
		return nil
	}
	return slices.Clone(d.users)
}

// For returns the permissions the configuration derives for callerID itself, without the
// `users` subject and without the root administrator's everything, in design §1 order.
func (d *Derivation) For(callerID string) []string {
	if d == nil {
		return nil
	}
	return slices.Clone(d.perCaller[callerID])
}

// Effective returns the config-derived permissions of callerID: every permission for a
// root administrator, otherwise the union of the `users` grants and the caller's own, in
// design §1 order. Grant management (auth.manage) is never derived, so only a root
// administrator ever has it.
func (d *Derivation) Effective(callerID string) []string {
	if d == nil {
		return nil
	}
	if d.IsRoot(callerID) {
		out := make([]string, 0, len(catalog))
		for _, p := range catalog {
			out = append(out, p.Name)
		}
		return out
	}
	out := make([]string, 0, util.CapSum(len(d.users), len(d.perCaller[callerID])))
	out = append(out, d.users...)
	out = append(out, d.perCaller[callerID]...)
	return catalogOrder(out)
}

// Has reports whether the configuration derives permission perm for callerID (see
// Effective).
func (d *Derivation) Has(callerID, perm string) bool {
	return slices.Contains(d.Effective(callerID), perm)
}

// Deprecations returns the deprecated capability keys the configuration still uses
// (G032 hint list, in a stable order).
func (d *Derivation) Deprecations() []Deprecated {
	if d == nil {
		return nil
	}
	return slices.Clone(d.deprecated)
}

// catalogOrder dedupes names and sorts them into design §1 (catalog) order, so every
// derivation result has one canonical order no matter how its groups were assembled.
func catalogOrder(names []string) []string {
	seen := make(map[string]struct{}, len(names))
	out := make([]string, 0, len(names))
	for _, p := range catalog {
		if !slices.Contains(names, p.Name) {
			continue
		}
		if _, dup := seen[p.Name]; dup {
			continue
		}
		seen[p.Name] = struct{}{}
		out = append(out, p.Name)
	}
	return out
}
