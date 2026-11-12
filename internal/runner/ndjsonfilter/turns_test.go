package ndjsonfilter

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// runTurns feeds lines through a Filter that writes turns.jsonl into the returned
// records.
func runTurns(t *testing.T, dialect string, mask func(string) string, lines []string) []TurnRecord {
	t.Helper()
	var turns bytes.Buffer
	var stdout, events bytes.Buffer
	f := New(&stdout, &events, Options{Keep: []string{"session"}, Projector: ProjectorGeneric, Turns: NewTurnWriter(&turns, mask), TurnDialect: dialect})
	if _, err := f.Write([]byte(sampleText(lines))); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return decodeTurns(t, turns.String())
}

func decodeTurns(t *testing.T, s string) []TurnRecord {
	t.Helper()
	var out []TurnRecord
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if l == "" {
			continue
		}
		var r TurnRecord
		if err := json.Unmarshal([]byte(l), &r); err != nil {
			t.Fatalf("turns line %q: %v", l, err)
		}
		out = append(out, r)
	}
	return out
}

func TestTurnsClaudeWritesOneRecordPerRun(t *testing.T) {
	recs := runTurns(t, TurnsClaude, nil, []string{
		`{"type":"system","session_id":"s1"}`,
		`{"type":"assistant","message":{"id":"m1","content":[{"type":"text","text":"looking"},{"type":"tool_use","id":"t1","name":"Read","input":{}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"x"}]}}`,
		`{"type":"assistant","message":{"id":"m2","content":[{"type":"tool_use","id":"t2","name":"Read","input":{}},{"type":"text","text":"all done"}]}}`,
		`{"type":"result","result":"all done","usage":{"input_tokens":10,"output_tokens":5}}`,
	})
	if len(recs) != 1 {
		t.Fatalf("records = %+v, want 1", recs)
	}
	r := recs[0]
	if r.Turn != 1 || r.Role != "assistant" || r.Text != "looking\n\nall done" || r.Tools["Read"] != 2 || r.Tokens != 15 {
		t.Fatalf("record = %+v", r)
	}
}

func TestTurnsClaudeKilledRunWritesAtClose(t *testing.T) {
	recs := runTurns(t, TurnsClaude, nil, []string{
		`{"type":"assistant","message":{"id":"m1","content":[{"type":"text","text":"partial"}]}}`,
	})
	if len(recs) != 1 || recs[0].Text != "partial" {
		t.Fatalf("records = %+v", recs)
	}
}

// Two suga turns; message_delta rows (dropped by the keep whitelist) are accumulated.
func TestTurnsSugaAccumulatesDeltasPerTurn(t *testing.T) {
	recs := runTurns(t, TurnsSuga, nil, []string{
		`{"type":"turn_start","run_id":"r1"}`,
		`{"type":"message_delta","text":"Hel"}`,
		`{"type":"message_delta","text":"lo"}`,
		`{"type":"tool_call","name":"bash"}`,
		`{"type":"tool_result","name":"bash"}`,
		`{"type":"message_delta","text":" there"}`,
		`{"type":"turn_end","usage":{"total_tokens":42}}`,
		`{"type":"turn_start","run_id":"r2"}`,
		`{"type":"message_delta","text":"second"}`,
		`{"type":"turn_end"}`,
		`{"type":"result","result":"second"}`,
	})
	if len(recs) != 2 {
		t.Fatalf("records = %+v, want 2", recs)
	}
	if recs[0].Turn != 1 || recs[0].Text != "Hello there" || recs[0].Tools["bash"] != 1 || recs[0].Tokens != 42 {
		t.Fatalf("turn 1 = %+v", recs[0])
	}
	if recs[1].Turn != 2 || recs[1].Text != "second" {
		t.Fatalf("turn 2 = %+v", recs[1])
	}
}

// retry{restart:true} re-sends the model call from scratch: only the text since the
// latest tool_result / turn_start is dropped.
func TestTurnsSugaRetryRestartKeepsOnlyLatestSegment(t *testing.T) {
	recs := runTurns(t, TurnsSuga, nil, []string{
		`{"type":"turn_start"}`,
		`{"type":"message_delta","text":"before tool. "}`,
		`{"type":"tool_call","name":"bash"}`,
		`{"type":"tool_result","name":"bash"}`,
		`{"type":"message_delta","text":"half an ans"}`,
		`{"type":"retry","restart":true}`,
		`{"type":"message_delta","text":"full answer"}`,
		`{"type":"turn_end"}`,
	})
	if len(recs) != 1 || recs[0].Text != "before tool. full answer" {
		t.Fatalf("records = %+v", recs)
	}
	// A restart right after turn_start drops the whole turn text so far.
	recs = runTurns(t, TurnsSuga, nil, []string{
		`{"type":"turn_start"}`,
		`{"type":"message_delta","text":"junk"}`,
		`{"type":"retry","restart":true}`,
		`{"type":"message_delta","text":"clean"}`,
		`{"type":"turn_end"}`,
	})
	if len(recs) != 1 || recs[0].Text != "clean" {
		t.Fatalf("records = %+v", recs)
	}
	// restart:false changes nothing.
	recs = runTurns(t, TurnsSuga, nil, []string{
		`{"type":"turn_start"}`,
		`{"type":"message_delta","text":"keep"}`,
		`{"type":"retry","restart":false}`,
		`{"type":"turn_end"}`,
	})
	if len(recs) != 1 || recs[0].Text != "keep" {
		t.Fatalf("records = %+v", recs)
	}
}

