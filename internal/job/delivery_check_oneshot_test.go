package job

import (
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

func defaultCheckerConfig(root, bin string, dailyLimit int) *config.Config {
	return &config.Config{
		Server:  config.ServerConfig{DeliveryCheck: config.DeliveryCheckConfig{Enabled: true, Agent: "claude", TimeoutSec: 30, DailyLimit: dailyLimit}},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {HostPath: root, AllowedAgents: []string{"codex", "claude"}, AllowedRunners: []string{"local"}},
		},
		Agents: map[string]config.AgentConfig{
			"codex": {Type: agent.TypeCLIAgent, Command: bin, Args: []string{"stderr-exit", "0", "", "{{prompt}}"}},
			"claude": {Type: agent.TypeCLIAgent, Command: bin, Args: []string{"printf", checkPassJSON, "{{prompt}}"},
				ReadOnlyArgs: []string{"ro"}},
		},
	}
}

// TestDeliveryCheckDailyLimitSurvivesServiceRebuild: the default checker counts its runs in
// background_agent_runs, so a rebuilt Service (a restarted server) still sees today's run:
// with daily_limit 1 the second job's check is refused (fail-open: the job keeps its
// outcome), no checker job runs for it, and the hard stop is audited.
func TestDeliveryCheckDailyLimitSurvivesServiceRebuild(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bin := testcmd.Path(t)
	first := newServiceFromCfg(t, root, defaultCheckerConfig(root, bin, 1))
	got := settled(t, first, mustSubmit(t, first, checkReq(nil)).ID)
	if sc, ok := DecodeStoredCheck(got.CheckJSON); !ok || sc.Verdict != "pass" || got.CheckError != "" {
		t.Fatalf("first check = %+v ok=%v err=%q", sc, ok, got.CheckError)
	}

	second := newServiceFromCfg(t, root, defaultCheckerConfig(root, bin, 1))
	blocked := settled(t, second, mustSubmit(t, second, checkReq(nil)).ID)
	if blocked.Status != StatusDone || !strings.Contains(blocked.CheckError, "daily_limit") || blocked.CheckJSON != "" {
		t.Fatalf("after rebuild: status=%s check_error=%q check_json=%q, want done + daily_limit refusal", blocked.Status, blocked.CheckError, blocked.CheckJSON)
	}
	evs, err := second.meta.ListAuditEventsSince(jobstore.AuditBackgroundStopped, 0, 10)
	if err != nil || len(evs) != 1 || !strings.Contains(evs[0].Detail, `"reason":"daily_limit"`) {
		t.Fatalf("hard-stop audit = %+v, %v", evs, err)
	}
}

// TestInjectedCheckerDailyLimitSurvivesServiceRebuild: a checker installed with
// SetDeliveryChecker is gated by the same persisted count.
func TestInjectedCheckerDailyLimitSurvivesServiceRebuild(t *testing.T) {
	t.Parallel()
	dc := enabledCheck(0)
	dc.DailyLimit = 1
	s, fake := newCheckService(t, checkSvcOpts{dc: dc})
	first := settled(t, s, mustSubmit(t, s, checkReq(nil)).ID)
	if fake.count() != 1 || first.CheckError != "" {
		t.Fatalf("calls=%d err=%q", fake.count(), first.CheckError)
	}
	root := s.config().Storage.Root
	rebuilt := newServiceFromCfg(t, root, s.config().Clone())
	fake2 := &fakeChecker{}
	rebuilt.SetDeliveryChecker(fake2)
	second := settled(t, rebuilt, mustSubmit(t, rebuilt, checkReq(nil)).ID)
	if fake2.count() != 0 || !strings.Contains(second.CheckError, "daily_limit") {
		t.Fatalf("after rebuild: calls=%d check_error=%q, want refused", fake2.count(), second.CheckError)
	}
}
