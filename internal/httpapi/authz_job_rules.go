// The handler-side rules of the permission model (design
// docs/design/2026-10-14-db-capabilities-and-session-grants-design.md §1 — the「无主资源」rules —
// and 架构 / 测试兜底, plan T1.5a–i).
//
// A route whose user side is declared `InHandler(perm…, deny|filter)` decides inside the
// handler, because the answer depends on the RESOURCE: 「操作自己的资源不需要这类权限」.
// This file holds the one implementation of those rules for the job domain, so no handler
// repeats an owner comparison of its own and the rules can be read in one place.
//
// Only a user-class caller is governed by permissions (design §2「只对 user 类调用方计算」). A
// worker token and a job credential reach a route through their own tables —
// workercredential.go's whitelist and jobcredential.go's SEC-01 — so for them the rules
// below answer what the pre-migration handler answered:
//
//   - a worker token reaches none of the job routes over HTTP (its whitelist is the
//     upgrade file, the transfer content and the assignable projects), so it never gets
//     here;
//   - a job credential reaches every GET and the few writes SEC-01 opens. On the plain
//     READ routes (no ownership check before) it read any job, so those pass
//     nonUserOthers=true; where the pre-migration check was an owner or capability test
//     (the attach/recording routes) it saw only its own job, so those pass false.

package httpapi

import (
	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/job"
)

// ownerAccess is the shared body of the InHandler rules: may this request act on a resource
// owned by `owner`?
//
//	own          the resource is the caller's own → the permissions in ownPerms are
//	             required (usually none: 「操作自己的资源不需要这类权限」)
//	others       someone else's resource → every permission in othersPerms is required
//	nonUserOthers  what a worker token / a job credential gets for someone else's
//	             resource (it is not governed by permissions — see the file header)
func (s *Server) ownerAccess(c *rux.Context, owner string, ownPerms, othersPerms []string, nonUserOthers bool) bool {
	own := owner == callerFromCtx(c)
	if callerKindFromCtx(c) != callerKindUser {
		if own {
			return true
		}
		return nonUserOthers
	}
	if own {
		return s.can(c, ownPerms...)
	}
	return s.can(c, othersPerms...)
}

// jobForAccess resolves the job a route acts on FOR THE PERMISSION RULE. ok=false means no
// such job: the handler then keeps its own "unknown job" answer (a 404 of its own, or the
// job package's sentinel). Service.Get reads the live state and falls back to the store, so
// a job that survived a restart resolves here too.
func (s *Server) jobForAccess(id string) (job.JobResult, bool) {
	return s.jobs.Get(id)
}

// denyJobAction answers a caller the job rules above refused. The body names the missing
// permission and where to get it, exactly like the route gate's 403 (design §3「403 文案分批」).
func (s *Server) denyJobAction(c *rux.Context, perms ...string) {
	s.writePermissionDenied(c, perms...)
}

// mayReadOwnedRow is the `InHandler(job.read.others, deny)` rule for any resource that
// records the caller id of its creator: a job, and a workflow header (whose owner is its
// CallerID). The own row needs nothing; someone else's needs job.read.others.
func (s *Server) mayReadOwnedRow(c *rux.Context, owner string) bool {
	return s.ownerAccess(c, owner, nil, []string{authz.PermJobReadOthers}, true)
}

// mayReadJob is design `InHandler(job.read.others, deny)` on one job.
func (s *Server) mayReadJob(c *rux.Context, res job.JobResult) bool {
	return s.mayReadOwnedRow(c, res.CallerID)
}

// mayManageJob is design `InHandler(job.manage.others, deny)`: resume / rebuild / say /
// end / title / worktree / deliver / pick / injecting an interaction.
//
// submit marks an entry that ALWAYS starts a job (resume, rebuild, a workflow pick): the
// design gives `job.submit` to every job-producing entry point 「只在这次请求会产生 job 时」,
// whether the source job is the caller's own or not, so it is required on both branches.
func (s *Server) mayManageJob(c *rux.Context, res job.JobResult, submit bool) bool {
	return s.mayManageOwnedRow(c, res.CallerID, submit)
}

