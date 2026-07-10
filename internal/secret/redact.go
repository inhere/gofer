// Package secret holds credential-redaction primitives shared by the workflow export
// path and the job request-redact path. Leaf package (imports only regexp) so both
// internal/job and internal/job/workflow reuse it without a cycle (internal/job cannot
// import internal/job/workflow — G024).
package secret

import "regexp"

// Placeholder replaces a value that matched a secret pattern (SR403). Structure is
// preserved so a redacted request/spec stays a runnable template; the real value must
// be filled back in before re-running.
const Placeholder = "***REDACTED***"

var secretKVPattern = regexp.MustCompile(
	`(?i)\b([\w.\-]*(?:secret|token|password|passwd|api[_\-]?key|access[_\-]?key|private[_\-]?key|auth|bearer|credential)[\w.\-]*\s*[:=]\s*)("?[^"\s]+"?)`,
)
var secretFlagPattern = regexp.MustCompile(
	`(?i)(--?[\w\-]*(?:secret|token|password|passwd|api[_\-]?key|access[_\-]?key|private[_\-]?key|auth|bearer|credential)[\w\-]*[=\s]+)(\S+)`,
)

// RedactString replaces credential-looking assignments/flags in s with Placeholder,
// keeping the key/flag so the output stays a usable template. Returns the scrubbed
// string and whether anything was redacted. 逐字迁自 export.go:40-62。
func RedactString(s string) (string, bool) {
	if s == "" {
		return s, false
	}
	redacted := false
	out := secretFlagPattern.ReplaceAllStringFunc(s, func(m string) string {
		sub := secretFlagPattern.FindStringSubmatch(m)
		if len(sub) != 3 {
			return m
		}
		redacted = true
		return sub[1] + Placeholder
	})
	out = secretKVPattern.ReplaceAllStringFunc(out, func(m string) string {
		sub := secretKVPattern.FindStringSubmatch(m)
		if len(sub) != 3 {
			return m
		}
		redacted = true
		return sub[1] + Placeholder
	})
	return out, redacted
}
