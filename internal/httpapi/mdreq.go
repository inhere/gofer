package httpapi

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/goccy/go-yaml"

	"github.com/inhere/gofer/internal/job"
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
	fm, rest, ok := splitFrontmatter(body)
	if !ok {
		return req, fmt.Errorf("missing yaml frontmatter (expected leading '---' block)")
	}
	if err := yaml.Unmarshal(fm, &req); err != nil {
		return req, fmt.Errorf("invalid frontmatter yaml: %w", err)
	}
	req.Prompt = strings.TrimSpace(string(rest))
	return req, nil
}

// splitFrontmatter separates a leading '---' yaml block from the body. It
// tolerates leading whitespace and \r\n line endings. ok=false when there is no
// opening '---' or no closing '---' line.
func splitFrontmatter(body []byte) (fm, rest []byte, ok bool) {
	b := bytes.TrimLeft(body, " \t\r\n")
	if !bytes.HasPrefix(b, []byte("---")) {
		return nil, nil, false
	}
	b = b[3:]
	idx := bytes.Index(b, []byte("\n---"))
	if idx < 0 {
		return nil, nil, false
	}
	fm = b[:idx]
	rest = b[idx+4:] // skip the "\n---"
	if i := bytes.IndexByte(rest, '\n'); i >= 0 {
		rest = rest[i+1:] // drop the rest of the closing '---' line
	} else {
		rest = nil
	}
	return fm, rest, true
}
