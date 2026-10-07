package config

import (
	"fmt"
	"strings"
)

// TranscriptDialects are the values AgentConfig.TranscriptDialect accepts (they mirror
// the dialect constants of internal/work/transcript).
var TranscriptDialects = []string{"claude", "codex", "omp", "generic"}

// validateAgentIntegration checks the four fields a self-built agent uses to plug into
// gofer (usage path, transcript dialect, tmux inject process names, session family).
// A typo'd value would otherwise silently do nothing, so every one is rejected at load.
func validateAgentIntegration(key string, ac AgentConfig) error {
	if d := ac.TranscriptDialect; d != "" {
		ok := false
		for _, v := range TranscriptDialects {
			if v == d {
				ok = true
			}
		}
		if !ok {
			return fmt.Errorf("agent %q: unknown transcript_dialect %q (want %s)", key, d, strings.Join(TranscriptDialects, "|"))
		}
	}
	if p := ac.NDJSONUsagePath; p != "" {
		if !validDottedPath(p) {
			return fmt.Errorf("agent %q: ndjson_usage_path %q must be a dotted path such as usage or result.usage", key, p)
		}
		if ac.Type == "acp-agent" || ac.Type == "exec" {
			return fmt.Errorf("agent %q: ndjson_usage_path only applies to a cli-agent", key)
		}
		if ac.OutputFormat != OutputFormatNDJSON {
			return fmt.Errorf("agent %q: ndjson_usage_path needs output_format: %s", key, OutputFormatNDJSON)
		}
	}
	for i, n := range ac.InjectProcess {
		if !ValidInjectProcessName(n) {
			return fmt.Errorf("agent %q: inject_process[%d] %q must be a bare process name (no path separator, no extension)", key, i, n)
		}
	}
	if len(ac.DeliverCommand) > 0 {
		if ac.Type != "" && ac.Type != "cli-agent" {
			return fmt.Errorf("agent %q: deliver_command only applies to a cli-agent", key)
		}
		all := strings.Join(ac.DeliverCommand, "\x00")
		if !strings.Contains(all, "{{session_id}}") {
			return fmt.Errorf("agent %q: deliver_command must contain {{session_id}}", key)
		}
		hasText := strings.Contains(all, "{{text}}")
		switch {
		case ac.DeliverStdin && hasText:
			return fmt.Errorf("agent %q: deliver_command with deliver_stdin: true must not contain {{text}} (the text goes to stdin)", key)
		case !ac.DeliverStdin && !hasText:
			return fmt.Errorf("agent %q: deliver_command must contain {{text}} (or set deliver_stdin: true)", key)
		}
	} else if ac.DeliverStdin {
		return fmt.Errorf("agent %q: deliver_stdin needs deliver_command", key)
	}
	if f := ac.SessionFamily; f != "" && !validFamilyName(f) {
		return fmt.Errorf("agent %q: session_family %q must be letters, digits, '-', '_' or '.'", key, f)
	}
	return nil
}

func validDottedPath(p string) bool {
	for _, seg := range strings.Split(p, ".") {
		if seg == "" || strings.ContainsAny(seg, " \t/\\") {
			return false
		}
	}
	return true
}

func validFamilyName(f string) bool {
	for _, r := range f {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

// ValidInjectProcessName reports whether n is a bare process name: non-empty, no path
// separator, no whitespace, and no ".exe"-style extension (the matcher strips the
// extension from the live process name, so a configured one could never match).
func ValidInjectProcessName(n string) bool {
	if n == "" || strings.ContainsAny(n, "/\\ \t") {
		return false
	}
	return !strings.EqualFold(n[max(0, len(n)-4):], ".exe")
}
