package hookrelay

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/client"
	rusage "github.com/inhere/gofer/internal/runner"
)

type usageEnv struct {
	t          *testing.T
	api        *fakeAPI
	sid        string
	transcript string
	stateDir   string
	agent      string
	budget     int64
}

func newUsageEnv(t *testing.T, agent string) *usageEnv {
	t.Helper()
	api := newFake()
	_, _ = api.RegisterSession(client.SessionRegister{SessionID: "u-sid", Agent: agent})
	dir := t.TempDir()
	return &usageEnv{t: t, api: api, sid: "u-sid", agent: agent, stateDir: filepath.Join(dir, "state"),
		transcript: filepath.Join(dir, "proj", "u-sid.jsonl")}
}

func (e *usageEnv) write(path string, lines ...string) {
	e.t.Helper()
	assert.NoErr(e.t, os.MkdirAll(filepath.Dir(path), 0o755))
	assert.NoErr(e.t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
}

func (e *usageEnv) appendTo(path string, raw string) {
	e.t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	assert.NoErr(e.t, err)
	_, err = f.WriteString(raw)
	assert.NoErr(e.t, err)
	assert.NoErr(e.t, f.Close())
}

// beat runs one hook event and returns the usage_delta its heartbeat carried (nil = none).
func (e *usageEnv) beat(event string) *rusage.SessionUsage {
	e.t.Helper()
	e.api.mu.Lock()
	before := len(e.api.beats)
	e.api.mu.Unlock()
	p := Payload{Agent: e.agent, Event: event, SessionID: e.sid, TranscriptPath: e.transcript}
	_, err := Run(e.api, p, Options{UsageStateDir: e.stateDir, UsageReadBudget: e.budget, Wait: 1})
	assert.NoErr(e.t, err)
	e.api.mu.Lock()
	defer e.api.mu.Unlock()
	for _, hb := range e.api.beats[before:] {
		if hb.UsageDelta != nil {
			return hb.UsageDelta
		}
	}
	return nil
}

func claudeRow(id, model string, in, out, cr, cw int, sidechain bool) string {
	return fmt.Sprintf(`{"type":"assistant","isSidechain":%v,"message":{"id":%q,"model":%q,"role":"assistant","content":[],"usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d,"service_tier":"standard"}}}`,
		sidechain, id, model, in, out, cr, cw)
}

func TestUsageClaudeDedupSidechainSubagentsModels(t *testing.T) {
	e := newUsageEnv(t, AgentClaude)
	e.write(e.transcript,
		`{"type":"user","message":{"role":"user","content":"hi"}}`,
		claudeRow("m1", "claude-opus", 10, 100, 1000, 50, false),
		claudeRow("m1", "claude-opus", 10, 100, 1000, 50, false), // same message, 2nd content block
		claudeRow("m2", "claude-sonnet", 5, 20, 0, 0, false),
		claudeRow("s0", "claude-sonnet", 7, 70, 0, 0, true), // sidechain row inside the main file
		`{"type":"system","note":"no usage here"}`,
		`not json "usage" at all`,
	)
	e.write(filepath.Join(strings.TrimSuffix(e.transcript, ".jsonl"), "subagents", "agent-a1.jsonl"),
		claudeRow("a1", "claude-sonnet", 3, 30, 300, 0, true),
		claudeRow("a1", "claude-sonnet", 3, 30, 300, 0, true),
	)
	d := e.beat("SubagentStop")
	if d == nil {
		t.Fatal("no usage_delta on SubagentStop")
	}
	assert.Eq(t, int64(15), d.Main.InputTokens)
	assert.Eq(t, int64(120), d.Main.OutputTokens)
	assert.Eq(t, int64(1000), d.Main.CacheReadTokens)
	assert.Eq(t, int64(50), d.Main.CacheWriteTokens)
	assert.Eq(t, int64(15+120+1000+50), d.Main.TotalTokens)
	assert.Eq(t, int64(7+3), d.Sub.InputTokens)
	assert.Eq(t, int64(100), d.Sub.OutputTokens)
	assert.Eq(t, int64(300), d.Sub.CacheReadTokens)
	assert.Eq(t, 0.0, d.Main.CostUSD)
	assert.Eq(t, int64(10+100+1000+50), d.ByModel["claude-opus"].TotalTokens)
	assert.Eq(t, int64(5+20+7+70+3+30+300), d.ByModel["claude-sonnet"].TotalTokens)

	// nothing new: no usage on the next event, even a repeat of the same one
	assert.Nil(t, e.beat("SessionEnd"))
	assert.Nil(t, e.beat("Stop"))

	// appended rows are counted once; a repeat of an already counted message is not
	e.appendTo(e.transcript, claudeRow("m3", "claude-opus", 1, 2, 0, 0, false)+"\n"+claudeRow("m2", "claude-sonnet", 5, 20, 0, 0, false)+"\n")
	d = e.beat("Stop")
	if d == nil {
		t.Fatal("expected a delta for the appended row")
	}
	assert.Eq(t, int64(3), d.Main.TotalTokens)
	assert.Eq(t, int64(0), d.Sub.TotalTokens)
	assert.Eq(t, 1, len(d.ByModel))
}

func TestUsageClaudeTruncatedRereadDoesNotDoubleCount(t *testing.T) {
	e := newUsageEnv(t, AgentClaude)
	e.write(e.transcript, claudeRow("m1", "claude-opus", 10, 100, 0, 0, false), claudeRow("m2", "claude-opus", 1, 1, 0, 0, false))
	d := e.beat("Stop")
	assert.Eq(t, int64(112), d.Main.TotalTokens)
	// the file is rewritten shorter (compaction): the same messages are not recounted,
	// a new one is.
	e.write(e.transcript, `{"type":"summary","summary":"compacted conversation, earlier turns folded into this line"}`,
		claudeRow("m1", "claude-opus", 10, 100, 0, 0, false), claudeRow("m9", "claude-opus", 2, 2, 0, 0, false))
	d = e.beat("Stop")
	if d == nil {
		t.Fatal("expected the new message after truncation")
	}
	assert.Eq(t, int64(4), d.Main.TotalTokens)
}

func TestUsagePartialLastLineWaitsForNewline(t *testing.T) {
	e := newUsageEnv(t, AgentClaude)
	full := claudeRow("m1", "claude-opus", 1, 1, 0, 0, false)
	e.write(e.transcript, full)
	e.appendTo(e.transcript, full[:len(full)/2]) // a writer mid-append
	d := e.beat("Stop")
	assert.Eq(t, int64(2), d.Main.TotalTokens)
	e.appendTo(e.transcript, full[len(full)/2:]+"\n") // finished: but the id was counted above
	assert.Nil(t, e.beat("Stop"))
}

func TestUsageReadBudgetLeavesRestForNextEvent(t *testing.T) {
	e := newUsageEnv(t, AgentClaude)
	var lines []string
	for i := 0; i < 6; i++ {
		lines = append(lines, claudeRow(fmt.Sprintf("m%d", i), "claude-opus", 1, 1, 0, 0, false))
	}
	e.write(e.transcript, lines...)
	e.budget = int64(len(lines[0])+1) * 2 // two lines per event
	var total int64
	for i := 0; i < 6; i++ {
		if d := e.beat("Stop"); d != nil {
			total += d.Main.TotalTokens
		}
	}
	assert.Eq(t, int64(12), total)
}

func TestUsageFailedBeatKeepsOffsetForRetry(t *testing.T) {
	e := newUsageEnv(t, AgentClaude)
	e.write(e.transcript, claudeRow("m1", "claude-opus", 4, 6, 0, 0, false))
	e.api.failHeartbeat = errors.New("hub down")
	assert.Nil(t, e.beat("Stop"))
	e.api.failHeartbeat = nil
	d := e.beat("Stop")
	if d == nil {
		t.Fatal("the delta must be re-sent after a failed beat")
	}
	assert.Eq(t, int64(10), d.Main.TotalTokens)
	assert.Nil(t, e.beat("Stop"))
}

func TestUsageNotCollectedOnPostToolUseOrWithoutStateDir(t *testing.T) {
	e := newUsageEnv(t, AgentClaude)
	e.write(e.transcript, claudeRow("m1", "claude-opus", 1, 1, 0, 0, false))
	assert.Nil(t, e.beat("PostToolUse"))
	e.stateDir = ""
	assert.Nil(t, e.beat("Stop"))
}

func TestUsageMissingTranscriptIsHarmless(t *testing.T) {
	e := newUsageEnv(t, AgentClaude)
	assert.Nil(t, e.beat("Stop"))
	e.api.mu.Lock()
	defer e.api.mu.Unlock()
	if len(e.api.beats) == 0 {
		t.Fatal("the heartbeat must still go out")
	}
}

func codexCount(in, cached, out, total int) string {
	return fmt.Sprintf(`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"cached_input_tokens":%d,"output_tokens":%d,"reasoning_output_tokens":0,"total_tokens":%d}}}}`,
		in, cached, out, total)
}

func TestUsageCodexCumulativeDifference(t *testing.T) {
	e := newUsageEnv(t, AgentCodex)
	e.write(e.transcript,
		`{"type":"turn_context","payload":{"model":"gpt-5-codex"}}`,
		`{"type":"event_msg","payload":{"type":"token_count","info":null}}`,
		codexCount(100, 40, 10, 110),
		codexCount(300, 100, 30, 330),
	)
	d := e.beat("Stop")
	if d == nil {
		t.Fatal("no codex usage")
	}
	assert.Eq(t, int64(200), d.Main.InputTokens)
	assert.Eq(t, int64(100), d.Main.CacheReadTokens)
	assert.Eq(t, int64(30), d.Main.OutputTokens)
	assert.Eq(t, int64(330), d.Main.TotalTokens)
	assert.Eq(t, int64(330), d.ByModel["gpt-5-codex"].TotalTokens)
	assert.Nil(t, e.beat("Stop"))

	e.appendTo(e.transcript, codexCount(350, 120, 50, 400)+"\n")
	d = e.beat("Stop")
	assert.Eq(t, int64(70), d.Main.TotalTokens)
	assert.Eq(t, int64(20), d.Main.OutputTokens)
	assert.Eq(t, int64(20), d.Main.CacheReadTokens)
}

func TestUsageOmpAndGeneric(t *testing.T) {
	e := newUsageEnv(t, AgentOmp)
	e.write(e.transcript,
		`{"type":"model_change","id":"m0","model":"omp-model"}`,
		`{"type":"message","id":"a1","message":{"role":"assistant","usage":{"input":10,"output":5,"cacheRead":2,"cacheWrite":1,"totalTokens":18,"cost":{"total":0.5}}}}`,
		`{"type":"message","id":"a1","message":{"role":"assistant","usage":{"input":10,"output":5,"cacheRead":2,"cacheWrite":1,"totalTokens":18}}}`,
		`{"type":"message","id":"a2","message":{"role":"assistant","usage":{}}}`,
	)
	d := e.beat("Stop")
	if d == nil {
		t.Fatal("no omp usage")
	}
	assert.Eq(t, int64(18), d.Main.TotalTokens)
	assert.Eq(t, 0.5, d.Main.CostUSD)
	assert.Eq(t, int64(18), d.ByModel["omp-model"].TotalTokens)

	g := newUsageEnv(t, DialectGeneric)
	g.write(g.transcript, `{"v":1,"type":"assistant","id":"x","usage":{"input_tokens":3,"output_tokens":4}}`, `{"v":1,"type":"assistant","text":"no usage"}`)
	d = g.beat("SessionEnd")
	if d == nil {
		t.Fatal("no generic usage")
	}
	assert.Eq(t, int64(7), d.Main.TotalTokens)
	assert.Eq(t, int64(7), d.ByModel["unknown"].TotalTokens)
}

func TestUsageSkipsJcodeAndPrunesOldState(t *testing.T) {
	e := newUsageEnv(t, AgentJcode)
	e.write(e.transcript, claudeRow("m1", "m", 1, 1, 0, 0, false))
	assert.Nil(t, e.beat("Stop"))
	assert.NoErr(t, os.MkdirAll(e.stateDir, 0o700))
	old := filepath.Join(e.stateDir, "old.json")
	assert.NoErr(t, os.WriteFile(old, []byte("{}"), 0o600))
	assert.NoErr(t, os.Chtimes(old, timeAgo(60), timeAgo(60)))
	pruneUsageState(e.stateDir, timeNow())
	if _, err := os.Stat(old); err == nil {
		t.Fatal("old state file should be pruned")
	}
}

func timeNow() time.Time         { return time.Now() }
func timeAgo(days int) time.Time { return time.Now().Add(-time.Duration(days) * 24 * time.Hour) }
