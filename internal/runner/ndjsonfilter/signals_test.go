package ndjsonfilter

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

func signalsOf(t *testing.T, opt Options, lines ...string) (Signals, string) {
	t.Helper()
	var events bytes.Buffer
	f := New(io.Discard, &events, opt)
	_, err := f.Write([]byte(strings.Join(lines, "\n") + "\n"))
	assert.NoErr(t, err)
	_ = f.Close()
	return f.Signals(), events.String()
}

var claudeRun = []string{
	`{"type":"system","subtype":"init","session_id":"s1","model":"m-claude","tools":["Bash"]}`,
	`{"type":"assistant","message":{"id":"a","model":"m-claude","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}}]}}`,
	`{"type":"assistant","message":{"id":"a","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}}]}}`,
	`{"type":"assistant","message":{"id":"b","content":[{"type":"tool_use","id":"t2","name":"Read","input":{}},{"type":"text","text":"ok"}]}}`,
	`{"type":"stream_event","event":{}}`,
	`{"type":"result","subtype":"success","result":"done","num_turns":3,"usage":{"input_tokens":1}}`,
}

// TestSignalsClaude: turns are the result row's num_turns, tool calls the distinct
// tool_use ids, the model the init row's — and the keep whitelist does not matter.
func TestSignalsClaude(t *testing.T) {
	sig, _ := signalsOf(t, Options{Projector: ProjectorClaude, Keep: []string{"result"}}, claudeRun...)
	assert.Eq(t, Signals{Turns: 3, ToolCalls: 2, Model: "m-claude", Known: true}, sig)

	// Killed before `result`: fall back to the distinct assistant messages.
	sig, _ = signalsOf(t, Options{Projector: ProjectorClaude}, claudeRun[:4]...)
	assert.Eq(t, int64(2), sig.Turns)
	assert.True(t, sig.Known)
}

// TestSignalsOMP: one turn per turn_end, one tool call per tool_execution_start.
func TestSignalsOMP(t *testing.T) {
	sig, _ := signalsOf(t, Options{Projector: ProjectorOMP},
		`{"type":"session","id":"x","model":"m-omp"}`,
		`{"type":"tool_execution_start","toolName":"bash"}`,
		`{"type":"tool_execution_start","toolName":"read"}`,
		`{"type":"turn_end","model":"m-omp"}`,
		`{"type":"turn_end"}`,
	)
	assert.Eq(t, Signals{Turns: 2, ToolCalls: 2, Model: "m-omp", Known: true}, sig)
}

// TestSignalsGenericUnknown: a dialect the counter does not know reports nothing.
func TestSignalsGenericUnknown(t *testing.T) {
	sig, _ := signalsOf(t, Options{Projector: ProjectorGeneric}, claudeRun...)
	assert.False(t, sig.Known)
	assert.Eq(t, int64(0), sig.Turns)
}

// TestCountCompactSignals: the backfill reads the compact event stream this filter
// wrote and recovers the same counts; without the result line claude's turns are
// unknown rather than zero.
func TestCountCompactSignals(t *testing.T) {
	_, events := signalsOf(t, Options{Projector: ProjectorClaude}, claudeRun...)
	sig := CountCompactSignals(strings.NewReader(events), ProjectorClaude)
	assert.True(t, sig.Known)
	assert.Eq(t, int64(3), sig.Turns)
	assert.Eq(t, "m-claude", sig.Model)
	// The compact assistant lines list each line's tool_use blocks (the duplicated
	// message line is counted again: the compact stream has no ids to dedupe).
	assert.Eq(t, int64(3), sig.ToolCalls)

	_, events = signalsOf(t, Options{Projector: ProjectorClaude}, claudeRun[:4]...)
	assert.False(t, CountCompactSignals(strings.NewReader(events), ProjectorClaude).Known)

	assert.False(t, CountCompactSignals(strings.NewReader(events), ProjectorGeneric).Known)
}
