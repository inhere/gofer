package jobstore

import (
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/runner"
)

func usageOf(in, out int64) runner.Usage {
	return runner.Usage{InputTokens: in, OutputTokens: out, TotalTokens: in + out}
}

func TestAddSessionUsageAccumulatesAndTalliesDaily(t *testing.T) {
	s := openTest(t)
	cur := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return cur })
	_, err := s.UpsertAgentSession(AgentSession{SessionID: "su1", Agent: "claude", ProjectKey: "p1"})
	assert.NoErr(t, err)
	_, err = s.UpsertAgentSession(AgentSession{SessionID: "su2", Agent: "codex", ProjectKey: "p1"})
	assert.NoErr(t, err)

	ok, err := s.AddSessionUsage("nope", runner.SessionUsage{Main: usageOf(1, 1)})
	assert.NoErr(t, err)
	assert.False(t, ok)

	d1 := runner.SessionUsage{Main: usageOf(10, 5), Sub: usageOf(3, 2),
		ByModel: map[string]runner.Usage{"opus": usageOf(10, 5), "sonnet": usageOf(3, 2)}}
	ok, err = s.AddSessionUsage("su1", d1)
	assert.NoErr(t, err)
	assert.True(t, ok)
	_, err = s.AddSessionUsage("su1", runner.SessionUsage{Main: usageOf(1, 1), ByModel: map[string]runner.Usage{"opus": usageOf(1, 1)}})
	assert.NoErr(t, err)

	a, _, err := s.GetAgentSession("su1")
	assert.NoErr(t, err)
	got := ParseSessionUsage(a.UsageJSON)
	assert.Eq(t, int64(11), got.Main.InputTokens)
	assert.Eq(t, int64(6), got.Main.OutputTokens)
	assert.Eq(t, int64(17), got.Main.TotalTokens)
	assert.Eq(t, int64(5), got.Sub.TotalTokens)
	assert.Eq(t, int64(17), got.ByModel["opus"].TotalTokens)
	assert.Eq(t, int64(22), got.Total().TotalTokens)

	// the next day, another agent's session
	cur = cur.Add(24 * time.Hour)
	_, err = s.AddSessionUsage("su2", runner.SessionUsage{Main: usageOf(100, 50)}) // no model split
	assert.NoErr(t, err)

	st, err := s.SessionUsageStats(cur.Unix(), []time.Duration{24 * time.Hour, 7 * 24 * time.Hour})
	assert.NoErr(t, err)
	// "24h" from 2026-10-10 10:00 reaches back to the 9th's bucket (whole-day tally)
	w24 := st.Windows["24h"]
	assert.Eq(t, 2, w24.Sessions)
	assert.Eq(t, int64(172), w24.Total.TotalTokens)
	assert.Eq(t, int64(150), w24.ByAgent["codex"].TotalTokens)
	assert.Eq(t, int64(22), w24.ByAgent["claude"].TotalTokens)
	assert.Eq(t, 2, st.Windows["7d"].Sessions)

	// a month later nothing is inside the windows any more
	st, err = s.SessionUsageStats(cur.Add(30*24*time.Hour).Unix(), []time.Duration{24 * time.Hour, 7 * 24 * time.Hour})
	assert.NoErr(t, err)
	assert.Eq(t, 0, st.Windows["7d"].Sessions)
	assert.Eq(t, int64(0), st.Windows["7d"].Total.TotalTokens)
}

func TestParseSessionUsageTolerant(t *testing.T) {
	assert.True(t, ParseSessionUsage("").Empty())
	assert.True(t, ParseSessionUsage("{broken").Empty())
}

