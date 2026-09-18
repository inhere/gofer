package ndjsonfilter

import (
	"strings"
	"testing"
)

// TestOmpAllAssistantTextKeepsEveryTurn (bd h-aii-lvo9): with AllAssistantText the
// stdout carries every non-empty assistant message in order, blank-line separated,
// not just the last one.
func TestOmpAllAssistantTextKeepsEveryTurn(t *testing.T) {
	_, stdout, _ := project(t, Options{Keep: ompKeep, Projector: ProjectorOMP, AllAssistantText: true}, ompSample())
	if want := "turn 1 done\n\nturn 2 done\n"; stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
}

// TestOmpAllAssistantTextSurvivesTrailingTurn is the incident that motivated the
// mode: the agent wrote its report, a harness-injected notice made it answer once
// more with a one-liner, and "last assistant text" published only the one-liner.
func TestOmpAllAssistantTextSurvivesTrailingTurn(t *testing.T) {
	lines := ompSample()
	report := strings.Repeat("## report line\n", 20)
	lines = append(lines,
		`{"type":"message_end","turn":3,"message":{"role":"assistant","content":[{"type":"text","text":`+quote(report)+`}]}}`,
		`{"type":"message_end","turn":4,"message":{"role":"user","content":[{"type":"text","text":"[notice] background task finished"}]}}`,
		`{"type":"message_end","turn":4,"message":{"role":"assistant","content":[{"type":"text","text":"cleanup done, nothing else to add"}]}}`,
		`{"type":"turn_end","turn":4}`,
	)
	_, last, _ := project(t, Options{Keep: ompKeep, Projector: ProjectorOMP}, lines)
	if last != "cleanup done, nothing else to add\n" {
		t.Fatalf("final_text mode: stdout = %q, want only the trailing one-liner (the documented old behaviour)", last)
	}
	_, all, _ := project(t, Options{Keep: ompKeep, Projector: ProjectorOMP, AllAssistantText: true}, lines)
	if !strings.Contains(all, "## report line") || !strings.HasSuffix(all, "cleanup done, nothing else to add\n") {
		t.Fatalf("assistant_text mode must keep the report AND the trailer, got %q", all)
	}
	if strings.Contains(all, "[notice]") {
		t.Fatalf("user-role messages must not reach stdout: %q", all)
	}
}

// TestClaudeAllAssistantTextDedupsResult: claude's result.result repeats the last
// assistant text, so all mode appends it only when it differs.
func TestClaudeAllAssistantTextDedupsResult(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"init","session_id":"s1"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"looking at the repo"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"all done: 3 files changed"}]}}`,
		`{"type":"result","subtype":"success","result":"all done: 3 files changed","session_id":"s1"}`,
	}
	_, all, _ := project(t, Options{Projector: ProjectorClaude, AllAssistantText: true}, lines)
	if want := "looking at the repo\n\nall done: 3 files changed\n"; all != want {
		t.Fatalf("stdout = %q, want %q", all, want)
	}
	_, final, _ := project(t, Options{Projector: ProjectorClaude}, lines)
	if final != "all done: 3 files changed\n" {
		t.Fatalf("final_text mode: stdout = %q", final)
	}
}

func quote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return `"` + s + `"`
}
