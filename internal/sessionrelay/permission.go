package sessionrelay

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/inhere/gofer/internal/jobstore"
)

// Terminal permission prompts on the web (Claude Code's PermissionRequest hook).
//
// The hook reports every prompt (the session goes needs_attention with a readable
// "需要授权：Bash `…`" message). When the relay rules say the person is away
// (WaitReason, the same verdict a Stop keys on) it also opens a permission
// decision and long-polls it like a relay turn; the person answers on the web with
// allow / allow-always (one of Claude Code's permission_suggestions) / deny, and the
// hook turns that into its decision JSON. The terminal dialog is shown at the same
// time (Claude Code runs the hook alongside it), so whichever side answers first
// wins; a prompt settled in the terminal is released here (ResolvePermissions).

// EventPermissionRequest is Claude Code's permission-prompt hook event.
const EventPermissionRequest = "PermissionRequest"

// Release tags of a permission decision closed without a web answer.
const (
	// ReleaseByTerminal: the prompt was settled in the terminal (the tool ran, the
	// person typed a new prompt, the turn stopped or was interrupted).
	ReleaseByTerminal = "terminal"
	// ReleaseBySuperseded: a newer prompt of the same session replaced it.
	ReleaseBySuperseded = "superseded"
)

// AuditPermissionAnswered records who answered a permission prompt from the web.
const AuditPermissionAnswered = "session.permission_answered"

// Permission answers stored on the decision (and accepted by AnswerPermission).
const (
	PermissionAllow        = "allow"
	PermissionDeny         = "deny"
	PermissionAlwaysPrefix = "always:"
	permissionDenyPrefix   = "deny:"
)

// Caps on what a hook may store (it already redacts and truncates).
const (
	maxPermissionSummary     = 512
	maxPermissionInput       = 8 * 1024
	maxPermissionSuggestions = 4
	maxPermissionLabel       = 200
	maxPermissionDenyMessage = 1000
)

// PermissionSuggestion is one "always allow" choice Claude Code offered
// (permission_suggestions[i]); only its human label travels — the hook keeps the
// raw entry and returns it as updatedPermissions when the person picks it.
type PermissionSuggestion struct {
	Label string `json:"label"`
}

// PermissionDetail is the decision's detail JSON of a permission prompt.
type PermissionDetail struct {
	ToolName    string                 `json:"tool_name"`
	Summary     string                 `json:"summary"`
	Input       string                 `json:"input,omitempty"`
	Suggestions []PermissionSuggestion `json:"suggestions,omitempty"`
	// Fingerprint identifies the tool call (tool name + canonical input) so the
	// PostToolUse of THAT call — not of a parallel one — settles the prompt.
	Fingerprint string `json:"fp,omitempty"`
}

// PermissionInput opens a permission decision.
type PermissionInput struct {
	PermissionDetail
	TimeoutSec int64
}

// ParsePermissionDetail reads a permission decision's detail JSON (zero value on
// a malformed blob).
func ParsePermissionDetail(raw string) PermissionDetail {
	var d PermissionDetail
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &d)
	}
	return d
}

// PermissionAnswer is a parsed answer.
type PermissionAnswer struct {
	Behavior string // allow | deny
	// Suggestion is the permission_suggestions index of an allow-always, else -1.
	Suggestion int
	Message    string // deny reason (optional)
}

