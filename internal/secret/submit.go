package secret

import "regexp"

var submitSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN [A-Z ]+ PRIVATE KEY-----`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`(?:^|[^A-Za-z0-9])(sk-|ghp_|github_pat_|xox[abp]-)[A-Za-z0-9_\-]{8,}`),
	regexp.MustCompile(`(?i)\b(?:key|secret|token|password)\s*[=:]\s*\S{16,}`),
}

// ScanSubmission returns location labels only. It never returns matching text,
// so callers can safely print the warning without echoing a credential.
func ScanSubmission(command, args []string, prompt string) []string {
	var out []string
	if hasSubmitSecret(join(command)) {
		out = append(out, "command")
	}
	if hasSubmitSecret(join(args)) {
		out = append(out, "args")
	}
	if hasSubmitSecret(prompt) {
		out = append(out, "prompt")
	}
	return out
}

func join(values []string) string {
	var out string
	for i, value := range values {
		if i > 0 {
			out += " "
		}
		out += value
	}
	return out
}

func hasSubmitSecret(value string) bool {
	for _, pattern := range submitSecretPatterns {
		if pattern.MatchString(value) {
			return true
		}
	}
	return false
}
