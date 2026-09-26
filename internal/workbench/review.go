package workbench

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

var diffHunkHeader = regexp.MustCompile(`^@@ -([0-9]+)(?:,[0-9]+)? \+([0-9]+)(?:,[0-9]+)? @@`)

// Review converts structured inline comments into one ordinary workbench turn.
// Turn remains the only dispatch owner, so its session, runner, and state gates
// apply unchanged to review continuations.
func (s *Service) Review(callerID, threadID string, input ReviewInput) (TurnResult, error) {
	kind, _, err := ParseThreadID(threadID)
	if err != nil {
		return TurnResult{}, err
	}
	if kind != KindAgent {
		return TurnResult{}, fmt.Errorf("%w: 该会话没有可续接的 agent session", ErrNotResumable)
	}
	normalized, err := normalizeReviewInput(input)
	if err != nil {
		return TurnResult{}, err
	}
	var patch string
	if len(normalized.Comments) > 0 {
		diff, err := s.Diff(threadID)
		if err != nil {
			return TurnResult{}, err
		}
		patch = diff.Patch
	}
	prompt := buildReviewPrompt(normalized, patch)
	seen := true
	if _, err := s.Patch(callerID, threadID, PatchInput{Seen: &seen}); err != nil {
		return TurnResult{}, err
	}
	return s.Turn(callerID, threadID, prompt)
}

func normalizeReviewInput(input ReviewInput) (ReviewInput, error) {
	input.Summary = strings.TrimSpace(input.Summary)
	if len(input.Comments) > MaxReviewComments {
		return ReviewInput{}, fmt.Errorf("%w: comments must contain at most %d items", ErrInvalidReview, MaxReviewComments)
	}
	comments := make([]ReviewComment, len(input.Comments))
	for i, comment := range input.Comments {
		comment.Path = strings.TrimSpace(comment.Path)
		comment.Text = strings.TrimSpace(comment.Text)
		if comment.Path == "" {
			return ReviewInput{}, fmt.Errorf("%w: comments[%d].path is required", ErrInvalidReview, i)
		}
		if comment.Line <= 0 {
			return ReviewInput{}, fmt.Errorf("%w: comments[%d].line must be positive", ErrInvalidReview, i)
		}
		if comment.Side != ReviewSideNew && comment.Side != ReviewSideOld {
			return ReviewInput{}, fmt.Errorf("%w: comments[%d].side must be old or new", ErrInvalidReview, i)
		}
		if comment.Text == "" {
			return ReviewInput{}, fmt.Errorf("%w: comments[%d].text is required", ErrInvalidReview, i)
		}
		if utf8.RuneCountInString(comment.Text) > MaxReviewCommentRunes {
			return ReviewInput{}, fmt.Errorf("%w: comments[%d].text exceeds %d characters", ErrInvalidReview, i, MaxReviewCommentRunes)
		}
		comments[i] = comment
	}
	if input.Summary == "" && len(comments) == 0 {
		return ReviewInput{}, fmt.Errorf("%w: summary or comments is required", ErrInvalidReview)
	}
	input.Comments = comments
	return input, nil
}

func buildReviewPrompt(input ReviewInput, patch string) string {
	parts := make([]string, 0, len(input.Comments)+1)
	if input.Summary != "" {
		parts = append(parts, input.Summary)
	}
	index := parseReviewPatch(patch)
	for _, comment := range input.Comments {
		var block strings.Builder
		fmt.Fprintf(&block, "%s:%d (%s)\n", comment.Path, comment.Line, comment.Side)
		if context := index.context(comment); context != "" {
			fence := reviewFence(context)
			block.WriteString(fence)
			block.WriteString("diff\n")
			block.WriteString(context)
			if !strings.HasSuffix(context, "\n") {
				block.WriteString("\n")
			}
			block.WriteString(fence)
			block.WriteString("\n")
		}
		block.WriteString(comment.Text)
		parts = append(parts, block.String())
	}
	return strings.Join(parts, "\n\n")
}

type reviewPatchLine struct {
	text    string
	oldLine int
	newLine int
}

type reviewPatchHunk struct {
	oldPath string
	newPath string
	lines   []reviewPatchLine
}

type reviewPatchIndex []reviewPatchHunk

func (index reviewPatchIndex) context(comment ReviewComment) string {
	for _, hunk := range index {
		path := hunk.newPath
		if comment.Side == ReviewSideOld {
			path = hunk.oldPath
		}
		if path != comment.Path {
			continue
		}
		for lineIndex, line := range hunk.lines {
			lineNumber := line.newLine
			if comment.Side == ReviewSideOld {
				lineNumber = line.oldLine
			}
			if lineNumber != comment.Line {
				continue
			}
			start := lineIndex - 2
			if start < 0 {
				start = 0
			}
			end := lineIndex + 3
			if end > len(hunk.lines) {
				end = len(hunk.lines)
			}
			var lines strings.Builder
			for _, nearby := range hunk.lines[start:end] {
				lines.WriteString(nearby.text)
				lines.WriteString("\n")
			}
			return lines.String()
		}
	}
	return ""
}

func parseReviewPatch(patch string) reviewPatchIndex {
	var (
		result           reviewPatchIndex
		oldPath, newPath string
		oldLine, newLine int
		current          *reviewPatchHunk
	)
	for _, raw := range strings.Split(patch, "\n") {
		line := strings.TrimSuffix(raw, "\r")
		switch {
		case strings.HasPrefix(line, "diff --git "):
			oldPath, newPath = diffHeaderPaths(line)
			current = nil
		case strings.HasPrefix(line, "--- "):
			oldPath = reviewPatchPath(strings.TrimPrefix(line, "--- "), "a/")
		case strings.HasPrefix(line, "+++ "):
			newPath = reviewPatchPath(strings.TrimPrefix(line, "+++ "), "b/")
		case diffHunkHeader.MatchString(line):
			match := diffHunkHeader.FindStringSubmatch(line)
			oldLine, _ = strconv.Atoi(match[1])
			newLine, _ = strconv.Atoi(match[2])
			result = append(result, reviewPatchHunk{oldPath: oldPath, newPath: newPath})
			current = &result[len(result)-1]
		case current == nil || line == "" || strings.HasPrefix(line, "\\ No newline"):
			continue
		default:
			entry := reviewPatchLine{text: line}
			switch line[0] {
			case ' ':
				entry.oldLine, entry.newLine = oldLine, newLine
				oldLine++
				newLine++
			case '-':
				entry.oldLine = oldLine
				oldLine++
			case '+':
				entry.newLine = newLine
				newLine++
			default:
				continue
			}
			current.lines = append(current.lines, entry)
		}
	}
	return result
}

func diffHeaderPaths(line string) (string, string) {
	body := strings.TrimPrefix(line, "diff --git ")
	separator := strings.Index(body, " b/")
	if separator < 0 {
		return "", ""
	}
	return strings.TrimPrefix(body[:separator], "a/"), body[separator+3:]
}

func reviewPatchPath(value, prefix string) string {
	if value == "/dev/null" {
		return ""
	}
	return strings.TrimPrefix(value, prefix)
}

func reviewFence(context string) string {
	maxRun, current := 0, 0
	for _, r := range context {
		if r == '`' {
			current++
			if current > maxRun {
				maxRun = current
			}
		} else {
			current = 0
		}
	}
	if maxRun < 3 {
		maxRun = 3
	} else {
		maxRun++
	}
	return strings.Repeat("`", maxRun)
}
