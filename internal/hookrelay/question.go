package hookrelay

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/client"
)

// Claude Code's AskUserQuestion relayed to the web (gofer-02fu). The PreToolUse hook
// fires before the terminal shows the choice dialog. When the relay rules say the
// person is away (the same verdict a permission prompt keys on) the questions become
// a web card; the web answer is handed back as the tool's own input
// (updatedInput.answers), so Claude Code records "User answered Claude's questions"
// and never shows the dialog. When the person is at the keyboard — or nothing usable
// comes back — the hook prints nothing and the terminal asks as usual.

// AskUserQuestionTool is Claude Code's question tool.
const AskUserQuestionTool = "AskUserQuestion"

// questionMessagePrefix leads the session's last message while a question waits.
const questionMessagePrefix = "等你回答："

// PreToolDecision is a PreToolUse verdict that lets the call run with a rewritten
// input (the answered questions).
type PreToolDecision struct {
	UpdatedInput json.RawMessage
	Reason       string
}

// PreToolJSON renders the PreToolUse hook output for d.
func PreToolJSON(d PreToolDecision) []byte {
	b, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName":            "PreToolUse",
		"permissionDecision":       "allow",
		"permissionDecisionReason": d.Reason,
		"updatedInput":             d.UpdatedInput,
	}})
	return b
}

// askUserQuestion reports the question and, while the person is away, waits for the
// web answer.
func (r *runner) askUserQuestion() Result {
	if r.api == nil {
		return Result{} // no hub (the command-memory fast path): the terminal asks
	}
	var in struct {
		Questions []client.SessionQuestion `json:"questions"`
	}
	if err := json.Unmarshal(r.p.ToolInput, &in); err != nil || len(in.Questions) == 0 {
		r.log("AskUserQuestion input not understood, leaving it to the terminal")
		return Result{}
	}
	msg := questionMessagePrefix + head(in.Questions[0].Question, 200)
	a, ok := r.heartbeat(client.SessionHeartbeat{Event: EventPermissionRequest, LastMessage: msg, IdleSec: idleSecPtr()})
	if !ok {
		return Result{}
	}
	if a.Notice != "" {
		r.log("session handed off, leaving the question to the terminal")
		return Result{}
	}
	reason, budgetSec := a.PermissionWaitReason, a.PermissionWaitBudgetSec
	if reason == "" {
		reason, budgetSec = a.WaitReason, a.WaitBudgetSec // as permissionRequest
	}
	if reason == "" {
		r.log("question %q reported; not waiting (person at the keyboard: relay=%s %s)", head(in.Questions[0].Question, 60), a.RelayMode, a.WaitReasonDetail)
		return Result{}
	}
	wait := r.opts.Wait
	if budgetSec > 0 {
		if budget := time.Duration(budgetSec) * time.Second; budget < wait {
			wait = budget
		}
	}
	fp := PermissionFingerprint(r.p.ToolName, r.p.ToolInput)
	d, err := r.api.OpenSessionPermission(r.p.SessionID, client.SessionPermission{
		ToolName: AskUserQuestionTool, Summary: msg, Questions: in.Questions,
		Fingerprint: fp, TimeoutSec: int64(wait / time.Second),
	})
	if err != nil {
		r.log("open question failed: %v", err)
		return Result{}
	}
	// The PostToolUse of this call (answered in the terminal after all) settles the card.
	marker := permissionMarker(r.opts.PermissionStateDir, r.p.SessionID, fp)
	if err := os.MkdirAll(filepath.Dir(marker), 0o700); err == nil {
		_ = os.WriteFile(marker, []byte(d.ID+"\n"), 0o600)
	}
	defer os.Remove(marker)
	r.log("question %s open (reason=%s, %s), waiting up to %s", d.ID, reason, head(in.Questions[0].Question, 60), wait)
	answer, ok := r.waitWebAnswer(d.ID, reason, wait, fp, "question")
	if !ok {
		return Result{}
	}
	updated, err := answeredInput(r.p.ToolInput, answer)
	if err != nil {
		r.log("question answer %q not usable (%v), leaving it to the terminal", head(answer, 80), err)
		r.releaseOpenPermission(fp)
		return Result{}
	}
	r.log("question answered on the web")
	return Result{PreTool: &PreToolDecision{UpdatedInput: updated, Reason: "answered on the gofer web"}}
}

// answeredInput is the AskUserQuestion input with the web answers added as `answers`
// (question text → answer), everything else kept as Claude Code sent it.
func answeredInput(input json.RawMessage, stored string) (json.RawMessage, error) {
	raw, ok := strings.CutPrefix(stored, "answers:")
	if !ok {
		return nil, errNotAnswers
	}
	var answers map[string]string
	if err := json.Unmarshal([]byte(raw), &answers); err != nil {
		return nil, err
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(input, &obj); err != nil {
		return nil, err
	}
	b, err := json.Marshal(answers)
	if err != nil {
		return nil, err
	}
	obj["answers"] = b
	return json.Marshal(obj)
}

type answersError string

func (e answersError) Error() string { return string(e) }

const errNotAnswers = answersError("not an answers: reply")
