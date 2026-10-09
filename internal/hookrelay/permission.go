package hookrelay

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/secret"
)

// Claude Code's PermissionRequest hook (terminal permission prompts on the web).
//
// Verified against Claude Code 2.1.295 (docs/runbook/session-relay.md §10):
// the terminal dialog is shown WHILE this hook runs; an allow/deny printed later
// closes it ("Allowed by PermissionRequest hook"), and a hook that prints nothing,
// exits or times out leaves the dialog as it was. A "Yes" typed in the terminal
// does NOT stop the hook — its later output is ignored — so the hook must notice
// that by itself (the PostToolUse of the same call, a new prompt, the turn's Stop:
// the server releases the decision and the poll below sees it). A "No" / Esc ends
// the turn ("Interrupted") without any hook event (no PostToolUse(Failure), no
// Stop, no Notification) but SIGTERMs the still-running hook ~50ms later and
// SIGKILLs it ~1s after that: the command's signal watcher runs
// ReportPermissionAbort, which closes the card right away.

// EventPermissionRequest is the hook event name.
const EventPermissionRequest = "PermissionRequest"

// permissionMessagePrefix opens the session's last message while a prompt is pending.
const permissionMessagePrefix = "需要授权："

// Display caps: the summary is one line on a card, the input a collapsible block.
const (
	permissionSummaryRunes = 200
	permissionInputBytes   = 4 * 1024
	permissionLabelRunes   = 160
	permissionMaxSugs      = 4
)

// defaultDenyMessage is what the agent reads when the person denied without a reason.
const defaultDenyMessage = "用户在 gofer web 上拒绝了这次操作"

// PermissionDecision is the hook's decision (hookSpecificOutput.decision).
type PermissionDecision struct {
	Behavior           string            `json:"behavior"`
	UpdatedPermissions []json.RawMessage `json:"updatedPermissions,omitempty"`
	Message            string            `json:"message,omitempty"`
}

// PermissionJSON renders the PermissionRequest hook output for d.
func PermissionJSON(d PermissionDecision) []byte {
	b, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": EventPermissionRequest, "decision": d,
	}})
	return b
}

// bearerPattern covers `Authorization: Bearer <token>`, which the shared
// key=value rules only half-redact (they stop at the scheme word).
var bearerPattern = regexp.MustCompile(`(?i)\b(bearer|basic|token)\s+[A-Za-z0-9._~+/=\-]{8,}`)

// secretKeyPattern names a JSON key whose value is a credential as a whole.
var secretKeyPattern = regexp.MustCompile(`(?i)(secret|token|password|passwd|api[_\-]?key|access[_\-]?key|private[_\-]?key|auth|bearer|credential)`)

// redactText scrubs credential-looking parts of free text (a shell command, a URL).
func redactText(s string) string {
	s = bearerPattern.ReplaceAllString(s, "$1 "+secret.Placeholder)
	out, _ := secret.RedactString(s)
	return out
}

