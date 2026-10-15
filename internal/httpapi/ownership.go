package httpapi

import "github.com/gookit/rux/v2"

// Resource ownership (gofer-hxld, closed out by plan T1.5i).
//
// Some resources record WHO created them — a job's caller_id, a transfer's caller_id, a
// schedule's stored request caller_id — and the handlers used to let any authenticated user
// caller read and act on everyone's. The rule they share is 「the OWNER — the caller id the
// server itself stamped at create time, never a value the request supplied — needs nothing;
// anyone else needs the permission of the domain's catalog row」, and each domain states it in
// its own rule file (authz_job_rules.go's ownerAccess, authz_session_rules.go's sessionAccess,
// authz_schedule_rules.go's mayManageSchedule, authz_xfer_rules.go's mayAccessXfer, …).
//
// T1.5a–i moved every handler onto those rules, so the two helpers this file grew during
// gofer-hxld are gone with the hand-written checks that called them: the owner-or-admin test of
// the schedule mask and the can_admin-based root test. The only survivor
// is the root test below, which the three callers that ask "is this the root administrator?"
// need in request shape — the plan supervisor bind (plan_handler.go), a terminal tool prompt
// (session_permission_handler.go) and a schedule's webhook secret (schedule_handler.go).

// callerIsRootAdmin reports whether the caller of this request is a root administrator.
//
// It is not a permission of its own: it answers design §3's root question — the callers the
// configuration names `can_admin: true`, or the legacy single-token `default` — through the
// derivation the whole permission model rests on (the snapshot's root set, decision.Root()),
// so the three callers above can only ever agree with a root-derived permission such as
// plan.supervise.others (design §3: 仅根). It is false for a worker token and for a job
// credential: "root" is a person's identity, and those credentials are confined by their own
// route tables (workercredential.go, jobcredential.go) rather than by resource ownership.
func (s *Server) callerIsRootAdmin(c *rux.Context) bool {
	if callerKindFromCtx(c) != callerKindUser {
		return false
	}
	return s.decisionOf(c).Root()
}
