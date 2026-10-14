package httpapi

import "github.com/gookit/rux/v2"

// Resource ownership (gofer-hxld).
//
// Some resources record WHO created them — a job's caller_id, a transfer's caller_id,
// a schedule's stored request caller_id — and the handlers used to let any
// authenticated user caller read and act on everyone's. "Owner or root administrator"
// is the rule those handlers now share:
//
//   - the OWNER is the caller id the server itself stamped at create time, never a
//     value the request supplied;
//   - the root ADMINISTRATOR is the can_admin capability bit (Config.CallerCanAdmin).
//     The governance switch that makes that bit optional (require_admin_capability)
//     deliberately does NOT apply here: it decides whether a WRITE needs the bit, while
//     these routes ask "is this yours?", and a deployment that never turned the switch
//     on has no other way to name an administrator. This is the same rule
//     job_delete_handler.go / job_redact_handler.go already apply to a job.
//
// A worker or job credential is never an owner: "own" is a person's identity, and those
// credentials are confined by their own route tables (workercredential.go,
// jobcredential.go) rather than by resource ownership.

// callerOwnsOrAdmins reports whether the caller owns the resource created by owner, or
// is a root administrator.
func (s *Server) callerOwnsOrAdmins(c *rux.Context, owner string) bool {
	if callerKindFromCtx(c) != callerKindUser {
		return false
	}
	if callerFromCtx(c) == owner {
		return true
	}
	return s.callerIsRootAdmin(c)
}

// callerIsRootAdmin reports whether the caller carries the can_admin capability bit.
// Unlike callerMayAdmin it ignores governance.require_admin_capability (see above) and
// is false for a worker / job credential. It is the context-shaped form of
// callerHoldsAdmin (session_owner_handler.go), which answers the same question about an
// arbitrary caller id (a resource's owner, say).
func (s *Server) callerIsRootAdmin(c *rux.Context) bool {
	if callerKindFromCtx(c) != callerKindUser {
		return false
	}
	return s.callerHoldsAdmin(callerFromCtx(c))
}