// ParsePermissionAnswer validates an answer against a prompt that offered
// suggestions "always allow" choices: allow | always:<i> | deny | deny:<reason>.
func ParsePermissionAnswer(answer string, suggestions int) (PermissionAnswer, error) {
	a := strings.TrimSpace(answer)
	switch {
	case a == PermissionAllow:
		return PermissionAnswer{Behavior: PermissionAllow, Suggestion: -1}, nil
	case a == PermissionDeny:
		return PermissionAnswer{Behavior: PermissionDeny, Suggestion: -1}, nil
	case strings.HasPrefix(a, permissionDenyPrefix):
		msg := strings.TrimSpace(strings.TrimPrefix(a, permissionDenyPrefix))
		if len(msg) > maxPermissionDenyMessage {
			msg = msg[:maxPermissionDenyMessage]
		}
		return PermissionAnswer{Behavior: PermissionDeny, Suggestion: -1, Message: msg}, nil
	case strings.HasPrefix(a, PermissionAlwaysPrefix):
		i, err := strconv.Atoi(strings.TrimPrefix(a, PermissionAlwaysPrefix))
		if err != nil || i < 0 || i >= suggestions {
			return PermissionAnswer{}, fmt.Errorf("%w: %q is not one of the %d offered suggestions", ErrInvalidInput, a, suggestions)
		}
		return PermissionAnswer{Behavior: PermissionAllow, Suggestion: i}, nil
	}
	return PermissionAnswer{}, fmt.Errorf("%w: permission answer must be allow | always:<i> | deny[:<reason>], got %q", ErrInvalidInput, a)
}

// String renders the canonical stored form.
func (a PermissionAnswer) String() string {
	switch {
	case a.Behavior == PermissionAllow && a.Suggestion >= 0:
		return PermissionAlwaysPrefix + strconv.Itoa(a.Suggestion)
	case a.Behavior == PermissionDeny && a.Message != "":
		return permissionDenyPrefix + a.Message
	}
	return a.Behavior
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	// Cut on a rune boundary.
	for n > 0 && n < len(s) && (s[n]&0xC0) == 0x80 {
		n--
	}
	return s[:n] + "…"
}

// OpenPermission posts a permission prompt the hook will wait on. Like OpenTurn
// the session must currently wait for the web (WaitReason) — else ErrRelayOff and
// the hook leaves the prompt to the terminal. An older still-open prompt of the
// session is released first (only one dialog is on screen at a time).
func (s *Service) OpenPermission(sid string, in PermissionInput) (jobstore.PlanDecision, error) {
	in.ToolName = clip(in.ToolName, 200)
	in.Summary = clip(in.Summary, maxPermissionSummary)
	if in.ToolName == "" {
		return jobstore.PlanDecision{}, fmt.Errorf("%w: tool_name required", ErrInvalidInput)
	}
	if in.Summary == "" {
		in.Summary = in.ToolName
	}
	in.Input = clip(in.Input, maxPermissionInput)
	if len(in.Suggestions) > maxPermissionSuggestions {
		in.Suggestions = in.Suggestions[:maxPermissionSuggestions]
	}
	for i := range in.Suggestions {
		in.Suggestions[i].Label = clip(in.Suggestions[i].Label, maxPermissionLabel)
	}
	in.Fingerprint = clip(in.Fingerprint, 128)
	a, ok, err := s.store.GetAgentSession(sid)
	if err != nil {
		return jobstore.PlanDecision{}, err
	}
	if !ok {
		return jobstore.PlanDecision{}, ErrUnknownSession
	}
	if a.State == jobstore.SessionHandedOff {
		return jobstore.PlanDecision{}, fmt.Errorf("%w: the session was taken over by job %s", ErrRelayOff, a.HandedOffJobID)
	}
	if s.WaitReason(a) == "" {
		return jobstore.PlanDecision{}, ErrRelayOff
	}
	if _, err := s.releasePermissions(sid, "", ReleaseBySuperseded); err != nil {
		return jobstore.PlanDecision{}, err
	}
	detail, err := json.Marshal(in.PermissionDetail)
	if err != nil {
		return jobstore.PlanDecision{}, err
	}
	d := jobstore.PlanDecision{
		Title:      sessionLabel(a) + " · 需要授权",
		Question:   PermissionMessage(in.Summary),
		TimeoutSec: in.TimeoutSec,
		SessionID:  sid,
		Kind:       jobstore.DecisionKindPermission,
		Detail:     string(detail),
	}
	if err := s.store.InsertDecision(&d); err != nil {
		return jobstore.PlanDecision{}, err
	}
	if a.State != jobstore.SessionNeedsAttention {
		_, _ = s.store.SetSessionState(sid, jobstore.SessionNeedsAttention)
	}
	return d, nil
}

