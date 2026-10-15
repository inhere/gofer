// The handler-side rules of the configuration domain — plan T1.5h of
// docs/plans/2026-10-14-permission-catalog-plan.md, design
// docs/design/2026-10-14-db-capabilities-and-session-grants-design.md §1 (the catalog) and §3
// (the admin group of the derivation) — beside the job rules (authz_job_rules.go), the session
// rules (authz_session_rules.go), the plan rules (authz_plan_rules.go), the schedule rules
// (authz_schedule_rules.go) and the transfer rules (authz_xfer_rules.go).
//
// Almost every route of this domain is a PLAIN requirement the route gate enforces — the
// configuration writes, the project / rule / skill writes, the worker management and upgrade,
// the release check and the update cancel (authz_routes.go, the T1.5h block). Only two routes
// have a user side the handler decides, because only the REQUEST BODY says whether the second
// permission of the catalog row applies:
//
//	POST /v1/workers/{id}/upgrade   worker.upgrade, and worker.upgrade.upload when the body
//	                                asks for `source=staged` — that source runs the binary an
//	                                earlier PUT .../upgrade/file stored (design §1:
//	                                「上传二进制；`source=staged` 也需要」).
//	POST /v1/update/apply           update.apply, and worker.upgrade when the body names the
//	                                workers the one-click update should upgrade along with the
//	                                server (design §1: 「带 workers 时另需 worker.upgrade」).
//
// Both are asked BEFORE anything is staged, handed over or restarted, so a refused request
// changes nothing (design §1「这些入口只在…时另需本权限」and the plan's「被拒的请求什么都不变」).
//
// The rest of this file is the four rules behind those two routes, plus the caller-KIND half of
// the admin gate. The permission itself is the route gate (Require(config.write) etc.), and the
// gate deliberately leaves a NON-user caller alone (design §2「只对 user 类调用方计算」): a worker
// token and a job credential are confined by workercredential.go's whitelist and
// jobcredential.go's SEC-01 tables, so they must not reach an administrative write through the
// gap the gate leaves. That is exactly the caller-kind half of the pre-migration admin gate
// (server.go). It is the only half of that test which survives the migration: its other half —
// 「with require_admin_capability OFF every caller passes, with the switch ON only a can_admin
// caller does」— is what the derived admin group says cell by cell (design §3).

package httpapi

import (
	"net/http"
	"strings"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
)

// adminUserOnly admits only a person (callerKindUser) to an administrative write of this
// domain. A worker transport token and a job credential are both refused, whatever they carry:
// the route gate does not judge a non-user caller at all (see the file comment above), so the
// kind check has to stay in the handler. It is an allowlist rather than a "not a job" denylist,
// so a future credential kind cannot slip through.
func adminUserOnly(c *rux.Context, what string) bool {
	if callerKindFromCtx(c) == callerKindUser {
		return true
	}
	writeError(c, http.StatusForbidden, "not permitted for this caller",
		"only a person's credential may "+what)
	return false
}

// mayUpgradeWorker is the first declared permission of POST /v1/workers/{id}/upgrade (design
// §1's `worker.upgrade` row): starting a remote upgrade. It is asked before the target worker is
// even looked up, so a caller without it learns nothing about this server's workers.
func (s *Server) mayUpgradeWorker(c *rux.Context) bool {
	if !s.can(c, authz.PermWorkerUpgrade) {
		s.writePermissionDenied(c, authz.PermWorkerUpgrade)
		return false
	}
	return true
}

// mayRunStagedBinary is the SECOND declared permission of the same route, and only the request
// body says whether it applies: `source=staged` hands the worker the binary an earlier PUT
// .../upgrade/file stored — the dangerous half of the operation — so it needs
// worker.upgrade.upload as well (design §1:「上传二进制；`source=staged` 也需要」). source is the
// raw body value, so the spellings the handler accepts around the literal are decided exactly
// the way startWorkerUpgrade decides them.
func (s *Server) mayRunStagedBinary(c *rux.Context, source string) bool {
	if strings.TrimSpace(source) != workerUpgradeSourceStaged {
		return true
	}
	if !s.can(c, authz.PermWorkerUpgradeUpload) {
		s.writePermissionDenied(c, authz.PermWorkerUpgradeUpload)
		return false
	}
	return true
}

// mayApplyUpdate is the first declared permission of POST /v1/update/apply (design §1's
// `update.apply` row): the one-click update itself. Asked as soon as the caller is known to be a
// person, so it does not depend on, and does not reveal, whether this server can update itself.
func (s *Server) mayApplyUpdate(c *rux.Context) bool {
	if !s.can(c, authz.PermUpdateApply) {
		s.writePermissionDenied(c, authz.PermUpdateApply)
		return false
	}
	return true
}

// mayUpgradeWorkersAlong is the SECOND declared permission of that route, decided by the body:
// naming workers to upgrade along with the server needs worker.upgrade as well — the same
// permission `gofer worker upgrade` asks, because the server hands those workers its own binary
// (design §1:「带 workers 时另需 worker.upgrade」). named is the number of workers the body carries;
// the handler validates the ids afterwards, so a refused request changes nothing.
func (s *Server) mayUpgradeWorkersAlong(c *rux.Context, named int) bool {
	if named <= 0 {
		return true
	}
	if !s.can(c, authz.PermWorkerUpgrade) {
		s.writePermissionDenied(c, authz.PermWorkerUpgrade)
		return false
	}
	return true
}
