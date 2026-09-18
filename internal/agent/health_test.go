package agent

import (
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// TestHealthStateThresholdsAndRecovery pins the pure classification the whole health
// surface (listing, CLI, pre-dispatch) reads: no evidence is UNKNOWN (never
// "healthy"), the failure threshold is inclusive, and a recovery is counted from
// successes AFTER the last outage — with the configured recover_after_ok honoured
// rather than assumed to be 1.
func TestHealthStateThresholdsAndRecovery(t *testing.T) {
	cfg := config.AgentHealthConfig{WindowSec: 3600, DegradedAfter: 3, RecoverAfterOK: 1}

	cases := []struct {
		name string
		h    jobstore.AgentHealth
		cfg  config.AgentHealthConfig
		want string
	}{
		{
			name: "no evidence is unknown, not healthy",
			h:    jobstore.AgentHealth{Agent: "codex"},
			want: HealthUnknown,
		},
		{
			name: "a success with no failures is healthy",
			h:    jobstore.AgentHealth{Agent: "codex", Jobs: 2, OK: 2, LastOKAt: 100},
			want: HealthHealthy,
		},
		{
			name: "below the failure threshold is healthy",
			h:    jobstore.AgentHealth{Agent: "codex", Jobs: 2, TransientFail: 2, LastTransientAt: 100},
			want: HealthHealthy,
		},
		{
			name: "at the threshold with no recovery is degraded",
			h:    jobstore.AgentHealth{Agent: "codex", Jobs: 3, TransientFail: 3, LastTransientAt: 100},
			want: HealthDegraded,
		},
		{
			name: "one success after the outage restores it",
			h:    jobstore.AgentHealth{Agent: "codex", Jobs: 4, TransientFail: 3, OKSinceTransient: 1, LastTransientAt: 100, LastOKAt: 200},
			want: HealthHealthy,
		},
		{
			name: "a configured recover_after_ok of 2 is not satisfied by one",
			h:    jobstore.AgentHealth{Agent: "codex", Jobs: 4, TransientFail: 3, OKSinceTransient: 1},
			cfg:  config.AgentHealthConfig{WindowSec: 3600, DegradedAfter: 3, RecoverAfterOK: 2},
			want: HealthDegraded,
		},
		{
			name: "the same evidence recovers once the second success lands",
			h:    jobstore.AgentHealth{Agent: "codex", Jobs: 5, TransientFail: 3, OKSinceTransient: 2},
			cfg:  config.AgentHealthConfig{WindowSec: 3600, DegradedAfter: 3, RecoverAfterOK: 2},
			want: HealthHealthy,
		},
		{
			name: "a raised failure threshold tolerates more outages",
			h:    jobstore.AgentHealth{Agent: "codex", Jobs: 3, TransientFail: 3},
			cfg:  config.AgentHealthConfig{WindowSec: 3600, DegradedAfter: 5, RecoverAfterOK: 1},
			want: HealthHealthy,
		},
		{
			name: "zero thresholds fall back to the defaults (3/1)",
			h:    jobstore.AgentHealth{Agent: "codex", Jobs: 3, TransientFail: 3},
			cfg:  config.AgentHealthConfig{},
			want: HealthDegraded,
		},
		{
			name: "other failures never make an agent degraded",
			h:    jobstore.AgentHealth{Agent: "codex", Jobs: 5, OtherFail: 5},
			want: HealthHealthy,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := HealthState(tc.h, tc.cfg)
			if got != tc.want {
				t.Fatalf("HealthState(%+v, %+v) = %q, want %q", tc.h, tc.cfg, got, tc.want)
			}
		})
	}

	// The defaults the config layer resolves are what an unset block means.
	if def := (&config.Config{}).EffectiveAgentHealth(); HealthState(jobstore.AgentHealth{Jobs: 3, TransientFail: 3}, def) != HealthDegraded {
		t.Fatal("the default thresholds must classify 3 recent provider errors as degraded")
	}
}
