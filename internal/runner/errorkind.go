package runner

import (
	"regexp"
	"strings"
)

// The agent's own verdict on a provider failure (suga's error_kind contract). suga labels
// every provider error it reports, so gofer need not guess from the message text:
//
//   - provider_transient: the stream broke, or an overload / rate-limit / 408 / 429 / 5xx /
//     connection error outlived suga's own retries — the only kind worth continuing;
//   - provider_quota: an allowance (credit, weekly usage) is exhausted — continuing
//     burns a dispatch and fails the same way until the limit resets;
//   - provider_rejected: the request itself was refused (400 / 401 / 403 / 404 / 422);
//   - provider_bad_response: an invalid JSON body or a permanent API error.
//
// Any other value is treated as "not transient": a kind this build has never heard of is
// a reason to stop, not to repeat the run.
const (
	ErrorKindTransient   = "provider_transient"
	ErrorKindQuota       = "provider_quota"
	ErrorKindRejected    = "provider_rejected"
	ErrorKindBadResponse = "provider_bad_response"
)

// ErrorKindMarker renders the kind (and the quota reset time, when the agent gave one) as
// a bracketed suffix of an error string, the one carrier that survives every route a job
// error takes (a remote worker's result carries only the error text). The ACP runner
// writes it from a prompt error response's `data`; ParseErrorKindMarker reads it back.
func ErrorKindMarker(kind, resetAt string) string {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return ""
	}
	out := " [error_kind=" + kind
	if r := strings.TrimSpace(resetAt); r != "" {
		out += " reset_at=" + r
	}
	return out + "]"
}

var (
	errorKindRe = regexp.MustCompile(`error_kind=([A-Za-z0-9_.-]+)`)
	resetAtRe   = regexp.MustCompile(`reset_at=([^\s\]]+)`)
)

// ParseErrorKindMarker reads an ErrorKindMarker back out of an error string.
func ParseErrorKindMarker(text string) (kind, resetAt string, ok bool) {
	m := errorKindRe.FindStringSubmatch(text)
	if m == nil {
		return "", "", false
	}
	if r := resetAtRe.FindStringSubmatch(text); r != nil {
		resetAt = r[1]
	}
	return m[1], resetAt, true
}
