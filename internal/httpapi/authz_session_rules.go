// The session-side handler rules of the permission model (design
// docs/design/2026-10-14-db-capabilities-and-session-grants-design.md §1 — the
// `session.read.others` / `session.reply.others` / `session.manage.others` /
// `session.permission.answer` rows — and 架构 / 测试兜底, plan T1.5b / T1.5c).
//
// Like the job rules of authz_job_rules.go these are InHandler rules: 操作自己的资源不需要
// 这类权限. A session's own owner needs nothing — and neither does ANY user-class caller on
// an OWNERLESS session, the design's 无主资源 rule (「无主终端会话：任何用户类调用方都视为
// owner」: there is nobody to compare against, and refusing would lock the human out of their
// own terminal). Everything else asks for the permission, which with the answer gate closed
// nobody derives, exactly like the pre-migration 「非 owner 一律被拒」.
//
// Only a user-class caller is governed by permissions (design §2「只对 user 类调用方计算」).
// A worker token and a job credential reach a route through their own tables —
// workercredential.go's whitelist and jobcredential.go's SEC-01 — so sessionAccess answers
// them what the pre-migration handler answered, arm by arm (sessionNonUser below): the
// catalog is never consulted for them.
//
// Plan T1.5b wrote the `session.reply.others` rule (mayReplyToSession) and is its first
// caller: answering a RELAY decision is 「向会话说话」. T1.5c migrates the terminal-session
// routes (say / messages / outbox / watches / turns / nudges / …) onto the rules here.

package httpapi

import (
	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/jobstore"
)

// sessionNonUser is what a rule answers a caller that is NOT governed by permissions (design
// §2: 权限只对 user 类调用方计算): a worker token and a job credential reach a session route
// through workercredential.go's whitelist / jobcredential.go's SEC-01, so each rule answers
// them exactly what the pre-migration handler answered. The three arms below are those
// answers; sessionAccess applies one of them and never consults the catalog for such a
// caller.
type sessionNonUser func(kind, owner, caller string) bool

// nonUserAsSessionMayAnswer is the arm of every route the pre-migration sessionMayAnswer
// gated (say / messages / outbox / deliver / relay / resume / the turn acks / watches /
// nudges): a worker token was refused outright — a worker runs jobs, it does not answer for
// a person — while a job credential acted as ITSELF: allowed on a session whose caller id is
// its own job id, and on an OWNERLESS one (CallerID == "", an empty-token server or a
// pre-owner row), refused on a person's session (CallerCanAnswer never applies to a job id).
func nonUserAsSessionMayAnswer(kind, owner, caller string) bool {
	if kind == callerKindWorker {
		return false
	}
	return owner == "" || owner == caller
}

// nonUserUnchanged is the arm of a route that checked NOTHING before the migration — the
// session reads and the three routes the design records as "no owner check": a worker token
// and a job credential keep exactly what their own tables admitted.
func nonUserUnchanged(string, string, string) bool { return true }

// nonUserNever is the arm of a route whose pre-migration check was 「只有人」: a worker token
// was refused outright, and a job credential is refused by SEC-01 before the handler runs.
func nonUserNever(string, string, string) bool { return false }

// sessionAccess is the shared body of the session InHandler rules: may this request act on
// a session owned by `owner`?
//
//	owner == ""      the session has no owner (an empty-token server, a pre-owner row):
//	                 every user-class caller counts as its owner
//	owner == caller  the caller's own session → the permissions in ownPerms are required
//	                 (usually none: 「操作自己的资源不需要这类权限」)
//	otherwise        someone else's session → every permission in othersPerms is required
//	nonUser          what a caller that is not user-class gets (see sessionNonUser); the
//	                 permission catalog is not consulted for it at all
func (s *Server) sessionAccess(c *rux.Context, owner string, ownPerms, othersPerms []string, nonUser sessionNonUser) bool {
	if kind := callerKindFromCtx(c); kind != callerKindUser {
		return nonUser != nil && nonUser(kind, owner, callerFromCtx(c))
	}
	if owner == "" || owner == callerFromCtx(c) {
		return s.can(c, ownPerms...)
	}
	return s.can(c, othersPerms...)
}

// mayReplyToSession is the `session.reply.others` rule for a session owned by owner (design
// §1: 向他人终端会话说话或读其待发内容 — say / messages / outbox / ack / deliver / relay /
// 中继类决策回答 / 目标为终端会话的 session-ask 与 report-request):
//
//	owner == ""      → true  (无主会话: every user-class caller counts as its owner)
//	owner == caller  → true  (the caller's own session)
//	otherwise        → the permission session.reply.others
//
// A worker token and a job credential are not governed by permissions (design §2), so they
// get the pre-migration sessionMayAnswer answer instead of a catalog lookup: the worker is
// refused, the job credential passes on its own or an ownerless session
// (nonUserAsSessionMayAnswer).
func (s *Server) mayReplyToSession(c *rux.Context, owner string) bool {
	return s.sessionAccess(c, owner, nil, []string{authz.PermSessionReplyOthers}, nonUserAsSessionMayAnswer)
}

