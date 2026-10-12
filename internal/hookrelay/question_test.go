package hookrelay

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/client"
)

func questionPayload(t *testing.T) Payload {
	return payload(t, "claude", map[string]any{
		"session_id": "s1", "hook_event_name": "PreToolUse", "cwd": "/w",
		"tool_name": "AskUserQuestion",
		"tool_input": map[string]any{"questions": []any{map[string]any{
			"question": "喜欢哪种颜色？", "header": "颜色", "multiSelect": false,
			"options": []any{map[string]any{"label": "红", "description": "红色"}, map[string]any{"label": "蓝", "description": "蓝色"}},
		}}},
	})
}

// gofer-02fu: with the person away, the question becomes a web card and the web
// answer is handed back as the tool's own input (updatedInput.answers).
func TestAskUserQuestionWaitsAndAnswersThroughInput(t *testing.T) {
	f := newFake()
	f.sessions["s1"] = client.AgentSession{SessionID: "s1", Agent: "claude", RelayMode: client.RelayModeOn, WaitReason: client.WaitModeOn}
	f.answerAfter, f.answer = 1, `answers:{"喜欢哪种颜色？":"蓝"}`
	var log strings.Builder
	res, err := Run(f, questionPayload(t), tickingOpts(t, &log, time.Minute))
	assert.NoErr(t, err)
	if res.PreTool == nil {
		t.Fatalf("no PreToolUse verdict; log:\n%s", log.String())
	}
	var in struct {
		Questions []map[string]any  `json:"questions"`
		Answers   map[string]string `json:"answers"`
	}
	assert.NoErr(t, json.Unmarshal(res.PreTool.UpdatedInput, &in))
	assert.Eq(t, 1, len(in.Questions))
	assert.Eq(t, "蓝", in.Answers["喜欢哪种颜色？"])
	opened := permOpened[f]
	assert.Eq(t, 1, len(opened))
	assert.Eq(t, "AskUserQuestion", opened[0].ToolName)
	assert.Eq(t, 1, len(opened[0].Questions))
	assert.Eq(t, 2, len(opened[0].Questions[0].Options))
	assert.Eq(t, "等你回答：喜欢哪种颜色？", f.beats[0].LastMessage)

	out := string(PreToolJSON(*res.PreTool))
	for _, want := range []string{`"hookEventName":"PreToolUse"`, `"permissionDecision":"allow"`, `"answers":{"喜欢哪种颜色？":"蓝"}`} {
		assert.True(t, strings.Contains(out, want), out)
	}
}

// The person at the keyboard: report only, the terminal asks.
func TestAskUserQuestionAtKeyboardLeavesItToTheTerminal(t *testing.T) {
	f := newFake()
	f.sessions["s1"] = client.AgentSession{SessionID: "s1", Agent: "claude", RelayMode: client.RelayModeAuto}
	var log strings.Builder
	res, err := Run(f, questionPayload(t), tickingOpts(t, &log, time.Minute))
	assert.NoErr(t, err)
	assert.Nil(t, res.PreTool)
	assert.Eq(t, 0, len(permOpened[f]))
	assert.Eq(t, 1, len(f.beats))
	assert.Eq(t, "PermissionRequest", f.beats[0].Event)
}

// No usable answer within the budget: nothing printed, the card closed.
func TestAskUserQuestionTimeoutReleases(t *testing.T) {
	f := newFake()
	f.sessions["s1"] = client.AgentSession{SessionID: "s1", Agent: "claude", RelayMode: client.RelayModeOn, WaitReason: client.WaitModeOn}
	var log strings.Builder
	res, err := Run(f, questionPayload(t), tickingOpts(t, &log, 4*time.Second))
	assert.NoErr(t, err)
	assert.Nil(t, res.PreTool)
	assert.True(t, len(permResolved[f]) > 0, log.String())
}
