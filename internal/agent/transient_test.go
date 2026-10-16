package agent

import (
	"reflect"
	"regexp"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// builtinTransientHits lists the provider-side failures the built-in list must catch,
// one per measured class: SUP-01 (status / connection), AUTO-05 (stall) and gofer-aiuj
// (stream interruption).
var builtinTransientHits = []string{
	"Selected model is at capacity",
	"RATE LIMIT",
	"overloaded",
	"too many requests",
	"HTTP 429",
	"HTTP 503",
	"econnreset",
	"Connection reset",
	"stream disconnected",
	"temporarily unavailable",
	"windows sandbox failed",
	"connecting runner pipe",
	"failed: stalled: no output for 900s",
	// gofer-aiuj: the provider stream ended mid-chunk, so the CLI failed with a PARSE
	// error. The first line is the exact message of job 20261015-225437-26c9c11c.
	"provider error: parse openai stream chunk: unexpected end of JSON input",
	"parse anthropic stream chunk: unexpected EOF",
	"streaming response interrupted: unexpected end of JSON input",
	"unexpected EOF while reading the stream chunk",
	"ndjson stream ended: unexpected end of input",
}

// builtinTransientMisses lists failures that must NOT be classified transient: real
// work-level errors, and — the gofer-aiuj requirement — ORDINARY JSON parse errors
// (a corrupt config / session / data file), which re-running the same prompt cannot
// repair.
var builtinTransientMisses = []string{
	"compile error",
	"14290",
	"5030",
	"panic: nil pointer dereference in the agent",
	"json: cannot unmarshal string into Go struct field Config.port of type int",
	"parse /home/x/config.json: unexpected end of JSON input",
	"open /tmp/session.json: unexpected EOF",
	"decode session record: unexpected end of JSON input",
	"json parse error: invalid character '}' looking for beginning of value",
	"wrote 1024 chunks to the stream log",
}

// cfgOf builds a one-agent config, the smallest snapshot ResolveAgent needs.
func cfgOf(key, typ, command string) *config.Config {
	return &config.Config{Agents: map[string]config.AgentConfig{key: {Type: typ, Command: command}}}
}

// patternsOf resolves key the way the job layer does and returns the transient
// patterns it ends up with (resolve is where the built-ins are filled in).
func patternsOf(t *testing.T, cfg *config.Config, key string) []string {
	t.Helper()
	ac, ok := ResolveAgent(cfg, key)
	if !ok {
		t.Fatalf("agent %q did not resolve", key)
	}
	return ac.TransientErrorPatterns
}

// transientMatches mirrors internal/job.transientHit: every pattern is compiled
// case-insensitively and the text counts as transient when ANY pattern hits.
func transientMatches(t *testing.T, patterns []string, text string) bool {
	t.Helper()
	for _, p := range patterns {
		re, err := regexp.Compile("(?i)" + p)
		if err != nil {
			t.Fatalf("pattern %q does not compile: %v", p, err)
		}
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

// checkBuiltinPatterns is the shared positive/negative pair: every transient class
// matches, and none of the real failures does.
func checkBuiltinPatterns(t *testing.T, patterns []string) {
	t.Helper()
	for _, message := range builtinTransientHits {
		if !transientMatches(t, patterns, message) {
			t.Errorf("built-in patterns did not match %q", message)
		}
	}
	for _, message := range builtinTransientMisses {
		if transientMatches(t, patterns, message) {
			t.Errorf("built-in patterns incorrectly matched %q", message)
		}
	}
}

// TestTransientPatternsBuiltinAndOverride: an agent that configures no
// transient_error_patterns adopts the built-ins when its name (the key, or the base
// name of Command) is one gofer has measured; an explicit list — including an empty
// one — replaces them as written.
func TestTransientPatternsBuiltinAndOverride(t *testing.T) {
	for _, command := range []string{"codex", "claude", "omp", "suga", `C:\tools\CODEX.EXE`, `C:\tools\SUGA.EXE`, "/opt/bin/claude"} {
		t.Run(command, func(t *testing.T) {
			cfg := cfgOf("custom", TypeCLIAgent, command)
			patterns := patternsOf(t, cfg, "custom")
			if len(patterns) == 0 {
				t.Fatalf("command %q acquired no built-in patterns", command)
			}
			checkBuiltinPatterns(t, patterns)

			for _, override := range [][]string{{"provider busy"}, {}} {
				cfg.Agents["custom"] = config.AgentConfig{Type: TypeCLIAgent, Command: command, TransientErrorPatterns: override}
				if got := patternsOf(t, cfg, "custom"); !reflect.DeepEqual(got, override) {
					t.Fatalf("override = %#v, want %#v", got, override)
				}
			}
		})
	}
	if patterns := patternsOf(t, cfgOf("custom", TypeCLIAgent, "unknown-agent"), "custom"); len(patterns) != 0 {
		t.Fatalf("unknown command acquired built-in patterns: %v", patterns)
	}
}

// TestTransientPatternsSugaAgents (gofer-aiuj): both spellings of the suga agent carry
// the built-ins — the CLI by key, and the ACP front-end (an acp-agent running the same
// `suga` CLI) through the command-base fallback. Before the fix suga matched neither
// list and its provider failures were classified as ordinary failures, so nothing
// continued the job.
func TestTransientPatternsSugaAgents(t *testing.T) {
	for _, tc := range []struct {
		key, typ, command string
	}{
		{"suga", TypeCLIAgent, "suga"},
		{"suga-acp", TypeACPAgent, "suga"},
		{"my-suga", TypeCLIAgent, "/usr/local/bin/suga"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			patterns := patternsOf(t, cfgOf(tc.key, tc.typ, tc.command), tc.key)
			if len(patterns) == 0 {
				t.Fatalf("agent %q (%s) acquired no built-in patterns", tc.key, tc.typ)
			}
			checkBuiltinPatterns(t, patterns)
			if !transientMatches(t, patterns, "provider error: parse openai stream chunk: unexpected end of JSON input") {
				t.Error("the reported stream-chunk parse failure must match")
			}
		})
	}
}

// TestTransientStreamPatternGating pins the rule that keeps the streaming class from
// swallowing ordinary JSON errors: the JSON spelling counts as transient only when a
// stream word sits within the same line, in either order.
func TestTransientStreamPatternGating(t *testing.T) {
	patterns := []string{builtinTransientStreamPattern}
	for _, message := range []string{
		"provider error: parse openai stream chunk: unexpected end of JSON input",
		"read stream: unexpected EOF",
		"unexpected EOF while reading the stream",
		"chunk decode: unexpected end of JSON input",
	} {
		if !transientMatches(t, patterns, message) {
			t.Errorf("stream pattern did not match %q", message)
		}
	}
	for _, message := range []string{
		"unexpected end of JSON input",
		"unexpected EOF",
		"json: cannot unmarshal number into Go value of type string",
		"invalid character 'x' looking for beginning of value",
		"parse session file: unexpected end of JSON input",
		// The two halves are on DIFFERENT lines, so they never pair up.
		"stream open failed\nunexpected EOF",
	} {
		if transientMatches(t, patterns, message) {
			t.Errorf("stream pattern incorrectly matched %q", message)
		}
	}
}
