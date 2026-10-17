package job

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/inhere/gofer/internal/runner"
)

// error_kind.go reads the agent's own verdict on a provider failure (suga's error_kind
// contract, see runner.ErrorKindTransient) and decides from it whether a failure is
// transient. It outranks the text-pattern classifier: an agent that labels its errors
// knows better than a regex over its message, and "unknown label" means STOP.

// errorKindInfo is one failure's error_kind evidence.
type errorKindInfo struct {
	Kind    string
	ResetAt string
	Message string
}

// errorKindEvidence finds the agent's classification of a failed job. Two carriers:
//   - the event stream (cli agents): the last `{"type":"error","error_kind":…}` row in the
//     stderr tail, with the optional `reset_at`;
//   - the error text (ACP agents): the marker the ACP runner appends from the prompt
//     error response's `data` (runner.ErrorKindMarker).
//
// The stream wins when both exist: it is the later and more specific source.
func errorKindEvidence(snap JobResult) (errorKindInfo, bool) {
	var found errorKindInfo
	ok := false
	if snap.ResultDir != "" {
		if b, err := os.ReadFile(filepath.Join(snap.ResultDir, "stderr.log")); err == nil {
			if len(b) > 8192 {
				b = b[len(b)-8192:]
			}
			sc := bufio.NewScanner(strings.NewReader(string(b)))
			sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
			for sc.Scan() {
				line := strings.TrimSpace(sc.Text())
				if !strings.HasPrefix(line, "{") {
					continue
				}
				var row struct {
					Type      string `json:"type"`
					Error     string `json:"error"`
					Message   string `json:"message"`
					ErrorKind string `json:"error_kind"`
					ResetAt   string `json:"reset_at"`
				}
				if json.Unmarshal([]byte(line), &row) != nil || row.Type != "error" || strings.TrimSpace(row.ErrorKind) == "" {
					continue
				}
				msg := row.Error
				if msg == "" {
					msg = row.Message
				}
				found = errorKindInfo{Kind: strings.TrimSpace(row.ErrorKind), ResetAt: strings.TrimSpace(row.ResetAt), Message: msg}
				ok = true
			}
		}
	}
	if ok {
		return found, true
	}
	if kind, resetAt, has := runner.ParseErrorKindMarker(snap.Error); has {
		return errorKindInfo{Kind: kind, ResetAt: resetAt, Message: snap.Error}, true
	}
	return errorKindInfo{}, false
}

// quotaTextRe recognises an exhausted allowance in the TEXT of an error: the fallback
// for agents (or suga builds) that carry no error_kind. A quota failure repeats until
// the limit resets, so it must never read as a transient glitch even though the same
// message often also says "rate limit" / 429.
var quotaTextRe = regexp.MustCompile(`(?i)usage limit|insufficient_quota|\bquota\b|limit resets? (?:at|on|in)|billing_hard_limit|(?:http|status|code)\D{0,12}\b402\b|\b402 payment required`)

// isQuotaText reports whether an error text describes an exhausted quota / usage limit.
func isQuotaText(text string) bool { return quotaTextRe.MatchString(text) }

// cwdMismatchRe recognises suga's refusal to resume a session from a different
// directory (run --resume exit 1, session/load JSON-RPC -32602).
var cwdMismatchRe = regexp.MustCompile(`(?i)allow-cwd-change|(?:cwd|working directory|work dir)[^\n]{0,120}(?:differ|mismatch|does not match|not match|changed)`)

// resumeCwdMessage returns suga's own line when a failed job died because the session
// belongs to another directory ("" when it did not). gofer always resumes in the source
// job's cwd, so this only shows up when that directory moved or was removed; the
// message is carried verbatim, and gofer never adds --allow-cwd-change on its own.
func resumeCwdMessage(snap JobResult) string {
	var lines []string
	if snap.ResultDir != "" {
		if b, err := os.ReadFile(filepath.Join(snap.ResultDir, "stderr.log")); err == nil {
			if len(b) > 8192 {
				b = b[len(b)-8192:]
			}
			lines = append(lines, strings.Split(string(b), "\n")...)
		}
	}
	lines = append(lines, strings.Split(snap.Error, "\n")...)
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || !cwdMismatchRe.MatchString(line) {
			continue
		}
		if strings.HasPrefix(line, "{") {
			var row struct {
				Error   string `json:"error"`
				Message string `json:"message"`
			}
			if json.Unmarshal([]byte(line), &row) == nil {
				if row.Error != "" {
					line = row.Error
				} else if row.Message != "" {
					line = row.Message
				}
			}
		}
		return line
	}
	return ""
}

// failureErrorNote is what gofer adds to a failed job's error so the reader does not
// have to open the logs: a quota failure says when the allowance comes back, and a
// resume refused for its directory repeats the agent's message verbatim. "" = nothing
// to add (including when the error already says it).
func failureErrorNote(snap JobResult) string {
	var notes []string
	if ev, ok := errorKindEvidence(snap); ok && ev.Kind == runner.ErrorKindQuota && ev.ResetAt != "" &&
		!strings.Contains(snap.Error, ev.ResetAt) {
		notes = append(notes, "provider quota exhausted; resets at "+ev.ResetAt)
	}
	if msg := resumeCwdMessage(snap); msg != "" && !strings.Contains(snap.Error, msg) {
		notes = append(notes, "resume refused: "+msg)
	}
	return strings.Join(notes, "; ")
}

// appendErrorNote joins a note onto an error string.
func appendErrorNote(errText, note string) string {
	if note == "" {
		return errText
	}
	if strings.TrimSpace(errText) == "" {
		return note
	}
	return errText + " (" + note + ")"
}