func TestTurnsUnknownDialectWritesNothing(t *testing.T) {
	var turns bytes.Buffer
	f := New(&bytes.Buffer{}, &bytes.Buffer{}, Options{Turns: NewTurnWriter(&turns, nil), TurnDialect: ""})
	_, _ = f.Write([]byte(`{"type":"result","result":"x"}` + "\n"))
	_ = f.Close()
	if turns.Len() != 0 {
		t.Fatalf("turns.jsonl = %q, want nothing for an unregistered dialect", turns.String())
	}
}

func TestTurnsTextIsCutAtLimitOnRuneBoundary(t *testing.T) {
	big := strings.Repeat("界", MaxTurnTextBytes) // 3 bytes each: far over the cap
	recs := runTurns(t, TurnsClaude, nil, []string{
		`{"type":"assistant","message":{"id":"m","content":[{"type":"text","text":"` + big + `"}]}}`,
		`{"type":"result","result":"x"}`,
	})
	if len(recs) != 1 || !recs[0].Truncated || len(recs[0].Text) > MaxTurnTextBytes || !utf8.ValidString(recs[0].Text) {
		t.Fatalf("record truncated=%v len=%d valid=%v", recs[0].Truncated, len(recs[0].Text), utf8.ValidString(recs[0].Text))
	}
}

func TestTurnsMaskCoversSecretSplitAcrossDeltasAndTheCutBoundary(t *testing.T) {
	const secret = "ghp_example_secret_0123456789"
	mask := func(s string) string { return strings.ReplaceAll(s, secret, "***") }
	lines := []string{`{"type":"turn_start"}`,
		`{"type":"message_delta","text":"token is ghp_example_sec"}`,
		`{"type":"message_delta","text":"ret_0123456789 ok"}`,
	}
	// Place a secret so that it straddles MaxTurnTextBytes after masking would shift
	// nothing: it sits right across the 256KB mark of the raw text.
	pad := strings.Repeat("a", MaxTurnTextBytes-10)
	lines = append(lines, `{"type":"message_delta","text":"`+pad+secret+`"}`, `{"type":"turn_end"}`)
	recs := runTurns(t, TurnsSuga, mask, lines)
	if len(recs) != 1 {
		t.Fatalf("records = %+v", len(recs))
	}
	if strings.Contains(recs[0].Text, "ghp_example") || strings.Contains(recs[0].Text, "0123456789") {
		t.Fatalf("secret (or a fragment) leaked into turns.jsonl")
	}
	if !strings.HasPrefix(recs[0].Text, "token is *** ok") {
		t.Fatalf("text head = %q", recs[0].Text[:30])
	}
}

// A secret with JSON-special characters must be masked before encoding, not matched
// against the escaped line.
func TestTurnsMaskRunsBeforeJSONEncoding(t *testing.T) {
	const secret = "pa\"ss\nword"
	mask := func(s string) string { return strings.ReplaceAll(s, secret, "***") }
	var turns bytes.Buffer
	w := NewTurnWriter(&turns, mask)
	if err := w.WriteTurn(TurnRecord{Turn: 1, Role: "assistant", Text: "x " + secret + " y", Error: secret}); err != nil {
		t.Fatal(err)
	}
	recs := decodeTurns(t, turns.String())
	if recs[0].Text != "x *** y" || recs[0].Error != "***" {
		t.Fatalf("record = %+v", recs[0])
	}
}

// The 1MB in-memory bound drops the middle of an endless turn; a secret inside the
// dropped region's neighbourhood is masked before the cut.
func TestTurnsBufferBoundKeepsHeadAndTail(t *testing.T) {
	var turns bytes.Buffer
	w := NewTurnWriter(&turns, nil)
	acc := newTurnObserver(TurnsSuga, w)
	acc.observe("turn_start", map[string]any{})
	chunk := strings.Repeat("x", 100<<10)
	for i := 0; i < 20; i++ { // 2MB
		acc.observe("message_delta", map[string]any{"text": chunk})
	}
	acc.observe("message_delta", map[string]any{"text": "THE-END"})
	acc.observe("turn_end", map[string]any{})
	recs := decodeTurns(t, turns.String())
	if len(recs) != 1 || !recs[0].Truncated || !strings.HasSuffix(recs[0].Text[:min(len(recs[0].Text), MaxTurnTextBytes)], "x") {
		t.Fatalf("records = %d truncated=%v", len(recs), len(recs) == 1 && recs[0].Truncated)
	}
}
