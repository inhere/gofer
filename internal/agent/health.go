package agent

import (
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// Health classification for agent failover (SUP-01 P3). It is deliberately a PURE
// function of (aggregate, thresholds) so every caller — the HTTP listing, the CLI
// and the submit-time pre-dispatch decision — reads the same verdict from the same
// evidence.

// Agent health states (SUP-01 P3).
const (
	// HealthUnknown means the window holds no evidence at all: no job was run, so
	// nothing can be claimed about the provider. It is NOT "healthy" — a listing must
	// show it as unknown, and a pre-dispatch substitution treats it as acceptable
	// (never having failed is not a reason to move work away).
	HealthUnknown = "unknown"
	// HealthHealthy means the agent ran jobs in the window and is not degraded.
	HealthHealthy = "healthy"
	// HealthDegraded means the window saw at least DegradedAfter transient failures
	// that the agent has not recovered from with RecoverAfterOK successes.
	HealthDegraded = "degraded"
)

// HealthState classifies an agent's recent-job aggregate. The rule:
//
//   - no job in the window → unknown (no evidence);
//   - transient failures >= degraded_after AND fewer than recover_after_ok
//     successes since the last of them → degraded;
//   - otherwise → healthy.
//
// A zero/negative threshold reads as "unset" and falls back to the documented
// default (config.EffectiveAgentHealth resolves them normally; this keeps a
// hand-built config from producing a nonsensical verdict such as "0 failures is
// already degraded").
func HealthState(h jobstore.AgentHealth, cfg config.AgentHealthConfig) string {
	if h.Jobs <= 0 {
		return HealthUnknown
	}
	degradedAfter := cfg.DegradedAfter
	if degradedAfter <= 0 {
		degradedAfter = config.DefaultAgentDegradedAfter
	}
	recoverAfterOK := cfg.RecoverAfterOK
	if recoverAfterOK <= 0 {
		recoverAfterOK = config.DefaultAgentRecoverAfterOK
	}
	if h.TransientFail >= degradedAfter && h.OKSinceTransient < recoverAfterOK {
		return HealthDegraded
	}
	return HealthHealthy
}