// PermissionMessage is the session's last message while a prompt is pending.
func PermissionMessage(summary string) string { return "需要授权：" + summary }

// AnswerPermission records the person's web answer to a pending prompt. The entry
// layer has already checked that the caller is the session's owner; here the
// prompt must belong to sid, be OPEN, and the answer must be one it offered.
func (s *Service) AnswerPermission(sid, id, answer, by string) (jobstore.PlanDecision, error) {
	d, ok, err := s.store.GetDecision(id)
	if err != nil {
		return jobstore.PlanDecision{}, err
	}
	if !ok || d.SessionID != sid || d.Kind != jobstore.DecisionKindPermission {
		return jobstore.PlanDecision{}, ErrUnknownTurn
	}
	if d.State != jobstore.DecisionOpen {
		return jobstore.PlanDecision{}, ErrNoOpenTurn
	}
	detail := ParsePermissionDetail(d.Detail)
	parsed, err := ParsePermissionAnswer(answer, len(detail.Suggestions))
	if err != nil {
		return jobstore.PlanDecision{}, err
	}
	ok, err = s.store.AnswerDecision(id, parsed.String(), by)
	if err != nil {
		return jobstore.PlanDecision{}, err
	}
	if !ok {
		return jobstore.PlanDecision{}, ErrNoOpenTurn
	}
	audit, _ := json.Marshal(map[string]string{
		"decision_id": id, "tool_name": detail.ToolName, "summary": detail.Summary, "answer": parsed.String(),
	})
	_, _ = s.store.AppendAuditEvent(AuditPermissionAnswered, sid, by, string(audit))
	if a, ok, _ := s.store.GetAgentSession(sid); ok && a.State == jobstore.SessionNeedsAttention {
		_, _ = s.store.SetSessionState(sid, jobstore.SessionRunning)
	}
	d, _, err = s.store.GetDecision(id)
	return d, err
}

// ResolvePermissions closes the session's pending prompts that were settled in the
// terminal: fp "" = all of them, else only the prompt of that tool call. It
// returns how many it closed; the session leaves needs_attention when it did.
func (s *Service) ResolvePermissions(sid, fp string) (int, error) {
	if _, ok, err := s.store.GetAgentSession(sid); err != nil {
		return 0, err
	} else if !ok {
		return 0, ErrUnknownSession
	}
	n, err := s.releasePermissions(sid, strings.TrimSpace(fp), ReleaseByTerminal)
	if err != nil || n == 0 {
		return n, err
	}
	if a, ok, _ := s.store.GetAgentSession(sid); ok && a.State == jobstore.SessionNeedsAttention {
		_, _ = s.store.SetSessionState(sid, jobstore.SessionRunning)
	}
	return n, nil
}

func (s *Service) releasePermissions(sid, fp, reason string) (int, error) {
	open, err := s.store.ListOpenSessionPermissions(sid, 20)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, d := range open {
		if fp != "" && ParsePermissionDetail(d.Detail).Fingerprint != fp {
			continue
		}
		released, err := s.store.ReleaseDecision(d.ID, reason)
		if err != nil {
			return n, err
		}
		if released {
			n++
		}
	}
	return n, nil
}

// settlesPermissions reports whether a hook event proves the on-screen permission
// prompt is gone: a new prompt, the end of the turn, an interrupt, the end of the
// session.
func settlesPermissions(event string) bool {
	switch event {
	case EventUserPromptSubmit, EventStop, EventInterrupt, EventSessionEnd, EventSessionStart:
		return true
	}
	return false
}
