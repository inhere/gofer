package sessionrelay

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// The input-area guard of the terminal delivery (design 2026-10-22 D4): before a
// message is typed into a session's tmux pane, the pane's last lines are read with
// `tmux capture-pane -p` and judged. A person may be halfway through typing there,
// or sitting in a dialog (AskUserQuestion, a permission question) where the Enter
// behind the paste would ANSWER FOR THEM — the two accidents this guard exists for.
//
// The judgement is per agent (a TUI's prompt line is its own) and deliberately
// conservative: only a rule that positively recognises an EMPTY input area counts as
// empty, everything unrecognised is GuardUnknown and leaves the message waiting.

// GuardVerdict is what the pane's last lines say about its input area.
const (
	// GuardEmpty: the input area is confirmed empty — the only verdict that lets
	// `auto` type into the pane.
	GuardEmpty = "empty"
	// GuardHasInput: the person has unsent text in the input area.
	GuardHasInput = "has_input"
	// GuardSelecting: a question / permission dialog is up; an Enter would answer it.
	GuardSelecting = "selecting"
	// GuardUnknown: the screen matches nothing the agent's rules know (a half-drawn
	// frame, a new TUI version) — never treated as empty.
	GuardUnknown = "unknown"
)

// Why a delivery was left to wait (the reason recorded in the hold event and, for a
// skip, in the delivery's reason code).
const (
	ReasonHoldMode      = "hold_mode"
	ReasonInputHasInput = "input_has_content"
	ReasonSelecting     = "selecting"
	ReasonGuardUnknown  = "guard_unknown"
)

// PathHeld is DeliverResult.Path for a message that was not typed but queued in the
// session's mailbox: its next UserPromptSubmit / Stop hook injects it.
const PathHeld = "held"

// TagRelayGuard labels the internal job that only captures the pane (the guard probe),
// so it is told apart from the injection jobs in `gofer job ls --tag`.
const TagRelayGuard = "relay-guard"

// Audit kinds of the terminal-delivery guard (D5): a set of the session's delivery mode,
// a message left waiting, and a person sending the waiting letters right away.
const (
	AuditInjectModeSet = "session.inject_mode_set"
	AuditInjectHold    = "session.inject_hold"
	AuditInjectFlush   = "session.inject_flush"
)

// heldMailDeliverBy is how long a held message waits in the mailbox before the mailbox's
// own deadline expires it: long enough for a terminal nobody touches for days, because a
// held message is what the person asked to send, not a draft.
const heldMailDeliverBy = 30 * 24 * time.Hour

// heldFlushLimit / heldFlushChars bound 「现在送」: one paste carries the waiting letters,
// and the same 8KB ceiling as a normal delivery applies to the joined text.
const (
	heldFlushLimit = 10
	heldFlushChars = MaxDeliverText
)

// ErrNothingHeld reports 「现在送」 on a session with no waiting message: nothing to type.
var ErrNothingHeld = errors.New("sessionrelay: no message is waiting for this session")

// InjectGuardRules is one agent's input-area recognition (agent config
// `inject_guard`, an empty list meaning "no rule of this kind"). The host compiles
// the configured regexps; the relay only classifies what it is given.
type InjectGuardRules struct {
	// Empty matches the agent's empty input area (the prompt line, or its placeholder).
	Empty []*regexp.Regexp
	// HasInput matches an input line that already carries text.
	HasInput []*regexp.Regexp
	// Selecting matches a dialog / selection UI where Enter answers something.
	Selecting []*regexp.Regexp
}

// IsZero reports whether no rule of any kind is configured.
func (g InjectGuardRules) IsZero() bool {
	return len(g.Empty) == 0 && len(g.HasInput) == 0 && len(g.Selecting) == 0
}

// DefaultInjectGuard returns the built-in recognition rules of an agent (empty for an
// agent gofer does not know: its sessions then always read GuardUnknown under `auto`).
//
// The samples behind these patterns are the fixtures in inject_guard_test.go. Claude
// Code and Codex both draw the input line as a box / a `›` prompt, and both answer a
// question with a `❯ 1. Yes` / `› 1. Yes` list.
func DefaultInjectGuard(agentKey string) InjectGuardRules {
	switch strings.ToLower(strings.TrimSpace(agentKey)) {
	case "claude":
		return InjectGuardRules{
			Empty: mustRegexps(
				`^│ >\s*│?\s*$`, // the empty box line
				`^│ > Try "`,    // the empty box with its placeholder
				`^>\s*$`,        // the same line without the box border
			),
			HasInput: mustRegexps(
				`^│ > \S`, // text typed into the box
				`^> \S`,   // the same line without the box border
			),
			Selecting: mustRegexps(
				`❯ \d+\.`,  // the numbered answer list
				`^\s*│ +❯`, // a boxed selection cursor
				`Do you want to proceed\?`,
			),
		}
	case "codex":
		return InjectGuardRules{
			Empty: mustRegexps(
				`^›\s*$`,
			),
			HasInput: mustRegexps(
				`^› \S`,
			),
			Selecting: mustRegexps(
				`^› \d+\.`,
			),
		}
	}
	return InjectGuardRules{}
}

