// The handler-side rules of the tunnel domain — plan T1.5f of
// docs/plans/2026-10-14-permission-catalog-plan.md of design
// docs/design/2026-10-14-db-capabilities-and-session-grants-design.md §1 (the rows
// tunnel.open, tunnel.hosted and tunnel.manage.others) — beside the job rules
// (authz_job_rules.go), the session rules (authz_session_rules.go), the plan rules
// (authz_plan_rules.go) and the schedule rules (authz_schedule_rules.go).
//
// The domain's permission rows, as implemented here and in the declaration table:
//
//	tunnel.open             opening a tunnel connection: the /v1/tunnels/connect endpoint
//	                        (its handler resolves the caller itself, see tunnel_handler.go)
//	                        and the in-process dial of a hosted forwarder, which is judged by
//	                        the STARTER (design §6「判定与登记都按启动者」).
//	tunnel.hosted           托管转发启停、写或删预设、导入预设 — every route of the hosted
//	                        surface, each of them 另需 tunnel.open (design §3「有意不保持现状的
//	                        三处」#2: 「tunnel.hosted 另需 tunnel.open：开关打开时，托管隧道不再
//	                        能绕过隧道权限」). The four plain ones are Require in the declaration
//	                        table — the gate decides them, the route asks the caller for nothing
//	                        more — and the STOP route is InHandler, because stopping someone
//	                        else's hosted forwarder is tunnel.manage.others.
//	tunnel.manage.others    停他人的转发与托管转发: stopping an external forwarder process that
//	                        somebody else registered, or a hosted forwarder somebody else started
//	                        (仅根 — the design's 严格检查 group).
//
// The lists (GET /v1/tunnels and GET /v1/tunnels/forwarders) stay Public — 「隧道与转发的列表为
// Public，保持现状」 (design §1) — and so do the forwarder REGISTRATION routes (register /
// heartbeat / goodbye), which are the forwarding process's own bookkeeping: the registry keeps
// them owner-only (tunnel.ForwarderRegistry), and the design's catalog gives that surface no
// permission of its own.
//
// Only a user-class caller is governed by permissions (design §2「只对 user 类调用方计算」). A
// worker token reaches no tunnel route at all (workercredential.go's whitelist holds none), and
// a job credential reaches only the reads (SEC-01 admits every GET and refuses every tunnel
// write), so the non-user arms keep exactly the pre-migration answers — which s.can returns
// true for, their own tables being the authority.

package httpapi

import (
	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
)

// mayOpenTunnel is design §1's `tunnel.open` for a CALLER ID rather than for a request: the
// tunnel endpoint authenticates outside the /v1 group (a WS handshake cannot use the JSON error
// envelope), and the hosted dial has no request at all, so both ask the permission in its
// caller-id form (like MayAnswerInteraction does for the browser-push buttons).
//
// It reads the live snapshot (configuration-derived grants) and the live database grants, so a
// configuration reload applies to the next dial.
func (s *Server) mayOpenTunnel(callerID string) bool {
	return s.effective(callerID).Has(authz.PermTunnelOpen)
}

// mayStopForwarder is design `InHandler(tunnel.manage.others, deny)` for the external forwarder
// registry: the caller that registered a forwarder needs nothing to stop it, someone else's
// needs tunnel.manage.others (仅根 — it used to be an owner-or-can_admin test).
func (s *Server) mayStopForwarder(c *rux.Context, owner string) bool {
	return s.ownerAccess(c, owner, nil, []string{authz.PermTunnelManageOthers}, false)
}

// denyForwarderStop answers a caller that may not stop another caller's forwarder.
func (s *Server) denyForwarderStop(c *rux.Context) {
	s.writePermissionDenied(c, authz.PermTunnelManageOthers)
}

// mayStopHosted is design `InHandler(tunnel.hosted + tunnel.open, tunnel.manage.others, deny)`
// for DELETE /v1/tunnels/hosted/{name}: the starter's own hosted forwarder needs tunnel.hosted
// and tunnel.open (the two permissions every hosted route asks), anyone else's needs
// tunnel.manage.others (仅根) — 「停他人的转发与托管转发」.
//
// A hosted forwarder whose starter is no longer a live caller takes the same branch: a legacy
// autostart preset recorded the preset's UpdatedBy as its starter (see StartHostedAutostart),
// and no caller id can equal it any more, so only a root administrator may stop it. That is the
// design's 「不是现存用户类调用方时按根管理员处理」.
func (s *Server) mayStopHosted(c *rux.Context, starter string) bool {
	return s.ownerAccess(c, starter,
		[]string{authz.PermTunnelHosted, authz.PermTunnelOpen},
		[]string{authz.PermTunnelManageOthers}, false)
}

// denyHostedStop names the permission the rule above refused: the hosted pair for the starter's
// own forwarder, tunnel.manage.others for somebody else's.
func (s *Server) denyHostedStop(c *rux.Context, starter string) {
	if starter == callerFromCtx(c) {
		s.writePermissionDenied(c, authz.PermTunnelHosted, authz.PermTunnelOpen)
		return
	}
	s.writePermissionDenied(c, authz.PermTunnelManageOthers)
}
