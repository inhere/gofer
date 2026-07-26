package jobstore

import (
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
)

func TestDecisionInsertGetListAnswer(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.InsertPlan(Plan{PlanID: "plan-dec", Status: PlanOpen, CreatedAt: 1, UpdatedAt: 1}))

	now := time.Now().Unix()
	d := PlanDecision{
		ID: "dec-1", PlanID: "plan-dec", Title: "t1", Question: "q1",
		OptionsJSON: `["a","b"]`, TimeoutSec: 60, AskedAt: now - 1,
	}
	assert.NoErr(t, s.InsertDecision(&d))

	got, ok, err := s.GetDecision("dec-1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "dec-1", got.ID)
	assert.Eq(t, "plan-dec", got.PlanID)
	assert.Eq(t, `["a","b"]`, got.OptionsJSON)
	assert.Eq(t, DecisionOpen, got.State)
	assert.Eq(t, int64(60), got.TimeoutSec)
	assert.Eq(t, now-1, got.AskedAt)
	assert.Eq(t, "", got.Answer)
	assert.Eq(t, int64(0), got.AnsweredAt)
	assert.Eq(t, "", got.AnsweredBy)

	_, ok, err = s.GetDecision("dec-missing")
	assert.NoErr(t, err)
	assert.False(t, ok)

	// Global question (no plan) stores plan_id as NULL and scans back as "".
	g := PlanDecision{ID: "dec-global", Title: "t", Question: "q", TimeoutSec: 60, AskedAt: now}
	assert.NoErr(t, s.InsertDecision(&g))
	var storedPlanID sql.NullString
	assert.NoErr(t, s.db.QueryRow(`SELECT plan_id FROM plan_decisions WHERE id='dec-global'`).Scan(&storedPlanID))
	assert.False(t, storedPlanID.Valid)

	list, err := s.ListDecisions("", "")
	assert.NoErr(t, err)
	assert.Len(t, list, 2)
	assert.Eq(t, "dec-1", list[0].ID) // asked_at ASC
	assert.Eq(t, "dec-global", list[1].ID)

	byPlan, err := s.ListDecisions("", "plan-dec")
	assert.NoErr(t, err)
	assert.Len(t, byPlan, 1)
	assert.Eq(t, "dec-1", byPlan[0].ID)

	// Answer the global one, then filters by state.
	answered, err := s.AnswerDecision("dec-global", "free text", "human")
	assert.NoErr(t, err)
	assert.True(t, answered)
	got, ok, err = s.GetDecision("dec-global")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, DecisionAnswered, got.State)
	assert.Eq(t, "free text", got.Answer)
	assert.Eq(t, "human", got.AnsweredBy)
	assert.True(t, got.AnsweredAt > 0)

	open, err := s.ListDecisions(DecisionOpen, "")
	assert.NoErr(t, err)
	assert.Len(t, open, 1)
	assert.Eq(t, "dec-1", open[0].ID)
	answeredList, err := s.ListDecisions(DecisionAnswered, "")
	assert.NoErr(t, err)
	assert.Len(t, answeredList, 1)

	// Answer a losing decision: state must not change (重复 answer 失败, D3).
	changed, err := s.AnswerDecision("dec-global", "second", "other")
	assert.NoErr(t, err)
	assert.False(t, changed)
	got, _, err = s.GetDecision("dec-global")
	assert.NoErr(t, err)
	assert.Eq(t, "free text", got.Answer)
	assert.Eq(t, "human", got.AnsweredBy)

	// Unknown id answers false, nil error.
	changed, err = s.AnswerDecision("dec-missing", "x", "y")
	assert.NoErr(t, err)
	assert.False(t, changed)
}