// redactValue walks decoded JSON: a value under a secret-named key is replaced
// whole, every other string is scrubbed as text.
func redactValue(key string, v any) any {
	if key != "" && secretKeyPattern.MatchString(key) {
		if _, isObj := v.(map[string]any); !isObj {
			return secret.Placeholder
		}
	}
	switch t := v.(type) {
	case string:
		return redactText(t)
	case map[string]any:
		for k, x := range t {
			t[k] = redactValue(k, x)
		}
		return t
	case []any:
		for i, x := range t {
			t[i] = redactValue("", x)
		}
		return t
	}
	return v
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

func oneLineText(s string) string { return strings.Join(strings.Fields(s), " ") }

// decodeInput reads tool_input as a JSON object (nil when it is not one).
func decodeInput(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil {
		return nil
	}
	return m
}

func strField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// PermissionView renders what a person needs to judge a prompt: a one-line
// summary ("Bash `rm -rf node_modules`") and the redacted, truncated input.
func PermissionView(tool string, input json.RawMessage) (summary, detail string) {
	tool = strings.TrimSpace(tool)
	if tool == "" {
		tool = "tool"
	}
	m := decodeInput(input)
	var what string
	switch {
	case m == nil:
	case strField(m, "command", "cmd") != "":
		what = "`" + redactText(oneLineText(strField(m, "command", "cmd"))) + "`"
	case strField(m, "file_path", "notebook_path", "path") != "":
		what = strField(m, "file_path", "notebook_path", "path")
	case strField(m, "url") != "":
		what = redactText(strField(m, "url"))
	case strField(m, "query", "pattern", "prompt", "description") != "":
		what = redactText(oneLineText(strField(m, "query", "pattern", "prompt", "description")))
	}
	if m != nil {
		redacted := redactValue("", m)
		if b, err := marshalPlain(redacted, "  "); err == nil {
			detail = b
			if what == "" {
				what, _ = marshalPlain(redacted, "")
			}
		}
	} else if len(bytes.TrimSpace(input)) > 0 {
		detail = redactText(string(input))
	}
	if len(detail) > permissionInputBytes {
		cut := permissionInputBytes
		for cut > 0 && !utf8.RuneStart(detail[cut]) {
			cut--
		}
		detail = detail[:cut] + "\n…（已截断）"
	}
	summary = tool
	if what != "" {
		summary = tool + " " + what
	}
	return truncateRunes(summary, permissionSummaryRunes), detail
}

// marshalPlain is json.Marshal(Indent) without HTML escaping: a shell command's
// `&&` must read as `&&`, not `\u0026\u0026`.
func marshalPlain(v any, indent string) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", indent)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimRight(buf.String(), "\n"), nil
}

// PermissionFingerprint identifies one tool call: the tool name plus its input in
// canonical JSON (object keys sorted), so the PostToolUse of the same call maps to
// the same value whatever the key order.
func PermissionFingerprint(tool string, input json.RawMessage) string {
	canon := bytes.TrimSpace(input)
	if len(canon) > 0 {
		dec := json.NewDecoder(bytes.NewReader(canon))
		dec.UseNumber()
		var v any
		if dec.Decode(&v) == nil {
			if b, err := json.Marshal(v); err == nil {
				canon = b
			}
		}
	}
	h := sha1.New()
	h.Write([]byte(strings.TrimSpace(tool)))
	h.Write([]byte{'\n'})
	h.Write(canon)
	return hex.EncodeToString(h.Sum(nil))
}

// destinationLabel names where an "always allow" rule would be stored.
func destinationLabel(dest string) string {
	switch dest {
	case "session":
		return "本会话"
	case "localSettings":
		return "本项目（本地）"
	case "projectSettings":
		return "本项目"
	case "userSettings":
		return "所有项目"
	}
	return dest
}

// permissionSuggestion is the subset of a permission_suggestions entry the label needs.
type permissionSuggestion struct {
	Type        string   `json:"type"`
	Behavior    string   `json:"behavior"`
	Mode        string   `json:"mode"`
	Destination string   `json:"destination"`
	Directories []string `json:"directories"`
	Rules       []struct {
		ToolName    string `json:"toolName"`
		RuleContent string `json:"ruleContent"`
	} `json:"rules"`
}

// SuggestionLabel renders one permission_suggestions entry for a button.
func SuggestionLabel(raw json.RawMessage) string {
	var s permissionSuggestion
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	var label string
	switch s.Type {
	case "addRules", "replaceRules":
		parts := make([]string, 0, len(s.Rules))
		for _, r := range s.Rules {
			if r.RuleContent != "" {
				parts = append(parts, r.ToolName+"("+r.RuleContent+")")
			} else {
				parts = append(parts, r.ToolName)
			}
		}
		label = "规则 " + strings.Join(parts, ", ")
		if s.Behavior != "" && s.Behavior != "allow" {
			label += "（" + s.Behavior + "）"
		}
	case "addDirectories":
		label = "目录 " + strings.Join(s.Directories, ", ")
	case "setMode":
		label = "切换到 " + s.Mode + " 模式"
	default:
		label = s.Type
	}
	if d := destinationLabel(s.Destination); d != "" {
		label += " · " + d
	}
	return truncateRunes(redactText(oneLineText(label)), permissionLabelRunes)
}

