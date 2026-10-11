package configlint

import (
	"regexp"
	"strings"
)

// keyLine matches a block-mapping key line: `  name:` / `  name: value` (not a list
// item, not a comment). Quoted keys are accepted.
var keyLine = regexp.MustCompile(`^( *)("[^"]+"|'[^']+'|[A-Za-z0-9_.\-]+):(\s|$)`)

// node is one block-mapping key of a yaml file: its dotted path and the line range
// [start, end) it occupies — head comment lines glued above it included, trailing
// blank / comment lines excluded.
type node struct {
	path   string
	depth  int // 1 = top-level
	indent int
	key    int // line index of the key itself
	start  int
	end    int
}

// scanNodes lists the block-mapping keys of text by indentation. It understands what
// gofer configs use — nested block mappings, block and flow sequences (including list
// items at the parent key's indent), block scalars — and treats a key it cannot place
// as a leaf. Keys inside list items are not listed (lint never removes those).
func scanNodes(lines []string) []node {
	type frame struct {
		indent int
		path   string
	}
	var stack []frame
	var out []node
	for i, l := range lines {
		m := keyLine.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		indent := len(m[1])
		name := strings.Trim(m[2], `"'`)
		for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}
		if insideListItem(lines, i, indent) || insideBlockScalar(lines, i, indent) {
			continue
		}
		p := name
		if len(stack) > 0 {
			p = stack[len(stack)-1].path + "." + name
		}
		stack = append(stack, frame{indent: indent, path: p})
		n := node{path: p, depth: len(stack), indent: indent, key: i}
		n.start = i
		for n.start > 0 && isCommentAt(lines[n.start-1], indent) {
			n.start--
		}
		n.end = extent(lines, i, indent)
		out = append(out, n)
	}
	return out
}

// extent returns the end (exclusive) of the value of the key at line i: following
// lines that are deeper, list items at the same indent, or blank — minus the trailing
// blank / comment lines, which belong to whatever comes next.
func extent(lines []string, i, indent int) int {
	last := i
	for j := i + 1; j < len(lines); j++ {
		l := lines[j]
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		in := len(l) - len(strings.TrimLeft(l, " "))
		if in > indent || (in == indent && strings.HasPrefix(t, "- ")) || (in == indent && t == "-") {
			if !strings.HasPrefix(t, "#") {
				last = j
			}
			continue
		}
		break
	}
	return last + 1
}

func isCommentAt(l string, indent int) bool {
	t := strings.TrimLeft(l, " ")
	return strings.HasPrefix(t, "#") && len(l)-len(t) == indent
}

// insideListItem reports whether the key at line i sits inside a sequence item (an
// enclosing `- ` line at a shallower indent before reaching the parent key).
func insideListItem(lines []string, i, indent int) bool {
	if strings.HasPrefix(strings.TrimLeft(lines[i], " "), "- ") {
		return true
	}
	for j := i - 1; j >= 0; j-- {
		l := lines[j]
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		in := len(l) - len(strings.TrimLeft(l, " "))
		if in >= indent {
			continue
		}
		return strings.HasPrefix(t, "- ")
	}
	return false
}

// insideBlockScalar reports whether line i is part of a `key: |` / `key: >` block.
func insideBlockScalar(lines []string, i, indent int) bool {
	for j := i - 1; j >= 0; j-- {
		l := lines[j]
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		in := len(l) - len(strings.TrimLeft(l, " "))
		if in >= indent {
			continue
		}
		return strings.HasSuffix(t, "|") || strings.HasSuffix(t, ">") || strings.HasSuffix(t, "|-") || strings.HasSuffix(t, ">-")
	}
	return false
}

// without returns lines with [start, end) removed.
func without(lines []string, start, end int) []string {
	out := make([]string, 0, len(lines))
	out = append(out, lines[:start]...)
	return append(out, lines[end:]...)
}
