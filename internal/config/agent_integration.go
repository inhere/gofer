package config

import (
	"fmt"
	"regexp"
	"strings"
)

// TranscriptDialects are the values AgentConfig.TranscriptDialect accepts (they mirror
// the dialect constants of internal/work/transcript).
var TranscriptDialects = []string{"claude", "codex", "omp", "generic"}

// InjectGuardConfig is one agent's input-area recognition for the terminal delivery
// guard (design 2026-10-22 D4): the tmux pane's last lines are captured before a
// message is typed into it, and the lists answer three questions. An empty list means
// "no rule of this kind"; every pattern is a Go regexp matched against one trimmed
// line of the capture. `selecting` is judged first (a dialog drawn over the input box),
// then `has_input`, then `empty` — a capture matching nothing is 不确定 and no message
// is typed into it while the session's mode is auto.
type InjectGuardConfig struct {
	// Empty matches the agent's EMPTY input area (the prompt line, or its placeholder).
	Empty []string `yaml:"empty,omitempty"`
	// HasInput matches an input line that already carries text a person typed.
	HasInput []string `yaml:"has_input,omitempty"`
	// Selecting matches a selection UI / question dialog (AskUserQuestion, a permission
	// prompt) where the Enter behind a paste would answer for the person.
	Selecting []string `yaml:"selecting,omitempty"`
}

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
	if g := ac.InjectGuard; g != nil {
		for _, list := range []struct {
			name string
			pats []string
		}{
			{"empty", g.Empty}, {"has_input", g.HasInput}, {"selecting", g.Selecting},
		} {
			for i, p := range list.pats {
				if _, err := regexp.Compile(p); err != nil {
					return fmt.Errorf("agent %q: inject_guard.%s[%d] %q is not a valid regexp: %v", key, list.name, i, p, err)
				}
			}
		}
	}
	if len(ac.DeliverCommand) > 0 {
		if ac.Type != "" && ac.Type != "cli-agent" {
			return fmt.Errorf("agent %q: deliver_command only applies to a cli-agent", key)
		}
		// deliver_image_args is the image half of the same command: each image expands it
		// once, so a template that names no {{image}} could never carry one.
		if len(ac.DeliverImageArgs) > 0 && !containsTemplate(ac.DeliverImageArgs, "{{image}}") {
			return fmt.Errorf("agent %q: deliver_image_args must contain {{image}}", key)
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
	} else if len(ac.DeliverImageArgs) > 0 {
		return fmt.Errorf("agent %q: deliver_image_args needs deliver_command", key)
	}
	if ac.DeliverOfflineMatch != "" {
		if len(ac.DeliverCommand) == 0 {
			return fmt.Errorf("agent %q: deliver_offline_match needs deliver_command", key)
		}
		if _, err := regexp.Compile(ac.DeliverOfflineMatch); err != nil {
			return fmt.Errorf("agent %q: deliver_offline_match is not a valid regexp: %v", key, err)
		}
	}
	if f := ac.SessionFamily; f != "" && !validFamilyName(f) {
		return fmt.Errorf("agent %q: session_family %q must be letters, digits, '-', '_' or '.'", key, f)
	}
	return nil
}

// containsTemplate reports whether any template element carries token.
func containsTemplate(args []string, token string) bool {
	for _, arg := range args {
		if strings.Contains(arg, token) {
			return true
		}
	}
	return false
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
