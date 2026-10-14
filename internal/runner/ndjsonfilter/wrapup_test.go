package ndjsonfilter

import (
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// sugaRun is a `suga run --output-format stream-json` stream that hit the step ceiling:
// its result row says so. A normal answer carries no wrap_up field at all.
var sugaRun = []string{
	`{"type":"session","id":"s-1"}`,
	`{"type":"turn_start"}`,
	`{"type":"tool_call","name":"bash"}`,
	`{"type":"result","result":"汇报正文","wrap_up":"max_steps"}`,
}

// TestSugaWrapUpSignals (gofer-gk74): the agent's own 收尾原因 is read from its stream
// even though suga runs the GENERIC projector (its turn shape stays unknown).
func TestSugaWrapUpSignals(t *testing.T) {
	sig, _ := signalsOf(t, Options{Projector: ProjectorGeneric, Keep: []string{"result"}}, sugaRun...)
	assert.Eq(t, WrapUpMaxSteps, sig.WrapUp)
	assert.False(t, sig.Known, "the generic projector still reports no turn shape")

	// A normal answer: no field, no wrap-up.
	sig, _ = signalsOf(t, Options{Projector: ProjectorGeneric, Keep: []string{"result"}},
		`{"type":"result","result":"汇报正文"}`)
	assert.Eq(t, "", sig.WrapUp)

	// The field is read through the keep whitelist (it comes off the raw parse), and
	// the first reason of a stream wins.
	sig, _ = signalsOf(t, Options{Projector: ProjectorGeneric},
		`{"type":"result","wrap_up":"max_tokens"}`,
		`{"type":"result","wrap_up":"timeout"}`)
	assert.Eq(t, WrapUpMaxTokens, sig.WrapUp)
}

// TestWrapUpFromErrorRow: a run that died on the step ceiling before suga reported the
// field leaves an error row and NO result row — that message IS the reason (suga-p5mt),
// and gofer must still mark the job.
func TestWrapUpFromErrorRow(t *testing.T) {
	sig, events := signalsOf(t, Options{Projector: ProjectorGeneric, Keep: []string{"error"}},
		`{"type":"turn_start"}`,
		`{"type":"error","error":"max steps exceeded: 100"}`)
	assert.Eq(t, WrapUpMaxSteps, sig.WrapUp)

	// An unrelated error row is not a wrap-up.
	sig, _ = signalsOf(t, Options{Projector: ProjectorGeneric},
		`{"type":"error","error":"model overloaded"}`)
	assert.Eq(t, "", sig.WrapUp)

	// The error row still reaches the event stream an operator reads.
	assert.True(t, strings.Contains(events, "max steps exceeded"))
}

// TestNormalizeWrapUp: the known spellings collapse onto the three canonical reasons;
// anything else is kept (trimmed, lower-cased) so a future ceiling is still visible.
func TestNormalizeWrapUp(t *testing.T) {
	for _, in := range []string{"max_steps", "max-steps", "MAX STEPS EXCEEDED", " max steps "} {
		assert.Eq(t, WrapUpMaxSteps, NormalizeWrapUp(in), in)
	}
	for _, in := range []string{"max_tokens", "max-tokens"} {
		assert.Eq(t, WrapUpMaxTokens, NormalizeWrapUp(in), in)
	}
	assert.Eq(t, WrapUpTimeout, NormalizeWrapUp(" Timeout "))
	assert.Eq(t, "wall_clock", NormalizeWrapUp(" Wall_Clock "))
	assert.Eq(t, "", NormalizeWrapUp("   "))
}

// TestWrapUpDoesNotDisturbKnownDialects: the reasons ride along with the counts claude
// and omp already report, and a stream without the field keeps them exactly as before.
func TestWrapUpDoesNotDisturbKnownDialects(t *testing.T) {
	sig, _ := signalsOf(t, Options{Projector: ProjectorClaude}, claudeRun...)
	assert.Eq(t, Signals{Turns: 3, ToolCalls: 2, Model: "m-claude", Known: true}, sig)

	sig, _ = signalsOf(t, Options{Projector: ProjectorOMP},
		`{"type":"turn_end","model":"m-omp"}`,
		`{"type":"result","wrap_up":"max_tokens"}`)
	assert.Eq(t, int64(1), sig.Turns)
	assert.Eq(t, WrapUpMaxTokens, sig.WrapUp)
}
