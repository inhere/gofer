package job

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/store"
	"github.com/inhere/gofer/internal/wsproto"
)

// No combination of project policy, agent policy, server-set session policy and requested
// tier may produce a gate looser than the one WITHOUT the tier (R7: a tier only tightens),
// and the result is never looser than the tier itself.
func TestPermissionTierNeverLoosensTheGate(t *testing.T) {
	t.Parallel()
	projectModes := []string{"", config.ApprovalOff, config.ApprovalAsk, config.ApprovalStrict}
	agentPolicies := []string{"", config.ApprovalAutoAllow, config.ApprovalAsk, config.ApprovalStrict}
	overrides := []*config.ApprovalConfig{nil, {Mode: config.ApprovalOff}, {Mode: config.ApprovalAsk}, {Mode: config.ApprovalStrict}}
	tiers := []string{"", "auto", "AUTO", "ask", "Ask", "strict", " strict ", "bogus"}

	for _, pm := range projectModes {
		for _, ap := range agentPolicies {
			for _, ov := range overrides {
				cfg := &config.Config{
					Projects: map[string]config.ProjectConfig{"p": {Approval: &config.ApprovalConfig{Mode: pm}}},
					Agents:   map[string]config.AgentConfig{"a": {ACP: &config.ACPConfig{PermissionPolicy: ap}}},
				}
				base := cfg.EffectiveApprovalWith("p", "a", ov)
				for _, tier := range tiers {
					got := effectiveApproval(cfg, JobRequest{ProjectKey: "p", Agent: "a", SessionApproval: ov, PermissionTier: tier})
					if approvalModeRank(got.Mode) < approvalModeRank(base.Mode) {
						t.Errorf("project=%q agent=%q override=%v tier=%q: mode %q is looser than the untiered %q",
							pm, ap, ov, tier, got.Mode, base.Mode)
					}
					want := 0
					switch strings.ToLower(strings.TrimSpace(tier)) {
					case "ask":
						want = approvalModeRank(config.ApprovalAsk)
					case "strict":
						want = approvalModeRank(config.ApprovalStrict)
					}
					if approvalModeRank(got.Mode) < want {
						t.Errorf("project=%q agent=%q override=%v tier=%q: mode %q is looser than the tier", pm, ap, ov, tier, got.Mode)
					}
					// Everything but the mode (timeouts, auto-allow kinds) stays the policy's own.
					if got.OnTimeout != base.OnTimeout || got.TimeoutSec != base.TimeoutSec {
						t.Errorf("project=%q tier=%q: only the mode may change: %+v vs %+v", pm, tier, got, base)
					}
				}
			}
		}
	}
}

func TestPermissionTierMapping(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Projects: map[string]config.ProjectConfig{
		"open":   {},
		"strict": {Approval: &config.ApprovalConfig{Mode: config.ApprovalStrict}},
	}}
	cases := []struct {
		project, tier, want string
	}{
		{"open", "", config.ApprovalOff},
		{"open", "auto", config.ApprovalOff},
		{"open", "ask", config.ApprovalAsk},
		{"open", "strict", config.ApprovalStrict},
		// The project is stricter than the request: strict stays strict.
		{"strict", "ask", config.ApprovalStrict},
		{"strict", "auto", config.ApprovalStrict},
	}
	for _, c := range cases {
		got := effectiveApproval(cfg, JobRequest{ProjectKey: c.project, Agent: "a", PermissionTier: c.tier})
		if got.Mode != c.want {
			t.Errorf("project %s tier %q: mode = %q, want %q", c.project, c.tier, got.Mode, c.want)
		}
	}
}

// The caller still cannot hand over session_approval: that stays server-set. (The entry
// layers refuse it; the tier is the only knob a caller has, and it only tightens.)
func TestPermissionTierAdmission(t *testing.T) {
	t.Parallel()
	s := newACPService(t, t.TempDir(), acptest.Options{})
	submit := func(agent, tier string) error {
		_, err := s.Submit(JobRequest{
			ProjectKey: "self", Agent: agent, Runner: "local", Cwd: ".", TimeoutSec: 20,
			Prompt: acpTestPrompt, PermissionTier: tier,
		})
		return err
	}
	if err := submit("acpbot", "loose"); !errors.Is(err, ErrInvalidRequest) || !strings.Contains(err.Error(), "permission_tier") {
		t.Fatalf("unknown tier = %v, want ErrInvalidRequest naming permission_tier", err)
	}
	if err := checkPermissionTier(JobRequest{PermissionTier: "ask"}, "exec", config.AgentConfig{Type: "exec"}); !errors.Is(err, ErrInvalidRequest) ||
		!strings.Contains(err.Error(), "only applies to an acp-agent") {
		t.Fatalf("ask on a non-acp agent = %v, want ErrInvalidRequest", err)
	}
	// "auto" asks for nothing, so any agent kind takes it.
	res, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".", TimeoutSec: 20, Prompt: acpTestPrompt, PermissionTier: "auto"})
	if err != nil {
		t.Fatalf("auto tier: %v", err)
	}
	s.Wait(res.ID)
}