// mayManageOwnedRow is the rule above for any resource that records its creator's caller id
// (a job, a workflow header).
func (s *Server) mayManageOwnedRow(c *rux.Context, owner string, submit bool) bool {
	if submit {
		return s.ownerAccess(c, owner,
			[]string{authz.PermJobSubmit},
			[]string{authz.PermJobManageOthers, authz.PermJobSubmit}, false)
	}
	return s.ownerAccess(c, owner, nil, []string{authz.PermJobManageOthers}, false)
}

// mayCancelJob is design `InHandler(job.cancel.others, deny)`: cancelling a job, a workflow
// or a retry.
func (s *Server) mayCancelJob(c *rux.Context, res job.JobResult) bool {
	return s.mayCancelOwnedRow(c, res.CallerID)
}

// mayCancelOwnedRow is the rule above for any resource that records its creator's caller id.
func (s *Server) mayCancelOwnedRow(c *rux.Context, owner string) bool {
	return s.ownerAccess(c, owner, nil, []string{authz.PermJobCancelOthers}, false)
}

// mayDeleteJob is design `InHandler(job.delete.others, deny)`: deleting or redacting a job
// (the design's 严格检查的那组 — job.delete.others only belongs to the root administrator).
func (s *Server) mayDeleteJob(c *rux.Context, res job.JobResult) bool {
	return s.ownerAccess(c, res.CallerID, nil, []string{authz.PermJobDeleteOthers}, false)
}

// mayWakeJob is design `InHandler(job.wake.others, deny)`: registering / re-arming /
// removing a wakeup on a job. submit marks the two entries the design lists as job.submit
// entry points (wakeups 创建与修改: registering one and re-arming it).
func (s *Server) mayWakeJob(c *rux.Context, res job.JobResult, submit bool) bool {
	if submit {
		return s.ownerAccess(c, res.CallerID,
			[]string{authz.PermJobSubmit},
			[]string{authz.PermJobWakeOthers, authz.PermJobSubmit}, false)
	}
	return s.ownerAccess(c, res.CallerID, nil, []string{authz.PermJobWakeOthers}, false)
}

// mayAttachJob is design §1's attach rule (the pty recording / pty session routes):
//
//	the caller's own job → job.attach        (the require_attach_capability gate, config-sourced)
//	someone else's job  → job.attach.others (root only)
//	a job without owner → job.attach.others (design §1「无主 job 的 attach 与录像：只有根管理员」)
//
// The own-job branch requires the permission rather than short-circuiting to true, because
// job.attach is exactly the old attach gate: with require_attach_capability off (the
// shipped default) the `users` subject holds it, with the switch on only the callers
// declaring can_attach do.
func (s *Server) mayAttachJob(c *rux.Context, res job.JobResult) bool {
	caller := callerFromCtx(c)
	own := res.CallerID != "" && res.CallerID == caller
	if callerKindFromCtx(c) != callerKindUser {
		// A worker token and a job credential are not governed by permissions: theirs is the
		// only job they may read (SEC-01 admitted the route itself).
		return own
	}
	if own {
		return s.can(c, authz.PermJobAttach)
	}
	return s.can(c, authz.PermJobAttachOthers)
}

// denyAttachJob writes the 403 for mayAttachJob, naming the permission that is missing:
// job.attach for the caller's own job (the attach gate), job.attach.others otherwise.
func (s *Server) denyAttachJob(c *rux.Context, res job.JobResult) {
	if res.CallerID != "" && res.CallerID == callerFromCtx(c) {
		s.denyJobAction(c, authz.PermJobAttach)
		return
	}
	s.denyJobAction(c, authz.PermJobAttachOthers)
}

