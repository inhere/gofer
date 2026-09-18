package httpapi

import (
	"fmt"
	"strings"

	"github.com/goccy/go-yaml"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/template"
)

// maxMarkdownBytes caps the md+yaml submit body (frontmatter + prose) to bound
// memory / abuse on the markdown submit path (design §9 / P1-b).
const maxMarkdownBytes = 256 * 1024

// parseMarkdownRequest parses a "yaml frontmatter + markdown body" document into
// a JobRequest: the leading '---' block becomes the request fields (via the
// JobRequest yaml tags) and the remaining prose becomes Prompt. It is the
// transport for cli-agent submits (codex/claude); exec is rejected upstream
// (design §6.2). CallerID is never taken from frontmatter (yaml:"-"); the
// handler stamps it from the auth context.
func parseMarkdownRequest(body []byte) (job.JobRequest, error) {
	var req job.JobRequest
	if len(body) > maxMarkdownBytes {
		return req, fmt.Errorf("markdown body exceeds %d bytes", maxMarkdownBytes)
	}
	fm, rest, ok := template.SplitFrontmatter(body)
	if !ok {
		return req, fmt.Errorf("missing yaml frontmatter (expected leading '---' block)")
	}
	if err := yaml.Unmarshal(fm, &req); err != nil {
		return req, fmt.Errorf("invalid frontmatter yaml: %w", err)
	}
	req.Prompt = strings.TrimSpace(string(rest))
	if req.Title == "" {
		req.Title = firstMarkdownHeading(string(rest))
	}
	return req, nil
}

// firstMarkdownHeading extracts the first ATX heading from markdown body text.
// It intentionally keeps parsing shallow; code fences are not special-cased.
func firstMarkdownHeading(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		spaces := 0
		for spaces < len(line) && line[spaces] == ' ' {
			spaces++
		}
		if spaces > 3 || spaces >= len(line) || line[spaces] != '#' {
			continue
		}
		i := spaces
		for i < len(line) && line[i] == '#' {
			i++
		}
		if level := i - spaces; level < 1 || level > 6 {
			continue
		}
		if i >= len(line) || (line[i] != ' ' && line[i] != '\t') {
			continue
		}
		text := strings.TrimSpace(line[i+1:])
		text = strings.TrimSpace(strings.TrimRight(text, "#"))
		if len([]rune(text)) > 200 {
			r := []rune(text)
			text = string(r[:200])
		}
		return text
	}
	return ""
}
