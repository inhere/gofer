package stagenote

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/msgprefix"
)

func active() Brief {
	return Brief{WorkItemID: "wi-1", Step: "doing", StepNo: 4, StepTotal: 6, Status: "active"}
}

func TestLineSaysTheStepAndTheThreeCommands(t *testing.T) {
	got := Line(i18n.ZhCN, active())
	for _, want := range []string{"wi-1", "4/6", "「doing」", "gofer stage done", "gofer stage back --reason", "gofer stage blocked --reason"} {
		if !strings.Contains(got, want) {
			t.Errorf("zh line lacks %q: %s", want, got)
		}
	}
	if kind, person := msgprefix.Classify(got); kind != msgprefix.Stage || person {
		t.Errorf("zh line is classified (%q, %v), want a stage system line", kind, person)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("the reminder must be one line: %q", got)
	}
	en := Line(i18n.EN, active())
	if !strings.HasPrefix(en, "[gofer · stage] ") || !strings.Contains(en, `step 4/6 "doing"`) {
		t.Errorf("en line: %s", en)
	}
}

func TestBlockedLineCarriesTheReason(t *testing.T) {
	b := active()
	b.Status, b.Reason = "blocked", "waiting for\nthe review"
	got := Line(i18n.ZhCN, b)
	if !strings.Contains(got, "已标为阻塞（waiting for the review）") || strings.Contains(got, "\n") {
		t.Errorf("blocked line: %q", got)
	}
}

func TestNoFlowNoLine(t *testing.T) {
	if got := Line(i18n.ZhCN, Brief{Enabled: true}); got != "" {
		t.Errorf("a session with no flow got a line: %q", got)
	}
}

func TestSessionStartLine(t *testing.T) {
	if got := SessionStartLine(i18n.ZhCN, nil); got != "" {
		t.Errorf("no brief: %q", got)
	}
	if got := SessionStartLine(i18n.ZhCN, &Brief{}); got != "" {
		t.Errorf("flow off, no flow: %q", got)
	}
	if got := SessionStartLine(i18n.ZhCN, &Brief{Enabled: true}); !strings.Contains(got, "gofer stage start <issue>") {
		t.Errorf("start hint: %q", got)
	}
	b := active()
	if got := SessionStartLine(i18n.ZhCN, &b); got != Line(i18n.ZhCN, b) {
		t.Errorf("in a flow SessionStart gives the current step: %q", got)
	}
}

func TestFitKeepsTheObjectWithinTheBudget(t *testing.T) {
	b := active()
	b.Status, b.Reason = "blocked", strings.Repeat("阻塞原因", 200)
	got := b.Fit()
	raw, _ := json.Marshal(got)
	if len(raw) > MaxBytes {
		t.Fatalf("brief is %d bytes, budget %d", len(raw), MaxBytes)
	}
	if got.Reason == "" || !strings.HasSuffix(got.Reason, "…") {
		t.Errorf("reason should be cut, not dropped: %q", got.Reason)
	}
	if !json.Valid(raw) || !strings.Contains(string(raw), `"step":"doing"`) {
		t.Errorf("fields other than the reason must survive: %s", raw)
	}
}