// mayListAllJobs is the FILTER form of job.read.others (the job and workflow lists): a
// user-class caller without the permission is narrowed to its own rows instead of being
// refused (design §1「过滤型权限：没有权限时只返回自己的数据，不报 403」).
//
// A worker token and a job credential keep the whole list: they never held the permission
// before the migration either, and SEC-01's read rule (every GET) is what admitted them.
func (s *Server) mayListAllJobs(c *rux.Context) bool {
	if callerKindFromCtx(c) != callerKindUser {
		return true
	}
	return s.can(c, authz.PermJobReadOthers)
}

// mayListAllPtySessions is the filter form of job.attach.others for the global pty session
// list: without it the caller sees only the sessions whose owner is its own caller id —
// which is what the pre-migration admin-bit test did, for a job credential as well.
func (s *Server) mayListAllPtySessions(c *rux.Context) bool {
	if callerKindFromCtx(c) != callerKindUser {
		return false
	}
	return s.can(c, authz.PermJobAttachOthers)
}

// mayScanAllSecrets is the filter form of secret.scan.all: without it a secret scan is
// narrowed to the caller's own jobs (design §1: 跨调用方秘密扫描与脱敏, 过滤型).
func (s *Server) mayScanAllSecrets(c *rux.Context) bool {
	if callerKindFromCtx(c) != callerKindUser {
		return false
	}
	return s.can(c, authz.PermSecretScanAll)
}

// mayDispatchComment is the `InHandler(job.submit, deny)` rule of the comment routes that can
// start work — the job, plan and todo threads (design §1's job.submit row: 「评论 @ 派发」 — one of
// the entries that 「只在这次请求会产生 job 时另需本权限」).
//
// The dispatch question belongs to the JOB layer and is answered without writing anything there
// (CommentWouldDispatch / CommentAsJobWouldDispatch: the configuration and the target only), and
// the PERMISSION is asked HERE — before the comment is recorded, so a refused request leaves both
// the thread and the cluster untouched. A comment that would dispatch nothing asks nothing.
//
// Only a user-class caller is governed by permissions (design §2「只对 user 类调用方计算」): a
// worker token is refused by the handler itself before this rule runs (a comment can dispatch
// work, and a worker token is not an identity) and a job credential keeps exactly what SEC-01's
// table admitted it.
func (s *Server) mayDispatchComment(c *rux.Context, scope, scopeID, body string) bool {
	if callerKindFromCtx(c) != callerKindUser {
		return true
	}
	dispatches, err := s.commentDispatchTargets(c, scope, scopeID, body)
	if err != nil || len(dispatches) == 0 {
		// Nothing would be dispatched, or the question cannot be answered yet (an unknown target,
		// an empty body): the handler keeps its own answer for the comment, and no permission of
		// this entry applies.
		return true
	}
	if !s.can(c, authz.PermJobSubmit) {
		s.denyJobAction(c, authz.PermJobSubmit)
		return false
	}
	return true
}

// commentDispatchTargets asks the job layer which mentions of this request would start a job,
// through the entry the caller's credential speaks with: a job credential speaks as its job's
// agent (CommentAsJob), everyone else as themselves.
func (s *Server) commentDispatchTargets(c *rux.Context, scope, scopeID, body string) ([]string, error) {
	if jc, ok := jobCallerFromCtx(c); ok {
		return s.jobs.CommentAsJobWouldDispatch(scope, scopeID, jc.JobID, body)
	}
	author, kind := s.commentAuthor(c)
	return s.jobs.CommentWouldDispatch(scope, scopeID, author, kind, body)
}

// jobMayReview is the human sign-off rule of accept / reject / approve (GATE-01 S3: only a
// PERSON rules on a delivery), now stated as the permission job.review. The worker refusal
// stays in the handler's own review entry (jobReviewer, job_handler.go): a worker token is
// not a user-class caller and the permission model says nothing about it.
func (s *Server) jobMayReview(c *rux.Context) bool {
	return s.can(c, authz.PermJobReview)
}
