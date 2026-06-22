package commands

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	yaml "github.com/goccy/go-yaml"

	"github.com/inhere/gofer/internal/job"
)

// maxStepMarkdownBytes caps a single md-per-step file (frontmatter + body) so a
// pathological file can not be slurped whole (mirrors httpapi.maxMarkdownBytes).
const maxStepMarkdownBytes = 256 * 1024

// expandStepMarkdown expands a step's optional md-per-step `file:` reference in place
// (T4.2). When step.File is set, the named md file (resolved relative to baseDir — the
// workflow file's directory) is read and parsed as "yaml frontmatter + markdown body":
// the frontmatter fills the step's params and the body becomes its prompt. Fields set
// INLINE in the workflow yaml take precedence over the md frontmatter (the inline step is
// the override layer), and an inline prompt (rare alongside a file) wins over the md body.
// A step with no File is left untouched (the common/v1 path). stepNo is 1-based, for
// error messages.
func expandStepMarkdown(step *job.StepSpec, baseDir string, stepNo int) error {
	if step.File == "" {
		return nil // no md-per-step reference: leave the inline step as-is (v1 path)
	}
	mdPath := step.File
	if !filepath.IsAbs(mdPath) {
		mdPath = filepath.Join(baseDir, mdPath)
	}
	body, err := os.ReadFile(mdPath)
	if err != nil {
		return fmt.Errorf("step %d: read md file %q: %w", stepNo, step.File, err)
	}
	if len(body) > maxStepMarkdownBytes {
		return fmt.Errorf("step %d: md file %q exceeds %d bytes", stepNo, step.File, maxStepMarkdownBytes)
	}
	fromMD, prompt, err := parseStepMarkdown(body)
	if err != nil {
		return fmt.Errorf("step %d: parse md file %q: %w", stepNo, step.File, err)
	}
	mergeStepFromMarkdown(step, fromMD, prompt)
	step.File = "" // consumed: never crosses the wire (already json:"-", cleared for clarity)
	return nil
}

// parseStepMarkdown parses an md-per-step document ("--- yaml frontmatter --- + body")
// into the frontmatter StepSpec and the trimmed body (the prompt). It reuses the same
// frontmatter shape as the E14 single-job md submit (a leading '---' block, then prose).
// A missing frontmatter block is an error (the md-per-step contract requires it so step
// params are explicit). The frontmatter binds via the StepSpec yaml tags.
func parseStepMarkdown(body []byte) (job.StepSpec, string, error) {
	var fmStep job.StepSpec
	fm, rest, ok := splitWorkflowFrontmatter(body)
	if !ok {
		return fmStep, "", fmt.Errorf("missing yaml frontmatter (expected leading '---' block)")
	}
	if len(fm) > 0 {
		if err := yaml.Unmarshal(fm, &fmStep); err != nil {
			return fmStep, "", fmt.Errorf("invalid frontmatter yaml: %w", err)
		}
	}
	return fmStep, strings.TrimSpace(string(rest)), nil
}

// mergeStepFromMarkdown overlays an md-per-step's frontmatter (fromMD) + body (prompt)
// onto the inline step, with the INLINE step winning every field it set (the inline yaml
// is the override layer over the shared md template). For each field, the md value is used
// only when the inline step left it at its zero value. The md body fills Prompt only when
// the merged step has no inline prompt.
func mergeStepFromMarkdown(step *job.StepSpec, fromMD job.StepSpec, prompt string) {
	if step.Name == "" {
		step.Name = fromMD.Name
	}
	if step.ProjectKey == "" {
		step.ProjectKey = fromMD.ProjectKey
	}
	if step.Agent == "" {
		step.Agent = fromMD.Agent
	}
	if step.Runner == "" {
		step.Runner = fromMD.Runner
	}
	if step.Cwd == "" {
		step.Cwd = fromMD.Cwd
	}
	if step.TimeoutSec == 0 {
		step.TimeoutSec = fromMD.TimeoutSec
	}
	if len(step.Cmd) == 0 {
		step.Cmd = fromMD.Cmd
	}
	if len(step.Tags) == 0 {
		step.Tags = fromMD.Tags
	}
	if step.OnFailure == "" {
		step.OnFailure = fromMD.OnFailure
	}
	if step.Retry == nil {
		step.Retry = fromMD.Retry
	}
	if step.FanOut == 0 {
		step.FanOut = fromMD.FanOut
	}
	if step.Join == "" {
		step.Join = fromMD.Join
	}
	if step.Type == "" {
		step.Type = fromMD.Type
	}
	if step.SubWorkflow == nil {
		step.SubWorkflow = fromMD.SubWorkflow
	}
	// The md body is the step prompt; an inline prompt (set in the workflow yaml) wins,
	// then the md frontmatter prompt, then the md body.
	if step.Prompt == "" {
		step.Prompt = fromMD.Prompt
	}
	if step.Prompt == "" {
		step.Prompt = prompt
	}
}

// splitWorkflowFrontmatter separates a leading '---' yaml block from the markdown body.
// It mirrors httpapi.splitFrontmatter (kept local so the commands package has no httpapi
// dependency): tolerant of leading whitespace and \r\n; ok=false when there is no opening
// '---' or no closing '---' line.
func splitWorkflowFrontmatter(body []byte) (fm, rest []byte, ok bool) {
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
