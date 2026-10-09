package hookrelay

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/tracker"
)

// Command-time memory injection (design 2026-10-09-prime-memory-quality-design.md
// §2.9, step P5): on PreToolUse of a shell tool, a memory whose `when.commands`
// prefixes the command the agent is ABOUT to run gets its full text as
// additional context. It shares the P1b machinery: the same candidates, filters
// and order, the same once-per-session set (a memory injected on a prompt is not
// injected again on a command, and vice versa) and the same 2 KiB budget.
//
// The hook must never hold the tool back: the whole path runs under
// Options.CommandDeadline (default DefaultCommandMemoryDeadline) and prints
// nothing when it runs out or anything fails.

// DefaultCommandMemoryDeadline is the hard budget of the PreToolUse path.
const DefaultCommandMemoryDeadline = 300 * time.Millisecond

var (
	// shellWrapRE matches a `bash -lc` / `sh -c` / `zsh -c` wrapper (codex hands
	// argv arrays such as ["bash","-lc","git push"]).
	shellWrapRE = regexp.MustCompile(`^(?:\S*/)?(?:ba|z|da)?sh\s+(?:-[a-z]*c[a-z]*)\s+`)
	// envAssignRE matches one leading NAME=value assignment (value unquoted,
	// 'single' or "double" quoted).
	envAssignRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=(?:'[^']*'|"[^"]*"|\S*)\s+`)
	// cdPrefixRE matches a leading `cd <dir> &&` / `cd <dir>;`.
	cdPrefixRE = regexp.MustCompile(`^cd\s+(?:'[^']*'|"[^"]*"|\S+)\s*(?:&&|;)\s*`)
)

// NormalizeCommand strips what commonly precedes the real command so a
// when.commands prefix can match it: surrounding whitespace, a `bash -lc` /
// `sh -c` wrapper (with its quotes), leading `env`, `NAME=value` assignments
// and `cd <dir> &&` / `cd <dir>;` steps, repeated in any order. Nothing else
// is parsed: a command later in a `&&` chain (other than after cd) does not
// match.
func NormalizeCommand(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	for {
		before := cmd
		if loc := shellWrapRE.FindStringIndex(cmd); loc != nil {
			cmd = unquote(strings.TrimSpace(cmd[loc[1]:]))
		}
		if cmd == "env" {
			return ""
		}
		if strings.HasPrefix(cmd, "env ") || strings.HasPrefix(cmd, "env\t") {
			cmd = strings.TrimSpace(cmd[4:])
		}
		if loc := envAssignRE.FindStringIndex(cmd); loc != nil {
			cmd = cmd[loc[1]:]
		}
		if loc := cdPrefixRE.FindStringIndex(cmd); loc != nil {
			cmd = cmd[loc[1]:]
		}
		cmd = strings.TrimSpace(cmd)
		if cmd == before {
			return cmd
		}
	}
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '\'' || s[0] == '"') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

// preToolUse injects the when.commands-matched memories for the shell command
// the agent is about to run. It never calls the hub (no heartbeat): the event
// fires before every shell call and must cost next to nothing.
func (r *runner) preToolUse() Result {
	if r.opts.CommandMemories == nil || !CatchUpAgent(r.p.dialect()) || strings.TrimSpace(r.p.SessionID) == "" {
		return Result{}
	}
	if !isShellTool(r.p.ToolName) {
		return Result{}
	}
	cmd := NormalizeCommand(r.p.ToolCommand)
	if cmd == "" {
		return Result{}
	}
	deadline := r.opts.CommandDeadline
	if deadline <= 0 {
		deadline = DefaultCommandMemoryDeadline
	}
	type outcome struct {
		state promptMemoryState
		hits  []promptMemoryHit
		text  string
	}
	done := make(chan outcome, 1)
	go func() {
		defer func() {
			if v := recover(); v != nil {
				r.log("command memories: panic: %v", v)
				done <- outcome{}
			}
		}()
		cands, err := r.opts.CommandMemories(r.p.Cwd)
		if err != nil {
			r.log("command memories: load partially failed: %v", err)
		}
		if len(cands) == 0 {
			done <- outcome{}
			return
		}
		state := r.loadMemoryState()
		hits := matchCommandMemories(cands, cmd, r.p.Agent, r.opts.now(), state.Keys)
		if len(hits) == 0 {
			done <- outcome{}
			return
		}
		done <- outcome{state: state, hits: hits, text: renderPromptMemories(hits, r.opts.MemoryBudget)}
	}()
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	select {
	case out := <-done:
		if len(out.hits) == 0 {
			return Result{}
		}
		// Recorded only now: a run that missed the deadline printed nothing, so
		// its memories stay eligible for the next command.
		r.recordInjected(out.state, out.hits)
		r.log("command memories: injected %d (%d bytes) for %q", len(out.hits), len(out.text), head(cmd, 60))
		return Result{Context: out.text}
	case <-timer.C:
		r.log("command memories: deadline %s exceeded, nothing injected", deadline)
		return Result{}
	}
}

// matchCommandMemories picks the candidates whose when.commands prefix cmd (an
// already normalised command), with matchPromptMemories' filters and order.
func matchCommandMemories(cands []PromptMemory, cmd, agent string, now time.Time, seen []string) []promptMemoryHit {
	hits := matchMemories(cands, agent, now, seen, func(m tracker.Memory) (string, bool) {
		return tracker.MemoryMatchesCommand(m.MemoryMeta, cmd)
	})
	for i := range hits {
		hits[i].head = fmt.Sprintf("[gofer 记忆 · 执行 “%s” 前]", hits[i].keyword)
	}
	return hits
}
