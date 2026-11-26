// Package stagenote renders the stage-flow reminder a session sees: the one line injected
// before each turn, and the one-sentence start hint at session start. It is a leaf (i18n and
// msgprefix only) so the server (session view, ACP turn note) and the terminal hook share one
// wording and cannot drift apart; what a session is on comes from internal/stage.
package stagenote

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/msgprefix"
)

// MaxBytes bounds the JSON size of a Brief, since it rides every heartbeat answer.
const MaxBytes = 300

// Brief is the `stage` object of a session view: where the flow of the work item the
// session is on stands. A session with no flow in progress carries Enabled only (the project
// has the flow on, so SessionStart suggests starting one); a project with the flow off and no
// flow in progress carries no object at all.
//
// It holds no gate result: evaluating a gate may run git, and the view is built on every
// heartbeat. The agent learns what is missing from `gofer stage done`.
type Brief struct {
	WorkItemID string `json:"work_item_id,omitempty"`
	Step       string `json:"step,omitempty"`
	// StepNo is the 1-based position of Step among StepTotal steps.
	StepNo    int `json:"step_no,omitempty"`
	StepTotal int `json:"step_total,omitempty"`
	// Status is active or blocked.
	Status string `json:"status,omitempty"`
	// Reason is why the flow is blocked (cut to keep the whole object within MaxBytes).
	Reason  string `json:"reason,omitempty"`
	Enabled bool   `json:"enabled,omitempty"`
}

// InFlow reports whether the brief describes a flow in progress.
func (b Brief) InFlow() bool { return b.WorkItemID != "" && b.Step != "" }

// Fit returns b with Reason cut so that its JSON stays within MaxBytes.
func (b Brief) Fit() Brief {
	for {
		raw, err := json.Marshal(b)
		if err != nil || len(raw) <= MaxBytes || b.Reason == "" {
			return b
		}
		over := len(raw) - MaxBytes
		keep := len(b.Reason) - over - len("…")
		if keep <= 0 {
			b.Reason = ""
			continue
		}
		b.Reason = cutBytes(b.Reason, keep) + "…"
	}
}

func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// Line renders the per-turn reminder of a flow in progress: the step, what to run when it is
// done, and the back / blocked commands. "" when b is not in a flow. The text carries the
// stage system prefix, so a session reads it as gofer speaking.
func Line(lang i18n.Lang, b Brief) string {
	if !b.InFlow() {
		return ""
	}
	key := "stage.note.active"
	if b.Status == "blocked" {
		key = "stage.note.blocked"
	}
	reason := strings.Join(strings.Fields(b.Reason), " ")
	if reason == "" {
		reason = "-"
	}
	body := i18n.T(lang, key,
		"work", b.WorkItemID, "no", strconv.Itoa(b.StepNo), "total", strconv.Itoa(b.StepTotal),
		"step", b.Step, "reason", reason)
	return msgprefix.FormatIn(lang, msgprefix.Stage, "", body)
}

// RemindLine is the turn the `remind` level sends a session when the flow enters one of its
// steps: continue with b.Step. The text carries the stage system prefix.
func RemindLine(lang i18n.Lang, b Brief) string {
	body := i18n.T(lang, "stage.remind.continue",
		"work", b.WorkItemID, "no", strconv.Itoa(b.StepNo), "total", strconv.Itoa(b.StepTotal), "step", b.Step)
	return msgprefix.FormatIn(lang, msgprefix.Stage, "", body)
}

// StartLine is the SessionStart hint for a session with no flow in progress.
func StartLine(lang i18n.Lang) string {
	return msgprefix.FormatIn(lang, msgprefix.Stage, "", i18n.T(lang, "stage.note.start"))
}

// SessionStartLine is what SessionStart adds for b: the current step of a flow in progress,
// else the start hint when the project has the flow on, else "".
func SessionStartLine(lang i18n.Lang, b *Brief) string {
	switch {
	case b == nil:
		return ""
	case b.InFlow():
		return Line(lang, *b)
	case b.Enabled:
		return StartLine(lang)
	}
	return ""
}
