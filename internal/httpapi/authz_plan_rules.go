// The handler-side rules of the plan domain — plans, their checklist items and the work
// items that may become one (design
// docs/design/2026-10-14-db-capabilities-and-session-grants-design.md §1 — the「无主资源」rules —
// and 架构 / 测试兜底, plan T1.5d), beside the job rules (authz_job_rules.go) and the
// session rules (authz_session_rules.go). This file holds the one implementation of the
// domain's ownership rules, so no plan handler repeats an owner comparison of its own.
//
// A plan is a collaboration CONTAINER (design §1 「计划是协作容器」): the READS stay Public —
// any user-class caller sees every plan — while a WRITE belongs to the plan's OWNER (the
// caller id the server stamped at creation), or to a holder of `plan.manage.others`
// (种入，所以默认部署与迁移前一样宽). A TODO has no owner of its own: the owner of the PLAN it
// lives in is the resource owner of every todo rule.
//
// Three further permissions appear in the domain:
//
//	plan.supervise.others  (仅根) binding a supervisor to someone else's plan, or binding a
//	                       session that belongs to someone else — the permission form of the
//	                       old strict-admin check (gofer-ka6r).
//	job.submit             the entries whose request starts a job: plan run / resume, the
//	                       PATCH that flips the per-plan leader switch ON, the todo PATCH
//	                       that dispatches (a move to ready, or an assignee) and the explicit
//	                       todo dispatch (design §1's job.submit row).
//	plan.manage.others     everything else that writes: handoff, attaching a job, adding an
//	                       item, pausing, and a work item's conversion into an item of
//	                       someone else's plan.
//
// Only a user-class caller is governed by permissions (design §2「只对 user 类调用方计算」). The
// non-user callers keep what they had before the migration: no plan handler ever checked a
// caller's kind, so these rules answer `true` for them, while SEC-01's own target rules
// (jobMaySetTodo, the handoff plan check) and workercredential.go's whitelist — which holds no
// plan route — are what actually narrow a worker token or a job credential here. Only
// `maySupervisePlan` is stricter, because the old strict-admin check it replaces was false for
// a non-user caller.

package httpapi

import (
	"net/http"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
)

// mayManagePlan is design `InHandler(plan.manage.others, deny)`: the plan's own owner needs
// nothing (「操作自己的资源不需要这类权限」), anyone else needs plan.manage.others.
func (s *Server) mayManagePlan(c *rux.Context, owner string) bool {
	if callerKindFromCtx(c) != callerKindUser {
		return true
	}
	if owner == callerFromCtx(c) {
		return true
	}
	return s.can(c, authz.PermPlanManageOthers)
}

// maySupervisePlan is design §1's `plan.supervise.others`: the caller may bind a plan's
// supervisor session, or a session that is not its own. It is a user-class permission —
// the strict-admin test it replaces was false for a worker token and a job credential —
// and 仅根, so by default only a root administrator holds it.
func (s *Server) maySupervisePlan(c *rux.Context) bool {
	if callerKindFromCtx(c) != callerKindUser {
		return false
	}
	return s.can(c, authz.PermPlanSuperviseOthers)
}

// denyPlanAction answers a caller one of the rules above refused, naming the missing
// permission and where to get it — the same 403 the route gate writes.
func (s *Server) denyPlanAction(c *rux.Context, perms ...string) {
	s.writePermissionDenied(c, perms...)
}

// mayManagePlanOf is mayManagePlan for a route that names its plan by id only: it resolves
// the plan first. An unknown plan answers true, so the service keeps its own 404 (the
// answer names no resource either way).
func (s *Server) mayManagePlanOf(c *rux.Context, id string) bool {
	p, ok, err := s.jobs.Meta().GetPlan(id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "get plan failed", err.Error())
		return false
	}
	if !ok {
		return true
	}
	if !s.mayManagePlan(c, p.Owner) {
		s.denyPlanAction(c, authz.PermPlanManageOthers)
		return false
	}
	return true
}

// mayAdvancePlan is the rule of the two chain entries that start work — POST
// /v1/plans/{id}/run and …/resume (design §1 「plan run / resume」 of the job.submit row):
// someone else's plan needs plan.manage.others, and both need job.submit, because the
// entry dispatches the plan's ready items later in this same request. An unknown plan
// answers true here, so the service keeps its own 404 (the answer names no resource
// either way) and a caller that could not advance a real plan learns nothing new.
func (s *Server) mayAdvancePlan(c *rux.Context, id string) bool {
	p, ok, err := s.jobs.Meta().GetPlan(id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "get plan failed", err.Error())
		return false
	}
	if !ok {
		return true
	}
	if !s.mayManagePlan(c, p.Owner) {
		s.denyPlanAction(c, authz.PermPlanManageOthers)
		return false
	}
	if !s.can(c, authz.PermJobSubmit) {
		s.denyPlanAction(c, authz.PermJobSubmit)
		return false
	}
	return true
}

// todoPlanOwner resolves the resource owner of a todo rule: the owner of the plan the item
// lives in. known=false means there is no such todo — the handler then keeps its own
// "unknown todo" answer (404), exactly as before the migration; a todo whose plan is gone
// answers the empty owner (nothing owns it any more).
func (s *Server) todoPlanOwner(todoID string) (owner string, known bool, err error) {
	t, ok, err := s.jobs.Meta().GetTodo(todoID)
	if err != nil || !ok {
		return "", false, err
	}
	p, ok, err := s.jobs.Meta().GetPlan(t.PlanID)
	if err != nil {
		return "", false, err
	}
	if !ok {
		return "", true, nil
	}
	return p.Owner, true, nil
}

// mayManageTodo is design `InHandler(plan.manage.others, deny)` for a checklist item: the
// owner of the item's PLAN needs nothing, anyone else needs plan.manage.others. An unknown
// todo answers true, so the write path keeps its own 404.
func (s *Server) mayManageTodo(c *rux.Context, todoID string) bool {
	owner, known, err := s.todoPlanOwner(todoID)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "get todo failed", err.Error())
		return false
	}
	if !known {
		return true
	}
	if !s.mayManagePlan(c, owner) {
		s.denyPlanAction(c, authz.PermPlanManageOthers)
		return false
	}
	return true
}

// mayDispatchTodo is the rule of the two entries that may START a job — POST
// /v1/todos/{todo_id}/dispatch and the todo PATCH that dispatches (design §1: 「todo dispatch
// 与 PATCH（改为 ready 或改 assignee 即派发）」): the manage rule above, plus job.submit.
// Both are checked before the write, so a refused request changes nothing.
func (s *Server) mayDispatchTodo(c *rux.Context, todoID string) bool {
	if !s.mayManageTodo(c, todoID) {
		return false
	}
	if !s.can(c, authz.PermJobSubmit) {
		s.denyPlanAction(c, authz.PermJobSubmit)
		return false
	}
	return true
}
