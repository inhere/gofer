// The session-side handler rules of the permission model (design
// docs/design/2026-10-14-db-capabilities-and-session-grants-design.md §1 — the
// `session.reply.others` row — and 架构 / 测试兜底, plan T1.5b / T1.5c).
//
// `session.reply.others` is 「向他人**终端会话**说话或读其待发内容」, and like the job rules of
// authz_job_rules.go it is an InHandler rule: 操作自己的资源不需要这类权限. The session's own
// owner needs nothing — and neither does ANY user-class caller on an OWNERLESS session, the
// design's 无主资源 rule (「无主终端会话：任何用户类调用方都视为 owner」: there is nobody to
// compare against, and refusing would lock the human out of their own terminal). Everything
// else asks for the permission, which with the answer gate closed nobody derives, exactly
// like the pre-migration 「非 owner 一律被拒」.
//
// Plan T1.5b is the first caller: answering a RELAY decision is 「向会话说话」 — the answer is
// injected back into the terminal — so POST /v1/decisions/{id}/answer follows this rule for
// that kind. T1.5c migrates the terminal-session routes (say / messages / outbox / …) onto
// it.

package httpapi

import (
	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
)

// mayReplyToSession is the `session.reply.others` rule for a session owned by owner:
//
//	owner == ""      → true  (无主会话: every user-class caller counts as its owner)
//	owner == caller  → true  (the caller's own session)
//	otherwise        → the permission session.reply.others
//
// A worker token and a job credential are not governed by permissions (design §2), and
// neither speaks for a person: the pre-migration rule refused a worker token on every
// session route, and a job credential only ever acted as itself, so both answer false for
// someone else's session.
func (s *Server) mayReplyToSession(c *rux.Context, owner string) bool {
	if callerKindFromCtx(c) != callerKindUser {
		return false
	}
	if owner == "" || owner == callerFromCtx(c) {
		return true
	}
	return s.can(c, authz.PermSessionReplyOthers)
}
