package authz

import (
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// Truth-table test of plan T1.2 / design §3「配置推出的授权（真值表）」.
//
// Every cell of {4 switches} × {4 capability bits} × {declared callers, single-token,
// allow_empty_token} × {resource has an owner, someone else's, no owner} is decided
// twice: once by a copy of the pre-design helper functions (oldServer below — the very
// functions the handlers call today), once from the derived permissions (newDecide). The
// two must agree, except in the cells the design marks as an intended change: the root
// administrator holds every permission, so where the old gate said no, the derivation now
// says yes (design decision 4 / §3「用户裁决带来的行为变化」).
//
// The old copies exist ONLY as this reference: the plan deletes the originals once T1.5
// has migrated the handlers, and this file is then the only place that still states what
// the old behaviour was.

// oldServer carries the pre-design decisions: those helpers only ever read the server
// config, so the copies take the caller id (and the resource owner) explicitly instead of
// a rux context. The caller is always a user-class caller here — a worker or job
// credential reaches none of these decisions through the permission model.
type oldServer struct{ cfg *config.ServerConfig }

// callerMayAdmin — copy of internal/httpapi/server.go callerMayAdmin (its caller-kind
// check stays in the handler).
func (s oldServer) callerMayAdmin(caller string) bool {
	if s.cfg == nil || !s.cfg.Governance.RequireAdminCapability {
		return true
	}
	return s.cfg.CallerCanAdmin(caller)
}

// callerMayAnswer — copy of internal/httpapi/interaction_handler.go callerMayAnswer.
func (s oldServer) callerMayAnswer(caller string) bool {
	if s.cfg == nil || !s.cfg.Governance.RequireAnswerCapability {
		return true
	}
	return s.cfg.CallerCanAnswer(caller)
}

// humanReviewer — copy of internal/httpapi/job_handler.go humanReviewer, minus its worker
// refusal (these cells are user callers): it is callerMayAnswer.
func (s oldServer) humanReviewer(caller string) bool { return s.callerMayAnswer(caller) }

// canWakeJob — copy of internal/job/wakeup.go canWakeJob. The job-id branch is for job
// credentials (a job waking itself); caller is never that id in these cells.
func (s oldServer) canWakeJob(jobID, jobOwner, caller string) bool {
	if caller != "" && caller == jobID {
		return true
	}
	if jobOwner != "" && caller == jobOwner {
		return true
	}
	if s.cfg == nil || !s.cfg.Governance.RequireAnswerCapability {
		return true
	}
	return s.cfg.CallerCanAnswer(caller)
}

// sessionMayAnswer — copy of internal/httpapi/session_handler.go sessionMayAnswer, minus
// its worker refusal and the session lookup (owner passed in).
func (s oldServer) sessionMayAnswer(sessionOwner, caller string) bool {
	if sessionOwner == "" || sessionOwner == caller {
		return true
	}
	return s.cfg != nil && s.cfg.Governance.RequireAnswerCapability && s.cfg.CallerCanAnswer(caller)
}

// callerMayAttach — copy of internal/httpapi/attach_ticket_handler.go callerMayAttach.
func (s oldServer) callerMayAttach(jobOwner, caller string) bool {
	if s.cfg != nil && s.cfg.Governance.RequireAttachCapability &&
		!s.cfg.CallerCanAttach(caller) && !s.cfg.CallerCanAdmin(caller) {
		return false
	}
	if jobOwner == "" {
		return s.cfg != nil && s.cfg.CallerCanAdmin(caller)
	}
	return jobOwner == caller || (s.cfg != nil && s.cfg.CallerCanAdmin(caller))
}

// mayOpenTunnel — copy of the gate in internal/httpapi/tunnel_handler.go tunnelConnect.
func (s oldServer) mayOpenTunnel(caller string) bool {
	return !(s.cfg != nil && s.cfg.Governance.RequireTunnelCapability && !s.cfg.CallerCanTunnel(caller))
}

// callerOwnsOrAdmins — copy of internal/httpapi/ownership.go callerOwnsOrAdmins: the
// owner of the resource, or the can_admin bit. This is the design's「严格检查的那组」,
// independent of any governance switch.
func (s oldServer) callerOwnsOrAdmins(owner, caller string) bool {
	if caller == owner {
		return true
	}
	return s.cfg != nil && s.cfg.CallerCanAdmin(caller)
}

// Decision points compared cell by cell. Each one is a question an old helper and a
// derived permission answer the same way.
const (
	actAdmin   = "admin write (config.write)"
	actAnswer  = "answer an interaction"
	actReview  = "review (accept/reject/approve) a job"
	actWake    = "wake a job"
	actSession = "reply to a terminal session"
	actAttach  = "attach to a job"
	actTunnel  = "open a tunnel"
	actStrict  = "delete/redact a job (strict admin)"
)

var truthActions = []string{actAdmin, actAnswer, actReview, actWake, actSession, actAttach, actTunnel, actStrict}

// ownership is how a resource relates to the caller under test. The empty caller id of
// an anonymous deployment makes it a value of its own: an EMPTY OWNER is the no-owner
// case, never "the anonymous caller's own resource".
type ownership string

const (
	ownResource    ownership = "own resource"
	othersResource ownership = "someone else's resource"
	noOwner        ownership = "resource without an owner"
)

// oldDecide is the pre-design answer for one cell.
func oldDecide(s oldServer, act, caller string, own ownership, ownerID string) bool {
	owner := ownerID
	if own == ownResource {
		owner = caller
	}
	switch act {
	case actAdmin:
		return s.callerMayAdmin(caller)
	case actAnswer:
		return s.callerMayAnswer(caller)
	case actReview:
		return s.humanReviewer(caller)
	case actWake:
		return s.canWakeJob("job-1", owner, caller)
	case actSession:
		return s.sessionMayAnswer(owner, caller)
	case actAttach:
		return s.callerMayAttach(owner, caller)
	case actTunnel:
		return s.mayOpenTunnel(caller)
	case actStrict:
		return s.callerOwnsOrAdmins(owner, caller)
	}
	panic("unknown action " + act)
}

// newDecide is the derived answer for the same cell: what T1.4's route declaration and
// T1.5's InHandler check ask for. The no-owner rules of design §1 are in the switch — a
// terminal session without an owner treats every user caller as its owner, a job without
// an owner needs the `.others` permission (which only the root administrator has).
func newDecide(d *Derivation, act, caller string, own ownership, ownerID string) bool {
	switch own {
	case ownResource:
		switch act {
		case actWake, actSession, actStrict:
			return true // the caller's own resource needs no permission
		case actAttach:
			return d.Has(caller, PermJobAttach)
		}
	case noOwner:
		switch act {
		case actSession:
			return true // any user-class caller answers for an ownerless session
		case actAttach:
			return d.Has(caller, PermJobAttachOthers)
		case actStrict:
			// The old owner test is a plain comparison, so the empty owner belongs to
			// the empty caller id of an anonymous deployment; the design's no-owner list
			// says nothing about redact/delete, so keep that (and root, via the
			// `.others` permission, for every other caller).
			return ownerID == caller || d.Has(caller, PermJobDeleteOthers)
		}
	case othersResource:
		// The `.others` permission of the action, below.
	}
	switch act {
	case actAdmin:
		return d.Has(caller, PermConfigWrite)
	case actAnswer:
		return d.Has(caller, PermInteractionAnswer)
	case actReview:
		return d.Has(caller, PermJobReview)
	case actWake:
		return d.Has(caller, PermJobWakeOthers)
	case actSession:
		return d.Has(caller, PermSessionReplyOthers)
	case actAttach:
		return d.Has(caller, PermJobAttachOthers)
	case actTunnel:
		return d.Has(caller, PermTunnelOpen)
	case actStrict:
		return d.Has(caller, PermJobDeleteOthers)
	}
	panic("unknown action " + act)
}

// TestDeriveTruthTableMatchesOldDecisions is the cell-by-cell comparison.
func TestDeriveTruthTableMatchesOldDecisions(t *testing.T) {
	t.Parallel()

	gateSets := []struct {
		name string
		set  func(*config.GovernanceConfig)
	}{
		{"answer-gate", func(g *config.GovernanceConfig) { g.RequireAnswerCapability = true }},
		{"admin-gate", func(g *config.GovernanceConfig) { g.RequireAdminCapability = true }},
		{"attach-gate", func(g *config.GovernanceConfig) { g.RequireAttachCapability = true }},
		{"tunnel-gate", func(g *config.GovernanceConfig) { g.RequireTunnelCapability = true }},
	}
	bitSets := []struct {
		name string
		set  func(*config.CallerConfig)
	}{
		{"can_answer", func(c *config.CallerConfig) { c.CanAnswer = true }},
		{"can_admin", func(c *config.CallerConfig) { c.CanAdmin = true }},
		{"can_attach", func(c *config.CallerConfig) { c.CanAttach = true }},
		{"can_tunnel", func(c *config.CallerConfig) { c.CanTunnel = true }},
	}

	type scenario struct {
		name   string
		cfg    *config.ServerConfig
		legacy bool
		caller string
	}
	var scenarios []scenario
	for _, gate := range gateSets {
		// Shape 3: allow_empty_token deployment — no callers, no token, one anonymous
		// caller. No root administrator (design §3).
		anon := &config.ServerConfig{AllowEmptyToken: true}
		gate.set(&anon.Governance)
		scenarios = append(scenarios, scenario{"anonymous/" + gate.name, anon, false, ""})
		// Shape 2: single-token deployment — no callers, so `default` is the root.
		single := &config.ServerConfig{Token: "single-token"}
		gate.set(&single.Governance)
		scenarios = append(scenarios, scenario{"single-token/" + gate.name, single, true, DefaultCallerID})
		// Shape 1: declared callers — `bob` holds exactly one capability bit.
		for _, bit := range bitSets {
			cc := config.CallerConfig{ID: "bob", Token: "tok-bob"}
			bit.set(&cc)
			cfg := &config.ServerConfig{Token: "single-token", Callers: []config.CallerConfig{cc}}
			gate.set(&cfg.Governance)
			scenarios = append(scenarios, scenario{"callers/" + gate.name + "+" + bit.name, cfg, true, "bob"})
		}
	}

	var cells, diffs int
	for _, sc := range scenarios {
		d := Derive(sc.cfg, sc.legacy)
		old := oldServer{cfg: sc.cfg}
		isRoot := d.IsRoot(sc.caller)

		ownerships := []struct {
			name  string
			kind  ownership
			owner string
		}{
			{"own resource", ownResource, sc.caller},
			{"someone else's resource", othersResource, "other-caller"},
			{"resource without an owner", noOwner, ""},
		}
		if sc.caller == "" {
			// The anonymous caller id IS the empty owner, so its "own" cells would be a
			// duplicate of the no-owner ones.
			ownerships = ownerships[1:]
		}
		for _, own := range ownerships {
			for _, act := range truthActions {
				cells++
				got := oldDecide(old, act, sc.caller, own.kind, own.owner)
				want := newDecide(d, act, sc.caller, own.kind, own.owner)
				// The only allowed difference is the root administrator gaining a
				// permission the old gate refused (design §3「用户裁决带来的行为变化」).
				expectedDiff := isRoot && !got
				if (got != want) != expectedDiff {
					t.Errorf("%s / caller=%q root=%v / %s / %s: old=%v new=%v, want new=%v (the new decision must %s the old one)",
						sc.name, sc.caller, isRoot, act, own.name, got, want, expectedDiff, map[bool]string{true: "differ from", false: "equal"}[expectedDiff])
					continue
				}
				if got != want {
					diffs++
					t.Logf("intended change: %s / caller=%q / %s / %s: old=%v new=%v",
						sc.name, sc.caller, act, own.name, got, want)
				}
			}
		}
	}
	if cells < 500 {
		t.Errorf("compared only %d cells: the table lost a dimension", cells)
	}
	if diffs == 0 {
		t.Error("no intended difference was seen: the root administrator's「全部权限」is not being exercised")
	}
	t.Logf("compared %d cells, %d intended differences (root administrator)", cells, diffs)
}
