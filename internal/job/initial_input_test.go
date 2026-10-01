package job

import "testing"

func TestInteractiveInitialInputUsesPromptAsSubmittedText(t *testing.T) {
	if got := interactiveInitialInput("hello"); got != "hello\r" {
		t.Fatalf("interactiveInitialInput = %q, want %q", got, "hello\r")
	}
	if got := interactiveInitialInput("hello\r"); got != "hello\r" {
		t.Fatalf("interactiveInitialInput preserved return = %q", got)
	}
	if got := interactiveInitialInput(" \t"); got != "" {
		t.Fatalf("blank interactiveInitialInput = %q, want empty", got)
	}
}
