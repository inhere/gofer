package commands

import (
	"testing"

	"github.com/inhere/gofer/internal/hookrelay"
)

// gofer-02fu: an AskUserQuestion PreToolUse needs the session hub, so it must not
// take the hub-less command-memory path (which ran it with a nil API and crashed).
func TestPreToolUseFastPathExcludesAskUserQuestion(t *testing.T) {
	if !preToolUseFastPath(hookrelay.Payload{Event: "PreToolUse", ToolName: "Bash"}) {
		t.Fatal("a shell PreToolUse keeps the fast path")
	}
	if preToolUseFastPath(hookrelay.Payload{Event: "PreToolUse", ToolName: hookrelay.AskUserQuestionTool}) {
		t.Fatal("AskUserQuestion must take the full hook path")
	}
}
