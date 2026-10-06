package config

import (
	"testing"
	"time"
)

func TestWorkConfigDefaultsAndOverrides(t *testing.T) {
	var w WorkConfig
	if w.SummarizerAgentName() != "claude" || !w.SummarizeOn() || !w.AutoHandoffOn() {
		t.Fatalf("defaults wrong: %+v", w)
	}
	if got := w.SummarizerArgsOrDefault(); len(got) != 5 || got[0] != "--model" || got[1] != "haiku" || got[2] != "--tools" || got[3] != "" || got[4] != "--no-session-persistence" {
		t.Fatalf("claude default args = %q", got)
	}
	if w.SummarizeIdle() != 15*time.Minute || w.SummarizeMinInterval() != 30*time.Minute || w.SummarizeDaily() != 50 || w.RequestTimeout() != 30*time.Minute {
		t.Fatalf("default thresholds wrong: %v %v %d %v", w.SummarizeIdle(), w.SummarizeMinInterval(), w.SummarizeDaily(), w.RequestTimeout())
	}

	off := false
	w = WorkConfig{SummarizerAgent: "codex", SummarizeEnabled: &off, AutoHandoff: &off, SummarizeIdleMin: 5, SummarizeMinIntervalMin: 10,
		SummarizeDailyLimit: -1, RequestTimeoutMin: 7}
	if w.SummarizerAgentName() != "codex" || w.SummarizeOn() || w.AutoHandoffOn() || w.SummarizeIdle() != 5*time.Minute ||
		w.SummarizeMinInterval() != 10*time.Minute || w.SummarizeDaily() != 0 || w.RequestTimeout() != 7*time.Minute {
		t.Fatalf("overrides wrong: %+v", w)
	}
	// Another agent gets no preset args (the operator picks its model); explicit args win.
	if got := w.SummarizerArgsOrDefault(); got != nil {
		t.Fatalf("non-claude default args = %q, want none", got)
	}
	w.SummarizerArgs = []string{"--fast"}
	if got := w.SummarizerArgsOrDefault(); len(got) != 1 || got[0] != "--fast" {
		t.Fatalf("explicit args = %q", got)
	}
	// The returned slice is a copy.
	got := w.SummarizerArgsOrDefault()
	got[0] = "x"
	if w.SummarizerArgs[0] != "--fast" {
		t.Fatal("SummarizerArgsOrDefault must return a copy")
	}
}

func TestWorkConfigValidateAndClone(t *testing.T) {
	if err := (WorkConfig{SummarizeIdleMin: -1}).validate(); err == nil {
		t.Fatal("a negative threshold must be rejected")
	}
	if err := (WorkConfig{SummarizeDailyLimit: -1}).validate(); err != nil {
		t.Fatalf("a negative daily limit means unlimited: %v", err)
	}
	if err := (WorkConfig{SummarizerAgent: " claude"}).validate(); err == nil {
		t.Fatal("surrounding whitespace in the agent name must be rejected")
	}

	on := true
	c := &Config{Work: WorkConfig{SummarizeEnabled: &on, AutoHandoff: &on, SummarizerArgs: []string{"a"}}}
	cl := c.Clone()
	*cl.Work.SummarizeEnabled = false
	cl.Work.SummarizerArgs[0] = "b"
	if !*c.Work.SummarizeEnabled || c.Work.SummarizerArgs[0] != "a" {
		t.Fatal("Clone must deep-copy the work block's pointers and slices")
	}
}
