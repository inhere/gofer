package runner

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/inhere/gofer/internal/config"
)

// The budget limit names (BudgetBreach.Limit): the JSON keys of config.Budget, which is
// also what a user typed, so an error names the exact knob that tripped.
const (
	BudgetLimitTokens = "max_tokens"
	BudgetLimitCost   = "max_cost_usd"
	BudgetLimitTurns  = "max_turns"
)

// BudgetErrorPrefix starts the error text of a budget failure. The job service
// recognises a failed job by it (a remote worker reports the failure as text only), so
// it is the contract between the executing side and the host's failure classification.
const BudgetErrorPrefix = "budget exceeded: "

// BudgetBreach is the first limit a job went over (N2 §B): which one, its ceiling and
// the value that crossed it.
type BudgetBreach struct {
	Limit  string  `json:"limit"`
	Max    float64 `json:"max"`
	Actual float64 `json:"actual"`
}

// Error renders the failure text: `budget exceeded: max_tokens 50000 (used 51234)`.
func (b BudgetBreach) Error() string {
	switch b.Limit {
	case BudgetLimitCost:
		return fmt.Sprintf("%s%s $%.4f (used $%.4f)", BudgetErrorPrefix, b.Limit, b.Max, b.Actual)
	case BudgetLimitTurns:
		return fmt.Sprintf("%s%s %d (used %d model requests)", BudgetErrorPrefix, b.Limit, int64(b.Max), int64(b.Actual))
	default:
		return fmt.Sprintf("%s%s %d (used %d)", BudgetErrorPrefix, b.Limit, int64(b.Max), int64(b.Actual))
	}
}

var budgetErrRe = regexp.MustCompile(`^budget exceeded: (max_[a-z_]+) \$?([0-9.]+) \(used \$?([0-9.]+)`)

// ParseBudgetBreach recovers a breach from a failed job's error text (ok=false when the
// text is not a budget failure). Used where only the text survives: a remote worker
// reports the failure as an error string.
func ParseBudgetBreach(errText string) (BudgetBreach, bool) {
	errText = strings.TrimSpace(errText)
	if !strings.HasPrefix(errText, BudgetErrorPrefix) {
		return BudgetBreach{}, false
	}
	b := BudgetBreach{}
	if m := budgetErrRe.FindStringSubmatch(errText); m != nil {
		b.Limit = m[1]
		b.Max, _ = strconv.ParseFloat(m[2], 64)
		b.Actual, _ = strconv.ParseFloat(m[3], 64)
	}
	return b, true
}

// BudgetMeter accumulates one job's spend from the agent's streamed accounting and
// reports the first limit crossed (N2 §B). It is fed from the execution-side capture
// points — the ndjson projection, the acp handler, the codex stderr sniff — and is safe
// for concurrent use. A nil *BudgetMeter is a valid no-op meter, so call sites never
// branch on "is there a budget".
//
// Accounting rules:
//   - tokens: input+output+cache of every distinct model message. A message that is
//     reported several times (claude emits one line per content block, all carrying the
//     message's usage) is keyed by its id and counted once, with the largest total seen.
//     A source with no per-message id (omp, acp usage_update, a generic usage path)
//     reports a RUNNING tally instead; that is kept as a high-water mark. The job total is
//     the larger of the two views.
//   - cost: the largest cost a source reported (0 where it reports none).
//   - turns: model requests — assistant messages (ndjson) or prompt turns (acp).
//
// The limit is "more than": max_turns=3 lets the third request finish and trips on the
// fourth, so a run is never cut off while delivering its last allowed answer.
type BudgetMeter struct {
	budget   config.Budget
	onBreach func(BudgetBreach)

	mu     sync.Mutex
	perMsg map[string]int64 // message id -> total tokens (high-water)
	msgSum int64            // sum of perMsg and of id-less messages
	tally  int64            // running-tally high-water
	cost   float64
	turns  int
	breach *BudgetBreach
}