// A tier on a worker runner: a worker that speaks the tier's protocol receives it on the
// forward (its own Submit then tightens ITS gate); an older one is refused, never downgraded;
// no tier is never refused.
func TestPermissionTierWorkerProtocolGate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		proto   int
		tier    string
		wantErr bool
	}{
		{"old worker + ask", wsproto.PermissionTierMinProtocolVersion - 1, "ask", true},
		{"old worker + strict", wsproto.PermissionTierMinProtocolVersion - 1, "strict", true},
		{"new worker + ask", wsproto.PermissionTierMinProtocolVersion, "ask", false},
		{"old worker, no tier", wsproto.PermissionTierMinProtocolVersion - 1, "", false},
		{"old worker, auto", wsproto.PermissionTierMinProtocolVersion - 1, "auto", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubWorkerRunner{}
			cand := selfCaps(WorkerCandidate{WorkerID: "w1", ProtocolVersion: tc.proto, ProtocolKnown: true})
			cand.Agents = append(cand.Agents, "acpw")
			sel := fakeSelector{cands: []WorkerCandidate{cand}}
			s := newWorkerTestServiceSel(t, t.TempDir(), stub, map[string]config.WorkerAuthConfig{"w1": {Token: "t"}}, sel)
			res, err := s.Submit(JobRequest{
				ProjectKey: "self", Agent: "acpw", Runner: "remote-w1", Prompt: "hi", Cwd: ".", TimeoutSec: 30,
				PermissionTier: tc.tier,
			})
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidRequest) || !strings.Contains(err.Error(), "permission tier") {
					t.Fatalf("err = %v, want ErrInvalidRequest naming the permission tier (worker too old)", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Submit: %v", err)
			}
			s.Wait(res.ID)
			if stub.gotForward == nil {
				t.Fatal("runner never saw the forward")
			}
			if stub.gotForward.PermissionTier != tc.tier {
				t.Fatalf("forward tier = %q, want %q", stub.gotForward.PermissionTier, tc.tier)
			}
		})
	}
}

// ask with an agent that maps acp.modes.ask: the session is switched to that mode before the
// prompt turn, the gate asks a person, and the job records the tier.
func TestPermissionTierAskSetsAgentModeAndAsksThePerson(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newACPServiceAgent(t, root, acptest.Options{}, nil, func(ac *config.AgentConfig) {
		ac.ACP = &config.ACPConfig{Modes: map[string]string{"ask": "default"}}
	})
	res, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".", TimeoutSec: 60,
		Prompt: acpTestPrompt, PermissionTier: "ask",
	})
	if err != nil {
		t.Fatal(err)
	}
	it := waitPermissionInteraction(t, s, res.ID, 10*time.Second)
	if _, err := s.AnswerInteractionByHuman(res.ID, it.ID, it.Options[0].Value, "alice"); err != nil {
		t.Fatalf("answer: %v", err)
	}
	// Further requests of the scripted turn may ask too: answer until the job ends.
	for i := 0; i < 5; i++ {
		if cur, _ := s.Get(res.ID); IsTerminal(cur.Status) {
			break
		}
		list, _ := s.GetInteractions(res.ID)
		for _, x := range list {
			if x.Status == InteractionPending && len(x.Options) > 0 {
				_, _ = s.AnswerInteractionByHuman(res.ID, x.ID, x.Options[0].Value, "alice")
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	s.Wait(res.ID)
	final, _ := s.Get(res.ID)

	log := readJobLog(t, final, store.StderrFile)
	if !strings.Contains(log, "session/set_mode mode=default") {
		t.Fatalf("the agent was never switched to its ask mode:\n%s", log)
	}
	evs := jobEventDetails(t, s, res.ID, EventJobPermissionTier)
	if len(evs) != 1 || evs[0]["tier"] != "ask" || evs[0]["agent_mode"] != "default" || evs[0]["approval_mode"] != "ask" {
		t.Fatalf("job.permission_tier events = %v", evs)
	}
}

// An agent without acp.modes.ask: only the gofer gate is tightened, the job still runs and an
// event says the agent half was not applied.
func TestPermissionTierWithoutAgentAskModeOnlyTightensTheGate(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newACPService(t, root, acptest.Options{})
	res, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".", TimeoutSec: 60,
		Prompt: acpTestPrompt, PermissionTier: "strict",
	})
	if err != nil {
		t.Fatal(err)
	}
	it := waitPermissionInteraction(t, s, res.ID, 10*time.Second)
	if it.Type != InteractionTypePermission {
		t.Fatalf("strict must ask a person: %+v", it)
	}
	_ = s.Cancel(res.ID)
	s.Wait(res.ID)

	evs := jobEventDetails(t, s, res.ID, EventJobPermissionTier)
	if len(evs) != 1 || evs[0]["agent_mode"] != "" || evs[0]["approval_mode"] != "strict" || evs[0]["note"] == nil {
		t.Fatalf("job.permission_tier events = %v", evs)
	}
	final, _ := s.Get(res.ID)
	if log := readJobLog(t, final, store.StderrFile); strings.Contains(log, "session/set_mode") {
		t.Fatalf("no ask mode is mapped, nothing may be switched:\n%s", log)
	}
}