// parseSuggestions splits permission_suggestions into raw entries (what the hook
// returns as updatedPermissions) and their labels (what the web shows).
func parseSuggestions(raw json.RawMessage) ([]json.RawMessage, []client.PermissionSuggestionLabel) {
	var list []json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &list) != nil {
		return nil, nil
	}
	if len(list) > permissionMaxSugs {
		list = list[:permissionMaxSugs]
	}
	labels := make([]client.PermissionSuggestionLabel, 0, len(list))
	for _, r := range list {
		labels = append(labels, client.PermissionSuggestionLabel{Label: SuggestionLabel(r)})
	}
	return list, labels
}

// decisionFromAnswer turns the stored web answer into the hook decision. ok=false
// for an answer the hook cannot honour (the terminal dialog then stays).
func decisionFromAnswer(answer string, sugs []json.RawMessage) (PermissionDecision, bool) {
	a := strings.TrimSpace(answer)
	switch {
	case a == "allow":
		return PermissionDecision{Behavior: "allow"}, true
	case a == "deny":
		return PermissionDecision{Behavior: "deny", Message: defaultDenyMessage}, true
	case strings.HasPrefix(a, "deny:"):
		msg := strings.TrimSpace(strings.TrimPrefix(a, "deny:"))
		if msg == "" {
			msg = defaultDenyMessage
		} else {
			msg = "用户在 gofer web 上拒绝了这次操作：" + msg
		}
		return PermissionDecision{Behavior: "deny", Message: msg}, true
	case strings.HasPrefix(a, "always:"):
		i, err := strconv.Atoi(strings.TrimPrefix(a, "always:"))
		if err != nil || i < 0 || i >= len(sugs) {
			return PermissionDecision{}, false
		}
		return PermissionDecision{Behavior: "allow", UpdatedPermissions: []json.RawMessage{sugs[i]}}, true
	}
	return PermissionDecision{}, false
}

// permissionMarker is the local "a prompt of this call is pending on the web"
// file: the PostToolUse hook of the same call checks it (no network otherwise).
func permissionMarker(dir, sid, fp string) string {
	if strings.TrimSpace(dir) == "" {
		dir = filepath.Join(os.TempDir(), "gofer-permission-pending")
	}
	h := sha1.Sum([]byte(sid))
	return filepath.Join(dir, fmt.Sprintf("%x-%s", h[:8], fp))
}

// PermissionPending reports whether a PostToolUse belongs to a call whose prompt
// is still pending on the web (the cheap pre-check the command uses to skip the
// hub entirely for an ordinary non-shell tool).
func PermissionPending(p Payload, dir string) bool {
	if p.ToolName == "" || len(p.ToolInput) == 0 {
		return false
	}
	_, err := os.Stat(permissionMarker(dir, p.SessionID, PermissionFingerprint(p.ToolName, p.ToolInput)))
	return err == nil
}

// resolvePendingPermission is the PostToolUse side: the call ran, so its prompt
// (if one is still pending on the web) was settled in the terminal.
func (r *runner) resolvePendingPermission() {
	if !PermissionPending(r.p, r.opts.PermissionStateDir) {
		return
	}
	fp := PermissionFingerprint(r.p.ToolName, r.p.ToolInput)
	_ = os.Remove(permissionMarker(r.opts.PermissionStateDir, r.p.SessionID, fp))
	if n, err := r.api.ResolveSessionPermission(r.p.SessionID, fp); err != nil {
		r.log("resolve permission failed: %v", err)
	} else {
		r.log("permission settled in the terminal (%d closed)", n)
	}
}

