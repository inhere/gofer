package ndjsonfilter

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
)

// Signals is what a structured agent stream says about the run's shape, for the
// dashboard's job metrics (gofer-yelm P2): how many agent turns it took, how many
// tool calls it made, and the model it ran on. Known=false means the stream dialect
// is not one the counter understands (generic agents) or it carried no evidence — the
// metrics then record nothing rather than a guessed zero.
type Signals struct {
	Turns     int64
	ToolCalls int64
	Model     string
	Known     bool
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
}

func (c *signalCounter) observe(typ string, obj map[string]any) {
	switch c.kind {
	case ProjectorClaude:
		c.observeClaude(typ, obj)
	case ProjectorOMP:
		c.observeOMP(typ, obj)
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
		return Signals{Model: c.model}
	}
	out := Signals{ToolCalls: c.tools, Model: c.model, Known: true}
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