// NewBudgetMeter returns a meter for b, or nil when b limits nothing. onBreach (may be
// nil) is called once, from the goroutine that fed the crossing value, with no meter
// lock held.
func NewBudgetMeter(b *config.Budget, onBreach func(BudgetBreach)) *BudgetMeter {
	if b.IsZero() {
		return nil
	}
	return &BudgetMeter{budget: *b, onBreach: onBreach, perMsg: map[string]int64{}}
}

// AddMessage records one assistant message: id is the provider's message id ("" = no id,
// every call is a distinct message) and u its usage (may be nil: the message still
// counts as a turn).
func (m *BudgetMeter) AddMessage(id string, u *Usage) {
	if m == nil {
		return
	}
	var total int64
	var cost float64
	if u != nil {
		total, cost = u.TotalTokens, u.CostUSD
	}
	m.mu.Lock()
	if id == "" {
		m.turns++
		m.msgSum += total
	} else {
		prev, seen := m.perMsg[id]
		if !seen {
			m.turns++
		}
		if total > prev {
			m.msgSum += total - prev
			m.perMsg[id] = total
		} else if !seen {
			m.perMsg[id] = prev
		}
	}
	if cost > m.cost {
		m.cost = cost
	}
	b := m.checkLocked()
	m.mu.Unlock()
	m.fire(b)
}

// AddTurn counts one model request that carries no usage of its own (an acp prompt
// turn, an omp assistant message whose usage arrives separately).
func (m *BudgetMeter) AddTurn() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.turns++
	b := m.checkLocked()
	m.mu.Unlock()
	m.fire(b)
}

// SetTally records a source's running usage tally (high-water, never decreasing).
func (m *BudgetMeter) SetTally(u *Usage) {
	if m == nil || u == nil {
		return
	}
	m.mu.Lock()
	if u.TotalTokens > m.tally {
		m.tally = u.TotalTokens
	}
	if u.CostUSD > m.cost {
		m.cost = u.CostUSD
	}
	b := m.checkLocked()
	m.mu.Unlock()
	m.fire(b)
}

// SetCost records a reported cost (high-water).
func (m *BudgetMeter) SetCost(c float64) {
	if m == nil || c <= 0 {
		return
	}
	m.mu.Lock()
	if c > m.cost {
		m.cost = c
	}
	b := m.checkLocked()
	m.mu.Unlock()
	m.fire(b)
}

// checkLocked returns the breach to announce when this call is the first to cross a
// limit (nil otherwise). Caller holds m.mu.
func (m *BudgetMeter) checkLocked() *BudgetBreach {
	if m.breach != nil {
		return nil
	}
	var b *BudgetBreach
	switch tokens := m.tokensLocked(); {
	case m.budget.MaxTokens > 0 && tokens > m.budget.MaxTokens:
		b = &BudgetBreach{Limit: BudgetLimitTokens, Max: float64(m.budget.MaxTokens), Actual: float64(tokens)}
	case m.budget.MaxCostUSD > 0 && m.cost > m.budget.MaxCostUSD:
		b = &BudgetBreach{Limit: BudgetLimitCost, Max: m.budget.MaxCostUSD, Actual: m.cost}
	case m.budget.MaxTurns > 0 && m.turns > m.budget.MaxTurns:
		b = &BudgetBreach{Limit: BudgetLimitTurns, Max: float64(m.budget.MaxTurns), Actual: float64(m.turns)}
	}
	m.breach = b
	return b
}

func (m *BudgetMeter) tokensLocked() int64 {
	if m.tally > m.msgSum {
		return m.tally
	}
	return m.msgSum
}

func (m *BudgetMeter) fire(b *BudgetBreach) {
	if b != nil && m.onBreach != nil {
		m.onBreach(*b)
	}
}

// Breach returns the first limit crossed, or nil.
func (m *BudgetMeter) Breach() *BudgetBreach {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.breach == nil {
		return nil
	}
	b := *m.breach
	return &b
}

// Spent reports what the meter has seen so far.
func (m *BudgetMeter) Spent() (tokens int64, costUSD float64, turns int) {
	if m == nil {
		return 0, 0, 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tokensLocked(), m.cost, m.turns
}
