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

// Command-line passwords the key=value / --flag rules miss. Only the password group
// (1) is replaced; the command, flag and user stay.
var secretPositionalPatterns = []*regexp.Regexp{
	// mysql -p<pass> (attached; "-p" alone prompts, "-P" is the port). psql's -p is a
	// port, its password travels as PGPASSWORD= (the key=value rule) or in a URL.
	regexp.MustCompile(`\b(?i:mysql|mysqldump|mysqladmin|mysqlimport|mysqlcheck|mysqlshow|mysqlpump|mariadb|mariadb-dump|mariadb-admin)(?:\.exe)?\b[^\n|;&]*?\s-p([^\s'"]+|'[^']*'|"[^"]*")`),
	// curl -u user:pass / --user user:pass / --user=user:pass
	regexp.MustCompile(`\b(?i:curl)(?:\.exe)?\b[^\n|;&]*?\s(?:-u|--user)(?:=|\s+)['"]?[^\s:'"]+:([^\s'"]+)`),
	// scheme://user:pass@host
	regexp.MustCompile(`\b[A-Za-z][A-Za-z0-9+.\-]*://[^\s/:@]+:([^\s/@]+)@`),
}

// redactGroup1 replaces capture group 1 of every match of re with Placeholder.
func redactGroup1(re *regexp.Regexp, s string) (string, bool) {
	idx := re.FindAllStringSubmatchIndex(s, -1)
	if len(idx) == 0 {
		return s, false
	}
	var b []byte
	last, hit := 0, false
	for _, m := range idx {
		if len(m) < 4 || m[2] < 0 || s[m[2]:m[3]] == Placeholder {
			continue
		}
		b = append(b, s[last:m[2]]...)
		b = append(b, Placeholder...)
		last, hit = m[3], true
	}
	if !hit {
		return s, false
	}
	return string(append(b, s[last:]...)), true
}

// RedactString replaces credential-looking assignments/flags in s with Placeholder,
// keeping the key/flag so the output stays a usable template. Returns the scrubbed
// string and whether anything was redacted. 逐字迁自 export.go:40-62；另加命令行
// 位置密码（mysql -p<pass>、curl -u user:pass、URL user:pass@）。
func RedactString(s string) (string, bool) {
	if s == "" {
		return s, false
	}
	redacted := false
	for _, re := range secretPositionalPatterns {
		var hit bool
		if s, hit = redactGroup1(re, s); hit {
			redacted = true
		}
	}
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