// ReportPermissionAbort is what the PermissionRequest hook does when the agent CLI
// signals it: the person answered No / Esc in the terminal (the earliest signal
// there is, ~50ms after the key press) or the hook outlived its timeout. Either
// way the prompt is no longer waited on: close the web card (released_by=terminal)
// FIRST — Claude Code SIGKILLs the hook ~1s later — then log which case it was.
func ReportPermissionAbort(api API, p Payload, opts Options) {
	fp := PermissionFingerprint(p.ToolName, p.ToolInput)
	_ = os.Remove(permissionMarker(opts.PermissionStateDir, p.SessionID, fp))
	n, err := api.ResolveSessionPermission(p.SessionID, fp)
	if opts.Log == nil {
		return
	}
	why := "hook stopped by the agent CLI (timeout / interrupt)"
	if waitTerminalDenied(p, permissionDenialWait) {
		why = "denied in the terminal"
	}
	result := fmt.Sprintf("card released (%d closed)", n)
	if err != nil {
		result = fmt.Sprintf("release card failed: %v", err)
	}
	fmt.Fprintf(opts.Log, "%s %s %s %s permission %s: %s\n",
		opts.withDefaults().now().Format(time.RFC3339), p.Agent, shortID(p.SessionID), p.Event, why, result)
}

// permissionDenialWait bounds how long the dying hook looks for the rejected
// tool_result: Claude Code stamps it ~10ms after the key press but flushes the
// transcript a little later, and SIGKILLs the hook ~1s after the SIGTERM.
var permissionDenialWait = 600 * time.Millisecond