func TestAddSessionUsageAttributesToLiveSupervisedPlans(t *testing.T) {
	s := openTest(t)
	for _, sid := range []string{"sup1", "sup2"} {
		_, err := s.UpsertAgentSession(AgentSession{SessionID: sid, Agent: "claude", ProjectKey: "p1"})
		assert.NoErr(t, err)
	}
	mk := func(id, sup, status string, updated int64) {
		assert.NoErr(t, s.InsertPlan(Plan{PlanID: id, Status: status, SupervisorSessionID: sup, CreatedAt: 1, UpdatedAt: updated}))
	}
	mk("plan-open-a", "sup1", PlanOpen, 5) // the most recently active live plan of sup1
	mk("plan-blk-b", "sup1", PlanBlocked, 2)
	mk("plan-done-c", "sup1", PlanDone, 9) // newer, but closed
	mk("plan-arch-d", "sup1", PlanArchived, 9)
	mk("plan-other", "sup2", PlanOpen, 1)
	mk("plan-unbound", "", PlanOpen, 9)

	d := runner.SessionUsage{Main: usageOf(10, 5), Sub: usageOf(3, 2),
		ByModel: map[string]runner.Usage{"opus": usageOf(10, 5), "haiku": usageOf(3, 2)}}
	_, err := s.AddSessionUsage("sup1", d)
	assert.NoErr(t, err)
	_, err = s.AddSessionUsage("sup1", runner.SessionUsage{Sub: usageOf(1, 1), ByModel: map[string]runner.Usage{"haiku": usageOf(1, 1)}})
	assert.NoErr(t, err)

	t.Run("only the most recently active live plan accrues, main/sub split kept", func(t *testing.T) {
		u, err := s.PlanSessionUsage("plan-open-a")
		assert.NoErr(t, err)
		assert.Eq(t, 1, u.Sessions)
		assert.Eq(t, int64(15), u.Usage.Main.TotalTokens)
		assert.Eq(t, int64(7), u.Usage.Sub.TotalTokens)
		assert.Eq(t, int64(15), u.Usage.ByModel["opus"].TotalTokens)
		assert.Eq(t, int64(7), u.Usage.ByModel["haiku"].TotalTokens)
	})
	t.Run("older live, closed, other-session and unbound plans get nothing", func(t *testing.T) {
		for _, id := range []string{"plan-blk-b", "plan-done-c", "plan-arch-d", "plan-other", "plan-unbound"} {
			u, err := s.PlanSessionUsage(id)
			assert.NoErr(t, err)
			assert.Eq(t, 0, u.Sessions)
			assert.True(t, u.Usage.Empty())
		}
	})
	t.Run("rebinding adds a second session; earlier usage stays", func(t *testing.T) {
		assert.NoErr(t, s.SetPlanSupervisorSessionID("plan-open-a", "sup2")) // binding bumps updated_at: newest of sup2
		_, err := s.AddSessionUsage("sup2", runner.SessionUsage{Main: usageOf(100, 0)})
		assert.NoErr(t, err)
		_, err = s.AddSessionUsage("sup1", runner.SessionUsage{Main: usageOf(1000, 0)}) // no longer bound to plan-open-a
		assert.NoErr(t, err)
		u, err := s.PlanSessionUsage("plan-open-a")
		assert.NoErr(t, err)
		assert.Eq(t, 2, u.Sessions)
		assert.Eq(t, int64(115), u.Usage.Main.TotalTokens)
		assert.Eq(t, int64(122), u.Usage.Total().TotalTokens)
		b, err := s.PlanSessionUsage("plan-blk-b") // sup1's only live plan now
		assert.NoErr(t, err)
		assert.Eq(t, int64(1000), b.Usage.Main.TotalTokens)
	})
}

func TestAddSessionUsageFollowsTheActivePlan(t *testing.T) {
	s := openTest(t)
	cur := time.Unix(1000, 0)
	s.SetClock(func() time.Time { return cur })
	_, err := s.UpsertAgentSession(AgentSession{SessionID: "sup", Agent: "claude", ProjectKey: "p1"})
	assert.NoErr(t, err)
	assert.NoErr(t, s.InsertPlan(Plan{PlanID: "plan-a", Status: PlanOpen, SupervisorSessionID: "sup", CreatedAt: 1, UpdatedAt: 10}))
	assert.NoErr(t, s.InsertPlan(Plan{PlanID: "plan-b", Status: PlanOpen, SupervisorSessionID: "sup", CreatedAt: 1, UpdatedAt: 20}))
	assert.NoErr(t, s.InsertTodo(PlanTodo{TodoID: "todo-a1", PlanID: "plan-a", Title: "a1", CreatedAt: 5, UpdatedAt: 5}))
	add := func(n int64) {
		_, err := s.AddSessionUsage("sup", runner.SessionUsage{Main: usageOf(n, 0)})
		assert.NoErr(t, err)
	}
	mainOf := func(id string) int64 {
		u, err := s.PlanSessionUsage(id)
		assert.NoErr(t, err)
		return u.Usage.Main.TotalTokens
	}

	add(7) // plan-b was touched last
	assert.Eq(t, int64(0), mainOf("plan-a"))
	assert.Eq(t, int64(7), mainOf("plan-b"))

	cur = cur.Add(time.Minute) // a todo change makes plan-a the active one
	_, err = s.SetTodoDone("todo-a1", true)
	assert.NoErr(t, err)
	add(30)
	assert.Eq(t, int64(30), mainOf("plan-a"))
	assert.Eq(t, int64(7), mainOf("plan-b")) // earlier deltas stay where they were booked

	cur = cur.Add(time.Minute) // and back to plan-b
	assert.NoErr(t, s.TouchPlan("plan-b"))
	add(500)
	assert.Eq(t, int64(30), mainOf("plan-a"))
	assert.Eq(t, int64(507), mainOf("plan-b"))
	// the session total equals the sum over its plans: nothing is counted twice
	a, ok, err := s.GetAgentSession("sup")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, ParseSessionUsage(a.UsageJSON).Main.TotalTokens, mainOf("plan-a")+mainOf("plan-b"))
}
