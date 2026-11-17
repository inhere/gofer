package job

import (
	"fmt"
	"strings"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/wsproto"
)

// Permission tiers (JobRequest.PermissionTier): how closely a person watches an ACP
// session's tool use. A tier can only TIGHTEN the approval gate the project (and the agent's
// own permission_policy) already imposes — it never widens it, so "auto" is "no change".
const (
	// PermissionTierAuto leaves the gate as configured (the default).
	PermissionTierAuto = "auto"
	// PermissionTierAsk asks a person for what the project's auto-allow kinds do not cover.
	PermissionTierAsk = "ask"
	// PermissionTierStrict asks a person for every permission request.
	PermissionTierStrict = "strict"

	// permissionTierModeKey is the acp.modes key an agent maps its own "ask before acting"
	// mode under (claude-acp: default).
	permissionTierModeKey = "ask"
)

// EventJobPermissionTier records the tier a job runs under and what of it could be applied.
const EventJobPermissionTier = "job.permission_tier"

// normalizePermissionTier lowercases and trims a tier; "" and "auto" are the same.
func normalizePermissionTier(tier string) string {
	t := strings.ToLower(strings.TrimSpace(tier))
	if t == "" {
		return PermissionTierAuto
	}
	return t
}

// checkPermissionTier is the admission rule for JobRequest.PermissionTier: a known value,
// only for an acp-agent (the only agent kind with a permission gate to tighten). On a worker
// runner the tier travels with the dispatch; a worker too old to carry it is refused
// separately (checkPermissionTierWorker).
func checkPermissionTier(req JobRequest, gateAgent string, ac config.AgentConfig) error {
	tier := normalizePermissionTier(req.PermissionTier)
	switch tier {
	case PermissionTierAuto:
		return nil
	case PermissionTierAsk, PermissionTierStrict:
	default:
		return fmt.Errorf("%w: permission_tier %q is not auto, ask or strict", ErrInvalidRequest, req.PermissionTier)
	}
	if ac.Type != agent.TypeACPAgent {
		return fmt.Errorf("%w: permission_tier only applies to an acp-agent (agent %q is not one)", ErrInvalidRequest, gateAgent)
	}
	return nil
}

// checkPermissionTierWorker refuses a tiered job for a worker whose protocol cannot carry
// the tier: it would drop the field and run the job under its own, possibly looser, gate.
// Like checkBudgetWorker it is the admission-time courtesy; the dispatch-time check in the
// worker runner is the backstop for a worker whose version is not known yet.
func (s *Service) checkPermissionTierWorker(cfg *config.Config, req JobRequest) error {
	if normalizePermissionTier(req.PermissionTier) == PermissionTierAuto || !isWorkerRunner(cfg, req.Runner) || s.workers == nil {
		return nil
	}
	workerID := req.WorkerID
	if workerID == "" {
		workerID = cfg.Runners[req.Runner].WorkerID
	}
	if workerID == "" {
		return nil
	}
	caps, online := s.workers.Candidate(workerID)
	if !online || !caps.ProtocolKnown {
		return nil
	}
	if !wsproto.SupportsPermissionTier(caps.ProtocolVersion) {
		return fmt.Errorf("%w: worker %s speaks protocol v%d, a permission tier needs v%d; upgrade the worker or drop the permission tier",
			ErrInvalidRequest, workerID, caps.ProtocolVersion, wsproto.PermissionTierMinProtocolVersion)
	}
	return nil
}

// tightenApproval raises an approval policy's mode to at least the tier's. It never lowers
// it: an "auto" or unknown tier, and a tier looser than the policy, return the policy as is.
func tightenApproval(pol config.ApprovalConfig, tier string) config.ApprovalConfig {
	want := ""
	switch normalizePermissionTier(tier) {
	case PermissionTierAsk:
		want = config.ApprovalAsk
	case PermissionTierStrict:
		want = config.ApprovalStrict
	default:
		return pol
	}
	if approvalModeRank(want) > approvalModeRank(pol.Mode) {
		pol.Mode = want
	}
	return pol
}

// approvalModeRank orders the gate modes by strictness (off < ask < strict).
func approvalModeRank(mode string) int {
	switch mode {
	case config.ApprovalAsk:
		return 1
	case config.ApprovalStrict:
		return 2
	default:
		return 0
	}
}

// effectiveApproval is the gate an ACP job runs under: the configured policy (project, the
// agent's own permission_policy, or the server-set session policy), tightened by the
// job's permission tier.
func effectiveApproval(cfg *config.Config, req JobRequest) config.ApprovalConfig {
	return tightenApproval(cfg.EffectiveApprovalWith(req.ProjectKey, req.Agent, req.SessionApproval), req.PermissionTier)
}

// permissionTierModeID is the agent mode (session/set_mode) a tier switches the session to:
// the agent's acp.modes.ask for ask / strict, "" for auto or an agent that maps none (the
// gofer gate alone then applies).
func permissionTierModeID(ac config.AgentConfig, tier string) string {
	switch normalizePermissionTier(tier) {
	case PermissionTierAsk, PermissionTierStrict:
		if ac.ACP != nil {
			return ac.ACP.Modes[permissionTierModeKey]
		}
	}
	return ""
}

// recordPermissionTier writes the job.permission_tier event for a job that asked for a tier:
// the effective gate mode and whether the agent's own ask mode could be applied. An agent
// that maps no acp.modes.ask is NOT an error — only the gofer gate is tightened — but the
// event says so, so the missing half is visible.
func (s *Service) recordPermissionTier(jobID string, cfg *config.Config, req JobRequest) {
	tier := normalizePermissionTier(req.PermissionTier)
	if tier == PermissionTierAuto {
		return
	}
	payload := map[string]any{"tier": tier, "approval_mode": effectiveApproval(cfg, req).Mode}
	if ac, ok := agent.ResolveAgent(cfg, req.Agent); ok {
		if mode := permissionTierModeID(ac, tier); mode != "" {
			payload["agent_mode"] = mode
		} else {
			payload["agent_mode"] = ""
			payload["note"] = "agent maps no acp.modes.ask: only the gofer approval gate is tightened"
		}
	}
	s.recordEvent(jobID, EventJobPermissionTier, payload)
}
