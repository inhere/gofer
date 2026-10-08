package runner

import (
	"testing"

	"github.com/inhere/gofer/internal/config"
)

func TestBudgetMeterNilIsNoop(t *testing.T) {
	var m *BudgetMeter
	m.AddMessage("a", &Usage{TotalTokens: 10})
	m.AddTurn()
	m.SetTally(&Usage{TotalTokens: 10})
	m.SetCost(1)
	if m.Breach() != nil {
		t.Fatal("nil meter breached")
	}
	if NewBudgetMeter(nil, nil) != nil || NewBudgetMeter(&config.Budget{}, nil) != nil {
		t.Fatal("an empty budget must yield no meter")
	}
}

func TestBudgetMeterTokensDedupeByMessageID(t *testing.T) {
	var got []BudgetBreach
	m := NewBudgetMeter(&config.Budget{MaxTokens: 1000}, func(b BudgetBreach) { got = append(got, b) })
	m.AddMessage("m1", &Usage{TotalTokens: 400})
	m.AddMessage("m1", &Usage{TotalTokens: 500}) // grows within the same message: delta only
	m.AddMessage("m1", &Usage{TotalTokens: 300}) // a smaller replay never shrinks it
	if tok, _, turns := m.Spent(); tok != 500 || turns != 1 {
		t.Fatalf("spent = %d tokens / %d turns, want 500 / 1", tok, turns)
	}
	if len(got) != 0 {
		t.Fatalf("breached early: %v", got)
	}
	m.AddMessage("m2", &Usage{TotalTokens: 501})
	if len(got) != 1 || got[0].Limit != BudgetLimitTokens || got[0].Actual != 1001 {
		t.Fatalf("breach = %v, want one max_tokens at 1001", got)
	}
	m.AddMessage("m3", &Usage{TotalTokens: 9999}) // only the FIRST crossing is announced
	if len(got) != 1 {
		t.Fatalf("breach announced %d times", len(got))
	}
}

func TestBudgetMeterTallyIsHighWaterAndBeatsSmallerSum(t *testing.T) {
	m := NewBudgetMeter(&config.Budget{MaxTokens: 100}, nil)
	m.SetTally(&Usage{TotalTokens: 80})
	m.SetTally(&Usage{TotalTokens: 60}) // a lower reading never lowers the tally
	if tok, _, _ := m.Spent(); tok != 80 {
		t.Fatalf("tokens = %d, want 80", tok)
	}
	m.SetTally(&Usage{TotalTokens: 101})
	if b := m.Breach(); b == nil || b.Limit != BudgetLimitTokens {
		t.Fatalf("breach = %v", b)
	}
}

func TestBudgetMeterTurnsAndCostAreMoreThan(t *testing.T) {
	m := NewBudgetMeter(&config.Budget{MaxTurns: 2, MaxCostUSD: 1}, nil)
	m.AddTurn()
	m.AddTurn()
	m.SetCost(1) // equal to the ceiling is not over it
	if m.Breach() != nil {
		t.Fatalf("breached at the ceiling: %v", m.Breach())
	}
	m.AddTurn()
	if b := m.Breach(); b == nil || b.Limit != BudgetLimitTurns || b.Actual != 3 {
		t.Fatalf("breach = %v, want max_turns at 3", b)
	}

	c := NewBudgetMeter(&config.Budget{MaxCostUSD: 1}, nil)
	c.SetCost(1.25)
	if b := c.Breach(); b == nil || b.Limit != BudgetLimitCost {
		t.Fatalf("breach = %v, want max_cost_usd", b)
	}
}

func TestBudgetBreachTextRoundTrip(t *testing.T) {
	for _, b := range []BudgetBreach{
		{Limit: BudgetLimitTokens, Max: 50000, Actual: 51234},
		{Limit: BudgetLimitCost, Max: 1.5, Actual: 2.25},
		{Limit: BudgetLimitTurns, Max: 20, Actual: 21},
	} {
		got, ok := ParseBudgetBreach(b.Error())
		if !ok || got != b {
			t.Fatalf("round trip of %q = %+v ok=%v, want %+v", b.Error(), got, ok, b)
		}
	}
	if _, ok := ParseBudgetBreach("exit status 1"); ok {
		t.Fatal("an ordinary error parsed as a budget failure")
	}
}
