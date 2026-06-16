package commands

import (
	"strings"

	"github.com/gookit/gcli/v3"
)

// NormalizeArgs reorders CLI args so positional arguments that appear before
// flags are moved after them, which Go's flag parser (used by gcli) requires.
//
// Background: gcli/stdlib flag parsing stops at the first non-flag token, so
// `project add self --host-path X` fails to bind --host-path. We follow the
// command path through the registered command tree, then move the leading
// contiguous block of positionals in the remaining tail to the end. This keeps
// flag/value pairs intact (only a positional block is relocated) and leaves an
// already flags-first command line unchanged. A `--` token ends reordering so
// raw exec commands (P6) are preserved verbatim.
func NormalizeArgs(app *gcli.App, args []string) []string {
	if app == nil || len(args) == 0 {
		return args
	}

	// 1) Consume the command path (leading tokens that name (sub)commands).
	pathLen := 0
	cur := app.GetCommand(args[0])
	if cur == nil || isFlag(args[0]) {
		return args // first token is a flag or not a known command; leave as-is
	}
	pathLen = 1
	for pathLen < len(args) {
		tok := args[pathLen]
		if isFlag(tok) || tok == "--" {
			break
		}
		sub := cur.GetCommand(tok)
		if sub == nil {
			break // not a deeper command -> this is a positional arg
		}
		cur = sub
		pathLen++
	}

	head := args[:pathLen]
	tail := args[pathLen:]
	return append(head, moveLeadingPositionals(tail)...)
}

// moveLeadingPositionals moves the leading contiguous non-flag tokens of tail to
// the end, stopping at a `--` separator. Returns tail unchanged when it already
// starts with a flag or contains `--`.
func moveLeadingPositionals(tail []string) []string {
	if len(tail) < 2 || isFlag(tail[0]) {
		return tail
	}
	// Do not reorder across an explicit `--` separator.
	for _, t := range tail {
		if t == "--" {
			return tail
		}
	}

	i := 0
	for i < len(tail) && !isFlag(tail[i]) {
		i++
	}
	if i == 0 || i == len(tail) {
		return tail // no flags follow; nothing to reorder
	}
	leading := tail[:i]
	rest := tail[i:]
	out := make([]string, 0, len(tail))
	out = append(out, rest...)
	out = append(out, leading...)
	return out
}

func isFlag(s string) bool {
	return strings.HasPrefix(s, "-") && s != "-"
}
