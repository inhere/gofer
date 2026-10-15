// The handler-side rules of the schedule domain — plan T1.5e (docs/plans/2026-10-14-permission-catalog-plan.md)
// of design docs/design/2026-10-14-db-capabilities-and-session-grants-design.md §1 (the rows
// `schedule.manage.others` and `job.submit`) — beside the job rules (authz_job_rules.go), the
// session rules (authz_session_rules.go) and the plan rules (authz_plan_rules.go). This file
// holds the one implementation of the domain's rule, so no schedule handler repeats an owner
// comparison of its own.
//
// A schedule records WHO CREATED IT inside its stored request (handleCreateSchedule stamps
// req.CallerID onto the request, and jobstore keeps that request as an opaque JSON string), so
// the owner is read back out of it by scheduleOwner — the same value the trigger_token masking
// has used since gofer-hxld. 「操作自己的资源不需要这类权限」: the creator's own schedule needs
// nothing, anyone else's needs `schedule.manage.others` (种入，所以迁移后的默认部署与迁移前一样
// 宽).
//
// Two entries make a schedule RUN — POST /v1/schedules/{id}/run-now and …/enable — and the
// design's job.submit row lists both (「schedules 创建 / run-now / enable」). run-now submits the
// stored request in this very request; enable switches the schedule's sweep on, after which the
// cron loop starts jobs with no further checkpoint, so the permission is asked at the switch
// itself. (POST /v1/schedules, the third entry of that row, is a plain requirement the route
// gate enforces — it is `Require(job.submit)` in the declaration table.)
//
// Only a user-class caller is governed by permissions (design §2「只对 user 类调用方计算」). The
// non-user arms keep what they had before the migration: neither a worker token nor a job
// credential reaches these handlers at all — workercredential.go's whitelist holds no schedule
// route, and SEC-01 (jobcredential.go) default-denies every schedule write — so a rule answers
// them the pre-migration answer (the handlers checked nothing) and their own tables stay the
// authority.
//
// The two READS of the domain (GET /v1/schedules and GET /v1/schedules/{id}) are declared
// Public and ask nothing of the caller: design §1's catalog row gives schedule.manage.others
// 「他人 schedule 的删除 / 启停 / 轮换 token / run-now」 — write actions, and unlike the
// xfer.manage.others row beside it no 「读取」 — while the webhook secret a read carries is masked
// per caller (scheduleToViewFor, gofer-hxld). Appendix A's draft column suggested gating them,
// and 附录 A 的「建议权限」列以正文为准.

package httpapi

import (
	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
)

// mayManageSchedule is design `InHandler(schedule.manage.others, deny)` for a schedule whose
// creator is owner: the creator's own schedule needs nothing, anyone else's needs
// schedule.manage.others. An OWNERLESS schedule — a row whose stored request carries no caller
// id, or one that cannot be decoded — is nobody's, so it takes the same branch; the permission
// is 种入, so a user-class caller that has not been narrowed keeps exactly the access it had
// before the migration.
//
// It answers the 403 itself (the same body the route gate writes), because every caller of it
// refuses the request at that point.
func (s *Server) mayManageSchedule(c *rux.Context, owner string) bool {
	if s.ownerAccess(c, owner, nil, []string{authz.PermScheduleManageOthers}, true) {
		return true
	}
	s.denyScheduleAction(c, authz.PermScheduleManageOthers)
	return false
}

// mayStartScheduleJobs is the joint rule of the two entries that make a schedule run
// (design §1's job.submit row): the manage rule above, plus job.submit on BOTH branches —
// someone else's schedule needs schedule.manage.others AND job.submit, the caller's own needs
// job.submit, because the entry starts work either way (「一切产生 job 的入口…只在这次请求会产生
// job 时另需本权限」). Both are decided before anything is written, so a refused request changes
// nothing.
func (s *Server) mayStartScheduleJobs(c *rux.Context, owner string) bool {
	if !s.mayManageSchedule(c, owner) {
		return false
	}
	if !s.can(c, authz.PermJobSubmit) {
		s.denyScheduleAction(c, authz.PermJobSubmit)
		return false
	}
	return true
}

// mayReadScheduleSecret is the trigger_token masking rule of the two Public reads
// (scheduleToViewFor): the schedule's OWNER reads the real secret, and so does a root
// administrator (callerIsRootAdmin — the root set of design §3「配置推出」，which is what
// gofer-hxld's owner-or-can_admin test asked). Everyone else gets the first four characters and
// stars.
//
// Holding `schedule.manage.others` deliberately does NOT unmask it: the token is a credential for
// an endpoint that asks nothing of its presenter (`POST /v1/schedules/{id}/trigger` runs the
// stored request in a channel of its own), so it is a secret of the schedule rather than a
// capability of its manager — a caller that may manage the schedule can rotate a fresh one, but
// it does not read someone else's out of a 200.
//
// A worker token and a job credential are always masked: they are not governed by permissions
// (design §2「只对 user 类调用方计算」)，也不是 owner 或根管理员。
func (s *Server) mayReadScheduleSecret(c *rux.Context, owner string) bool {
	if callerKindFromCtx(c) != callerKindUser {
		return false
	}
	if owner != "" && owner == callerFromCtx(c) {
		return true
	}
	return s.callerIsRootAdmin(c)
}

// denyScheduleAction answers a caller the rules above refused, naming the missing permission and
// where to get it — exactly like the route gate's 403 (design §3「403 文案分批」).
func (s *Server) denyScheduleAction(c *rux.Context, perms ...string) {
	s.writePermissionDenied(c, perms...)
}