// ClassifyInjectGuard judges a pane capture. Selecting wins over an input line (a
// dialog is drawn OVER the box, so a stale box line may still be visible), then a
// positively empty input area, then a non-empty one — the empty rules are written to
// match the placeholder, which would otherwise look like typed text; anything else is
// unknown.
func ClassifyInjectGuard(capture string, rules InjectGuardRules) string {
	lines := guardLines(capture)
	if matchesAny(lines, rules.Selecting) {
		return GuardSelecting
	}
	if matchesAny(lines, rules.Empty) {
		return GuardEmpty
	}
	if matchesAny(lines, rules.HasInput) {
		return GuardHasInput
	}
	return GuardUnknown
}

// guardReason maps a verdict to the reason code recorded for a skipped delivery.
func guardReason(verdict string) string {
	switch verdict {
	case GuardEmpty:
		return ""
	case GuardHasInput:
		return ReasonInputHasInput
	case GuardSelecting:
		return ReasonSelecting
	}
	return ReasonGuardUnknown
}

// guardLines keeps the non-blank lines of a capture: tmux pads the pane with empty
// lines below the frame, and a rule should never see them.
func guardLines(capture string) []string {
	raw := strings.Split(capture, "\n")
	out := make([]string, 0, len(raw))
	for _, l := range raw {
		l = strings.TrimRight(l, " \t\r")
		if strings.TrimSpace(l) == "" {
			continue
		}
		out = append(out, l)
	}
	return out
}

func matchesAny(lines []string, rules []*regexp.Regexp) bool {
	for _, re := range rules {
		if re == nil {
			continue
		}
		for _, l := range lines {
			if re.MatchString(l) {
				return true
			}
		}
	}
	return false
}

func mustRegexps(pats ...string) []*regexp.Regexp {
	return CompileInjectGuard(pats)
}

// CompileInjectGuard compiles the configured patterns of one list, dropping the ones
// that do not compile (config validation rejects those at load; a snapshot that
// bypassed it must not take the delivery path down).
func CompileInjectGuard(pats []string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(pats))
	for _, p := range pats {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		re, err := regexp.Compile(p)
		if err != nil {
			continue
		}
		out = append(out, re)
	}
	return out
}

// guardCaptureScript is the guard probe: print the pane's last guardCaptureLines
// lines. It reports a missing pane with the injection script's own code (3) so the
// caller can tell "no pane" from "unreadable screen".
func guardCaptureScript(pane string) string {
	return "tmux capture-pane -p -t " + shQuote(pane) + " -S -" + itoa(guardCaptureLines) +
		" 2>/dev/null || { echo " + injectPaneMissing + "; exit 3; }\n"
}

// guardCaptureLines is how much of the pane the guard reads: a TUI's input area and
// any dialog above it fit well inside this, and a longer scroll-back would let an old
// prompt line answer for the current screen.
const guardCaptureLines = 30

// probeInjectGuard captures the session's pane and classifies its input area. A pane
// that cannot be read, or a probe job that outlived its wait window, is GuardUnknown:
// the guard never guesses "empty".
func (s *Service) probeInjectGuard(ctx context.Context, a jobstore.AgentSession, origin string) (string, string) {
	if s.injector == nil || strings.TrimSpace(a.Runner) == "" || strings.TrimSpace(a.TmuxPane) == "" {
		return GuardUnknown, injectRunnerError
	}
	res, err := s.injector.InjectSession(ctx, InjectRequest{
		ProjectKey:   a.ProjectKey,
		Runner:       a.Runner,
		Cwd:          ".",
		Title:        "relay guard → " + shortSessionID(a.SessionID),
		Tags:         []string{TagRelayGuard},
		TimeoutSec:   injectTimeoutSec,
		Cmd:          []string{"sh", "-c", guardCaptureScript(a.TmuxPane)},
		UpdateExempt: true,
		OriginCaller: origin,
	})
	if err != nil || res.Status != "" {
		return GuardUnknown, injectRunnerError
	}
	if res.ExitCode != 0 {
		return GuardUnknown, injectPaneMissing
	}
	return ClassifyInjectGuard(res.Output, s.injectGuardFor(a.Agent)), ""
}

// itoa is the tiny int → decimal used by the generated shell scripts.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
