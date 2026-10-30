package job

import "strings"

// The task-book marker of a delivery requirement: a block of lines between these two
// tags, each tag alone on its line. The block stays in the prompt the agent receives
// (it tells the agent how the delivery will be checked); gofer only copies its text
// out as an additional source of the delivery-check requirement.
const (
	checkBlockOpen  = "<delivery-check>"
	checkBlockClose = "</delivery-check>"
)

// ExtractCheckBlocks returns the text of every <delivery-check> block of text, in order
// of appearance. A tag counts only when it is alone on its line (surrounding spaces are
// fine), tags inside a fenced code block (``` or ~~~) are ignored, and a block that is
// never closed is dropped. Empty blocks are skipped.
func ExtractCheckBlocks(text string) []string {
	var (
		blocks []string
		cur    []string
		inside bool
		fence  string // the opening fence marker while inside a code block
	)
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if marker := fenceMarker(trimmed); marker != "" {
			switch {
			case fence == "":
				fence = marker
			case strings.HasPrefix(trimmed, fence):
				fence = ""
			}
			if inside {
				cur = append(cur, line)
			}
			continue
		}
		if fence != "" {
			if inside {
				cur = append(cur, line)
			}
			continue
		}
		switch {
		case !inside && trimmed == checkBlockOpen:
			inside, cur = true, cur[:0]
		case inside && trimmed == checkBlockClose:
			inside = false
			if body := strings.TrimSpace(strings.Join(cur, "\n")); body != "" {
				blocks = append(blocks, body)
			}
			cur = nil
		case inside:
			cur = append(cur, line)
		}
	}
	return blocks
}

// fenceMarker returns the fence opener ("```" or "~~~") a trimmed line starts with.
func fenceMarker(trimmed string) string {
	for _, m := range []string{"```", "~~~"} {
		if strings.HasPrefix(trimmed, m) {
			return m
		}
	}
	return ""
}

// applyPromptCheckBlocks folds the prompt's <delivery-check> blocks into the request's
// delivery-check requirement. It is idempotent: a request whose requirement already
// carries the blocks (source prompt_block / merged — a held request re-entering Submit,
// a worker re-entering for a job the hub decided) is left alone. The prompt itself is
// never changed.
func applyPromptCheckBlocks(req *JobRequest) {
	if req.CheckPromptSource == CheckPromptSourcePromptBlock || req.CheckPromptSource == CheckPromptSourceMerged {
		return
	}
	blocks := ExtractCheckBlocks(req.Prompt)
	if len(blocks) == 0 {
		return
	}
	text := strings.Join(blocks, "\n\n")
	explicit := strings.TrimSpace(req.CheckPrompt)
	if explicit == "" {
		req.CheckPrompt, req.CheckPromptSource = text, CheckPromptSourcePromptBlock
		return
	}
	req.CheckPrompt = "### " + checkSectionExplicit + "\n" + explicit + "\n\n### " + checkSectionPromptBlock + "\n" + text
	req.CheckPromptSource = CheckPromptSourceMerged
}
