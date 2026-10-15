// The handler-side rules of the transfer (xfer) domain — plan T1.5g of
// docs/plans/2026-10-14-permission-catalog-plan.md of design
// docs/design/2026-10-14-db-capabilities-and-session-grants-design.md §1 (the rows
// xfer.write and xfer.manage.others) — beside the job rules (authz_job_rules.go), the
// session rules (authz_session_rules.go), the plan rules (authz_plan_rules.go), the
// schedule rules (authz_schedule_rules.go) and the tunnel rules (authz_tunnel_rules.go).
//
// The domain's two permission rows, as implemented here and in the declaration table:
//
//	xfer.write            创建传输（在执行主机读写文件）— the design's 危险级 permission, 种入.
//	                      It is a plain requirement of the two create routes, so the route
//	                      gate enforces it and no handler rule lives here: the handlers keep
//	                      their own user-only refusal (a worker must not park bytes in the
//	                      server's staging area, and a job never creates a transfer).
//	xfer.manage.others    读取、下载、删除他人的传输 — 仅根, and deliberately NOT seeded
//	                      (design §3「有意不保持现状的三处」#1: 「xfer.manage.others 不种入：
//	                      他人的传输只有根管理员能读取、下载」). This is the domain's one
//	                      InHandler rule: it restates the "owner or root administrator" test
//	                      gofer-hxld introduced (the design's appendix A records the bypass it
//	                      closed — before it, any caller could download a finished transfer's
//	                      content), now as the permission a root administrator holds and a
//	                      migration-time narrowing can keep from a caller.
//
// 「操作自己的资源不需要这类权限」: the caller that INITIATED a transfer (the id the server
// stamped at create time, never a value the request supplied) reads, downloads and deletes it
// without any permission. The LIST is the filter form of the same permission — without it a
// caller sees only its own transfers instead of a 403, which is the answer gofer-hxld gave and
// keeps `gofer tool xfer ls` usable.
//
// An OWNERLESS transfer (an empty caller id — an allow_empty_token deployment, a pre-owner
// row) is nobody's, so reading, downloading and deleting it takes the same permission; that is
// the design's 「无主 xfer、schedule：只有根管理员可以操作他人那一类动作」, and it is what the
// pre-migration owner-or-administrator test answered for such a row (only the root administrator
// holds the permission, so an ownerless transfer stays root-only).
//
// Only a user-class caller is governed by permissions (design §2「只对 user 类调用方计算」). The
// non-user arms keep exactly the pre-migration answers — nothing here consults the catalog for
// them: the content route's worker branch is its own rule (a worker fetches the SOURCE of a put
// assigned to itself, workercredential.go's whitelist), and every other non-user path was
// refused by the old owner-or-administrator test, which is what nonUserOthers=false below
// reproduces for someone else's (and for an ownerless) transfer.

package httpapi

import (
	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
)

// mayAccessXfer is design `InHandler(xfer.manage.others, deny)` for one transfer whose
// initiating caller is owner: the owner's own transfer needs nothing, anyone else's needs
// xfer.manage.others (仅根).
func (s *Server) mayAccessXfer(c *rux.Context, owner string) bool {
	return s.ownerAccess(c, owner, nil, []string{authz.PermXferManageOthers}, false)
}

// denyXferAccess answers a caller that may not read, download or delete another caller's
// transfer, naming the permission — the same 403 body the route gate writes (design
// §3「403 文案分批」).
func (s *Server) denyXferAccess(c *rux.Context) {
	s.writePermissionDenied(c, authz.PermXferManageOthers)
}

// mayListAllXfers is the FILTER form of xfer.manage.others (design §1「过滤型权限：没有权限时
// 只返回自己的数据，不报 403」): without it the list is narrowed to the caller's own transfers
// rather than refused.
//
// A worker token and a job credential keep the pre-migration answer, which was the same
// narrowing — the old test was 「root administrator ? everything : only my own rows」 for every
// caller kind, and neither credential ever created a transfer.
func (s *Server) mayListAllXfers(c *rux.Context) bool {
	if callerKindFromCtx(c) != callerKindUser {
		return false
	}
	return s.can(c, authz.PermXferManageOthers)
}