// mayReadSession is the `session.read.others` rule for ONE session (design §1: 他人会话的
// 详情 / 消息 / tail / turn / nudge / 收件箱). The own session and an ownerless one need
// nothing; someone else's needs session.read.others.
//
// A worker token and a job credential keep the whole read surface: SEC-01's read rule
// (every GET) and the steward's read allowlist admitted them before the migration, when the
// route checked nothing at all.
func (s *Server) mayReadSession(c *rux.Context, owner string) bool {
	return s.sessionAccess(c, owner, nil, []string{authz.PermSessionReadOthers}, nonUserUnchanged)
}

// mayListAllSessions is the FILTER form of session.read.others (the session list and the
// cross-session nudge list): a user-class caller without the permission is narrowed to its
// own rows instead of being refused (design §1「过滤型权限：没有权限时只返回自己的数据，不报
// 403」).
//
// A worker token and a job credential keep the whole list: they never held the permission
// before the migration either, and their own tables are what admitted them.
func (s *Server) mayListAllSessions(c *rux.Context) bool {
	if callerKindFromCtx(c) != callerKindUser {
		return true
	}
	return s.can(c, authz.PermSessionReadOthers)
}

// mayManageSession is the `session.manage.others` rule (design §1: 删除他人会话、开 / 释放
// turn): the caller's own session and an ownerless one need nothing, someone else's needs
// session.manage.others.
//
// A worker token keeps the routes it had: deleting a session, opening a turn and releasing
// one checked nothing before the migration (design: 「无 owner 校验；worker token 可删」).
func (s *Server) mayManageSession(c *rux.Context, owner string) bool {
	return s.sessionAccess(c, owner, nil, []string{authz.PermSessionManageOthers}, nonUserUnchanged)
}

// mayReleaseSessionTakeover is the `session.manage.others` rule of
// POST /v1/sessions/{sid}/release-takeover, whose pre-migration check was 「只有人」: a worker
// token was refused outright (a worker drives jobs, it does not hand a terminal back).
func (s *Server) mayReleaseSessionTakeover(c *rux.Context, owner string) bool {
	return s.sessionAccess(c, owner, nil, []string{authz.PermSessionManageOthers}, nonUserNever)
}

// mayAnswerSessionPermission is the `session.permission.answer` rule of
// POST /v1/sessions/{sid}/permissions/{id}/answer (design §5): a terminal tool prompt is the
// most direct 「替这个人做事」 there is, so the session's own owner (or anybody, on an
// ownerless session) answers it — and someone else's prompt needs session.permission.answer,
// which only a root administrator holds (design §3: the 严格检查 group).
//
// Only a user-class caller: a worker token and every job credential (the steward included)
// were refused before the migration, and — unlike opening and resolving a prompt, which are
// the HOOK's own actions and stay with the session owner — answering is the person's.
func (s *Server) mayAnswerSessionPermission(c *rux.Context, owner string) bool {
	return s.sessionAccess(c, owner, nil, []string{authz.PermSessionPermissionAnswer}, nonUserNever)
}

// sessionForAccess resolves the session a route acts on ({sid} in its path) FOR THE
// PERMISSION RULE. ok=false means the caller must stop: there is no such session, so no
// owner can be shown either — the handler then answers as the relay does (404), exactly
// what it answered before the migration (the relay lives in this server, so a session id
// the relay cannot resolve is unknown to the whole installation).
func (s *Server) sessionForAccess(c *rux.Context, action string) (jobstore.AgentSession, bool) {
	a, err := s.relay.Session(c.Param("sid"))
	if err != nil {
		writeError(c, relayStatus(err), action+" failed", err.Error())
		return jobstore.AgentSession{}, false
	}
	return a, true
}

// sessionOwnerOrUnknown is sessionForAccess for the rules applied to a session the service
// itself resolves anyway (a long poll, a detail read, an ack): an unresolvable session
// answers "" — nobody can be shown to be its owner, so only the permission answers. That
// fails closed rather than treating a vanished session as ownerless, the same choice
// mayAnswerDecision makes for a relay turn; the route's own 404 is unchanged.
func (s *Server) sessionOwnerOrUnknown(c *rux.Context) string {
	owner, _ := s.terminalSessionOwner(c.Param("sid"))
	return owner
}

// sessionOwnedBy reports whether the session sid counts as caller's own for a FILTER rule
// (the cross-session nudge list): the caller's session, or an OWNERLESS one, which every
// user-class caller counts as its own (design §1 无主资源). A session this server cannot
// resolve (the row is gone, or no relay service is wired) is nobody's, so the row is
// filtered out — fail closed instead of leaking it.
func (s *Server) sessionOwnedBy(sid, caller string) bool {
	owner, ok := s.terminalSessionOwner(sid)
	return ok && (owner == "" || owner == caller)
}

// denySessionAction answers a caller the session rules above refused. The body names the
// missing permission and where to get it, exactly like the route gate's 403 (design §3
// 「403 文案分批」).
func (s *Server) denySessionAction(c *rux.Context, perms ...string) {
	s.writePermissionDenied(c, perms...)
}
