package ndjsonfilter

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"

	"github.com/inhere/gofer/internal/runner"
)

// Signals is what a structured agent stream says about the run's shape, for the
// dashboard's job metrics (gofer-yelm P2): how many agent turns it took, how many
// tool calls it made, and the model it ran on. Known=false means the stream dialect
// is not one the counter understands (generic agents) or it carried no evidence — the
// metrics then record nothing rather than a guessed zero.
//
// WrapUp (gofer-gk74) is the agent's own reason for ending the run early instead of
// answering: it is read from EVERY dialect that reports it, including the generic
// projector (suga), and is independent of Known — a run that wrapped up says so even
// when the counter does not know the stream's turn shape.
type Signals struct {
	Turns     int64
	ToolCalls int64
	Model     string
	// WrapUp is the 收尾原因 the agent reported, normalised by NormalizeWrapUp
	// (`max_steps` / `max_tokens` / `timeout`). Empty = it answered, or the stream never
	// said (a dialect without the field, or a killed run).
	WrapUp string
	// AgentBudget is the last soft-budget snapshot a row of the stream carried (a
	// `budget` object on suga's usage / result rows), nil when the agent was not given
	// one. Like WrapUp it is independent of Known.
	AgentBudget *runner.AgentBudgetUsage
	Known       bool
}

// Wrap-up reasons an agent reports about itself, and the spellings it uses. suga's
// `--output-format stream-json` result row carries the canonical three (suga-az4l);
// `max steps exceeded` is the older shape of the same fact, printed as an error row by
// a run that died on the ceiling before the wrap-up field existed (suga-p5mt: the job's
// last line, with no result row at all).
const (
	WrapUpMaxSteps  = "max_steps"
	WrapUpMaxTokens = "max_tokens"
	WrapUpTimeout   = "timeout"
	// WrapUpBudget is the agent winding the run up because its soft budget (agent_tokens
	// / agent_cost_usd) ran out — distinct from the step ceiling and the time limit.
	WrapUpBudget = "budget"
)

// NormalizeWrapUp maps a reason the stream carried onto its canonical spelling. An
// unknown value is returned trimmed and lower-cased rather than dropped: a future
// agent ceiling must still reach the job.
func NormalizeWrapUp(v string) string {
	s := strings.ToLower(strings.TrimSpace(v))
	switch s {
	case "max_steps", "max-steps", "maxsteps", "max steps", "max steps exceeded", "step_limit", "steps":
		return WrapUpMaxSteps
	case "max_tokens", "max-tokens", "maxtokens", "token_limit", "tokens":
		return WrapUpMaxTokens
	case "timeout", "time_limit", "deadline":
		return WrapUpTimeout
	case "budget", "budget_exhausted", "max_cost_usd", "max_cost", "cost_budget":
		return WrapUpBudget
	}
	return s
}

// signalCounter accumulates Signals from the RAW parsed lines, before the keep
// whitelist, so the counts do not depend on a log-volume setting (same rule as the
// budget meter).
//
//   - claude: the `result` row's num_turns is the run's turn count (fallback: the
//     distinct assistant message ids, for a run killed before `result`); every
//     distinct tool_use block of an assistant message is one tool call; the `system`
//     init row names the model.
//   - omp: one `turn_end` per turn (fallback: completed assistant messages); one
//     `tool_execution_start` per tool call; the `session` row names the model.
type signalCounter struct {
	kind        string
	seen        bool
	msgIDs      map[string]struct{}
	toolIDs     map[string]struct{}
	tools       int64
	resultTurns int64
	turnEnds    int64
	assistants  int64
	model       string
	// wrapUp is the agent's own 收尾原因 (gofer-gk74); first one wins, and it is
	// collected for EVERY dialect because it is a property of the run, not of the
	// projector that compacts it.
	wrapUp string
	// agentBudget is the latest soft-budget snapshot seen on any row.
	agentBudget *runner.AgentBudgetUsage
}

func (c *signalCounter) observe(typ string, obj map[string]any) {
	c.observeWrapUp(typ, obj)
	if b := runner.AgentBudgetFromLine(obj); b != nil {
		c.agentBudget = b
	}
	switch c.kind {
	case ProjectorClaude:
		c.observeClaude(typ, obj)
	case ProjectorOMP:
		c.observeOMP(typ, obj)
	}
}