func TestDecisionInsertNormalisation(t *testing.T) {
	s := openTest(t)

	// id generated in-store with dec- prefix (MCP standalone bypasses httpapi).
	d := PlanDecision{Title: "t", Question: "q"}
	assert.NoErr(t, s.InsertDecision(&d))
	assert.True(t, strings.HasPrefix(d.ID, "dec-"))
	assert.Eq(t, int64(DefaultDecisionTimeoutSec), d.TimeoutSec) // <=0 -> default
	assert.Eq(t, DecisionOpen, d.State)
	assert.True(t, d.AskedAt > 0)

	// timeout clamp bounds.
	low := PlanDecision{Title: "t", Question: "q", TimeoutSec: 1}
	assert.NoErr(t, s.InsertDecision(&low))
	assert.Eq(t, int64(MinDecisionTimeoutSec), low.TimeoutSec)
	high := PlanDecision{Title: "t", Question: "q", TimeoutSec: 1 << 30}
	assert.NoErr(t, s.InsertDecision(&high))
	assert.Eq(t, int64(MaxDecisionTimeoutSec), high.TimeoutSec)

	// empty options array normalises to NULL (= free text).
	opts := PlanDecision{Title: "t", Question: "q", OptionsJSON: "[]"}
	assert.NoErr(t, s.InsertDecision(&opts))
	var stored sql.NullString
	assert.NoErr(t, s.db.QueryRow(`SELECT options_json FROM plan_decisions WHERE id=?`, opts.ID).Scan(&stored))
	assert.False(t, stored.Valid)
	got, ok, err := s.GetDecision(opts.ID)
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "", got.OptionsJSON)

	// invalid state rejected; empty title/question rejected.
	bad := PlanDecision{Title: "t", Question: "q", State: "WEIRD"}
	assert.Err(t, s.InsertDecision(&bad))
	assert.Err(t, s.InsertDecision(&PlanDecision{Question: "q"}))
	assert.Err(t, s.InsertDecision(&PlanDecision{Title: "t"}))
	assert.Err(t, func() error { _, e := s.ListDecisions("WEIRD", ""); return e }())
}

func TestDecisionLazyExpiry(t *testing.T) {
	s := openTest(t)
	// asked 10s ago with timeout 2s -> already due (seconds, plan B1: no *1000).
	d := PlanDecision{Title: "t", Question: "q", TimeoutSec: 2, AskedAt: time.Now().Unix() - 10}
	assert.NoErr(t, s.InsertDecision(&d))

	// Read path runs lazy expiry: get no longer treats it as OPEN.
	got, ok, err := s.GetDecision(d.ID)
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, DecisionExpired, got.State)

	// Answer after expiry fails.
	changed, err := s.AnswerDecision(d.ID, "too late", "human")
	assert.NoErr(t, err)
	assert.False(t, changed)

	// list ?state=OPEN only returns live ones.
	fresh := PlanDecision{Title: "t", Question: "q", TimeoutSec: 3600}
	assert.NoErr(t, s.InsertDecision(&fresh))
	open, err := s.ListDecisions(DecisionOpen, "")
	assert.NoErr(t, err)
	assert.Len(t, open, 1)
	assert.Eq(t, fresh.ID, open[0].ID)
	expired, err := s.ListDecisions(DecisionExpired, "")
	assert.NoErr(t, err)
	assert.Len(t, expired, 1)
	assert.Eq(t, d.ID, expired[0].ID)
}

// TestDecisionAnswerExpireRace hammers one decision with a concurrent Answer and
// expire: exactly one may win, the row must end in a terminal state, and the
// lock discipline (HIGH-1) must not deadlock.
func TestDecisionAnswerExpireRace(t *testing.T) {
	s := openTest(t)
	d := PlanDecision{Title: "t", Question: "q", TimeoutSec: 2, AskedAt: time.Now().Unix() - 10}
	assert.NoErr(t, s.InsertDecision(&d))

	var wg sync.WaitGroup
	results := make([]bool, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		ok, err := s.AnswerDecision(d.ID, "raced", "human")
		assert.NoErr(t, err)
		results[0] = ok
	}()
	go func() {
		defer wg.Done()
		assert.NoErr(t, s.expireDueDecisions())
		// expireDueDecisions has no bool; "win" is observed via final state.
		results[1] = true
	}()
	wg.Wait()

	got, ok, err := s.GetDecision(d.ID)
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.True(t, got.State == DecisionAnswered || got.State == DecisionExpired)
	if got.State == DecisionAnswered {
		assert.True(t, results[0])
		assert.Eq(t, "raced", got.Answer)
	} else {
		assert.False(t, results[0])
		assert.Eq(t, "", got.Answer)
	}
}