// waitTerminalDenied polls TerminalDenied for up to wait (log classification only;
// the card is already closed by then).
func waitTerminalDenied(p Payload, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		if TerminalDenied(p.TranscriptPath, p.ToolName, p.ToolInput) {
			return true
		}
		if strings.TrimSpace(p.TranscriptPath) == "" || time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// permissionRequest reports the prompt and, while the relay rules say the person
// is away, waits for their web answer. No answer = no output: the terminal dialog
// that is already on screen simply stays.
func (r *runner) permissionRequest() Result {
	summary, detail := PermissionView(r.p.ToolName, r.p.ToolInput)
	msg := permissionMessagePrefix + summary
	if ObserveOnly(r.p.dialect()) {
		r.beatAndLog(client.SessionHeartbeat{Event: r.p.Event, LastMessage: msg})
		return Result{}
	}
	a, ok := r.heartbeat(client.SessionHeartbeat{Event: r.p.Event, LastMessage: msg, IdleSec: idleSecPtr()})
	if !ok {
		return Result{}
	}
	if a.Notice != "" {
		r.log("session handed off, leaving the prompt to the terminal")
		return Result{}
	}
	// The permission verdict skips the supervising gate (the dialog blocks the
	// terminal anyway); a server without it only sends the Stop verdict.
	reason, budgetSec := a.PermissionWaitReason, a.PermissionWaitBudgetSec
	if reason == "" {
		reason, budgetSec = a.WaitReason, a.WaitBudgetSec
	}
	if reason == "" {
		r.log("prompt %q reported; not waiting (person at the keyboard: relay=%s %s)", head(summary, 60), a.RelayMode, a.WaitReasonDetail)
		return Result{}
	}
	wait := r.opts.Wait
	if budgetSec > 0 {
		if budget := time.Duration(budgetSec) * time.Second; budget < wait {
			wait = budget
		}
	}
	sugs, labels := parseSuggestions(r.p.PermissionSuggestions)
	fp := PermissionFingerprint(r.p.ToolName, r.p.ToolInput)
	d, err := r.api.OpenSessionPermission(r.p.SessionID, client.SessionPermission{
		ToolName: r.p.ToolName, Summary: summary, Input: detail, Suggestions: labels,
		Fingerprint: fp, TimeoutSec: int64(wait / time.Second),
	})
	if err != nil {
		r.log("open permission failed: %v", err) // 409 = relay flipped off in between
		return Result{}
	}
	marker := permissionMarker(r.opts.PermissionStateDir, r.p.SessionID, fp)
	if err := os.MkdirAll(filepath.Dir(marker), 0o700); err == nil {
		_ = os.WriteFile(marker, []byte(d.ID+"\n"), 0o600)
	}
	defer os.Remove(marker)
	probeArmed := reason == client.WaitIdleProbe
	pollSec := r.opts.PollSec
	if probeArmed && pollSec > autoArmPollSec {
		pollSec = autoArmPollSec
	}
	r.log("permission %s open (reason=%s, %s), waiting up to %s", d.ID, reason, head(summary, 60), wait)
	// leave is every exit that hands the prompt back to the terminal while the card
	// may still be OPEN on the web: nobody consumes a web answer any more, so the card
	// is closed (released) instead of staying answerable into a void.
	leave := func() Result {
		r.releaseOpenPermission(fp)
		return Result{}
	}
	deadline := r.opts.now().Add(wait)
	failures := 0
	for {
		remaining := deadline.Sub(r.opts.now())
		if remaining <= 0 {
			break
		}
		step := pollSec
		if rs := int(remaining / time.Second); rs < step {
			step = rs
		}
		if step < 1 {
			step = 1
		}
		st, err := r.api.WaitSessionTurn(r.p.SessionID, d.ID, step)
		if err != nil {
			failures++
			r.log("permission wait failed (%d/%d): %v", failures, transientRetries, err)
			if failures >= transientRetries || client.StatusOf(err) == 404 {
				return leave()
			}
			r.opts.sleep(transientBackoff)
			continue
		}
		failures = 0
		switch st.Outcome {
		case "answered":
			dec, ok := decisionFromAnswer(st.Decision.Answer, sugs)
			if !ok {
				r.log("permission answer %q not usable, leaving it to the terminal", st.Decision.Answer)
				return leave()
			}
			r.log("permission answered on the web: %s", st.Decision.Answer)
			return Result{Permission: &dec}
		case "expired", "relay_off":
			// already settled server-side
			r.log("permission %s (%s), leaving it to the terminal", st.Outcome, st.Decision.ReleasedBy)
			return Result{}
		}
		if probeArmed && r.releasedOnUserReturn(d.ID) {
			return Result{} // released server-side
		}
	}
	r.log("permission wait budget exhausted, leaving it to the terminal")
	return leave()
}

// permissionResolveTimeout bounds the best-effort close of a card on the way out:
// the terminal dialog must not wait on an unreachable hub.
var permissionResolveTimeout = 3 * time.Second

// releaseOpenPermission closes this call's web card (best effort, short timeout)
// when the hook stops waiting on it without a usable web answer.
func (r *runner) releaseOpenPermission(fp string) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		if n, err := r.api.ResolveSessionPermission(r.p.SessionID, fp); err != nil {
			r.log("release permission card failed: %v", err)
		} else {
			r.log("permission card released (%d closed)", n)
		}
	}()
	select {
	case <-done:
	case <-time.After(permissionResolveTimeout):
		r.log("release permission card timed out")
	}
}

// SkipPostToolUse reports a Claude PostToolUse the hook has nothing to do for: the
// template matches every tool (so the call that answered a permission prompt can
// settle it), but only a shell tool carries progress / job watches, and any other
// tool matters only when its prompt is still pending on the web. Checked before
// the hub client is even built, so an ordinary Read / Edit costs one stat.
func SkipPostToolUse(p Payload, dir string) bool {
	return p.Event == "PostToolUse" && p.dialect() == AgentClaude &&
		!isShellTool(p.ToolName) && !PermissionPending(p, dir)
}