// observeWrapUp reads the 收尾原因 of the row. Two spellings of it:
//
//   - suga's `result` row carries wrap_up=max_steps|max_tokens|timeout when the run
//     ended on a ceiling; an ordinary answer carries no such field at all.
//   - a run killed by the step ceiling (before suga reported the field) leaves
//     {"type":"error","error":"max steps exceeded: 100"} and NO result row — the
//     message IS the wrap-up reason, so it is read as one.
func (c *signalCounter) observeWrapUp(typ string, obj map[string]any) {
	if c.wrapUp != "" {
		return
	}
	switch typ {
	case "result":
		if v := stringField(obj, "wrap_up"); v != "" {
			c.wrapUp = NormalizeWrapUp(v)
		}
	case "error":
		if msg := strings.ToLower(stringField(obj, "error")); strings.Contains(msg, "max steps exceeded") {
			c.wrapUp = WrapUpMaxSteps
		}
	}
}

func (c *signalCounter) observeClaude(typ string, obj map[string]any) {
	switch typ {
	case "system":
		if m := stringField(obj, "model"); m != "" && c.model == "" {
			c.model = m
		}
	case "assistant":
		c.seen = true
		if id := pathString(obj, "message", "id"); id != "" {
			if c.msgIDs == nil {
				c.msgIDs = map[string]struct{}{}
			}
			c.msgIDs[id] = struct{}{}
		}
		if m := pathString(obj, "message", "model"); m != "" && c.model == "" {
			c.model = m
		}
		for _, b := range contentBlocks(obj) {
			m, ok := b.(map[string]any)
			if !ok || stringField(m, "type") != "tool_use" {
				continue
			}
			id := stringField(m, "id")
			if id == "" {
				c.tools++
				continue
			}
			if c.toolIDs == nil {
				c.toolIDs = map[string]struct{}{}
			}
			if _, dup := c.toolIDs[id]; !dup {
				c.toolIDs[id] = struct{}{}
				c.tools++
			}
		}
	case "result":
		c.seen = true
		if n, ok := obj["num_turns"].(float64); ok && n > 0 {
			c.resultTurns = int64(n)
		}
	}
}

func (c *signalCounter) observeOMP(typ string, obj map[string]any) {
	switch typ {
	case "session":
		if m := stringField(obj, "model"); m != "" && c.model == "" {
			c.model = m
		}
	case "turn_end":
		c.seen = true
		c.turnEnds++
		if m := stringField(obj, "model"); m != "" && c.model == "" {
			c.model = m
		}
	case "tool_execution_start":
		c.seen = true
		c.tools++
	case "message_end":
		if pathString(obj, "message", "role") == "assistant" {
			c.seen = true
			c.assistants++
		}
	}
}

func (c *signalCounter) result() Signals {
	if !c.seen {
		return Signals{Model: c.model, WrapUp: c.wrapUp, AgentBudget: c.agentBudget}
	}
	out := Signals{ToolCalls: c.tools, Model: c.model, WrapUp: c.wrapUp, AgentBudget: c.agentBudget, Known: true}
	switch c.kind {
	case ProjectorClaude:
		out.Turns = c.resultTurns
		if out.Turns == 0 {
			out.Turns = int64(len(c.msgIDs))
		}
	case ProjectorOMP:
		out.Turns = c.turnEnds
		if out.Turns == 0 {
			out.Turns = c.assistants
		}
	}
	return out
}

// Signals returns what the stream said about turns / tool calls / model so far.
func (f *Filter) Signals() Signals { return f.signals.result() }

// CountCompactSignals reads a job's COMPACT event stream (the stderr.log this filter
// wrote) and recovers the same Signals for a job that ended before the live counter
// existed (`gofer tool stats-backfill`). The compact lines are whatever the keep
// whitelist let through, so this is best-effort: claude needs its `result` line (the
// compact `assistant` line lists its tool_use blocks under `tools`), omp its
// `turn_end` / `tool_execution_start` lines. A dialect it does not know reports
// Known=false.
func CountCompactSignals(r io.Reader, projector string) Signals {
	c := signalCounter{kind: projector}
	if projector != ProjectorClaude && projector != ProjectorOMP {
		return Signals{}
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), DefaultMaxLineBytes)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var obj map[string]any
		if json.Unmarshal([]byte(line), &obj) != nil {
			continue
		}
		typ := stringField(obj, "type")
		if projector == ProjectorClaude && typ == "assistant" {
			// The compact assistant line carries the tool_use summaries, not the blocks.
			c.seen = true
			if tools, ok := obj["tools"].([]any); ok {
				c.tools += int64(len(tools))
			}
			continue
		}
		c.observe(typ, obj)
	}
	if projector == ProjectorClaude && c.resultTurns == 0 {
		// No `result` line survived (killed run, or the whitelist dropped it): the
		// compact stream keeps no message ids, so the turn count is unknown.
		return Signals{Model: c.model}
	}
	return c.result()
}
