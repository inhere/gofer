package config

import (
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// Tool is one external tool a runner declares as installed on its machine
// (CFG-16): ffmpeg, docker, an internal CLI... The list is DESCRIPTIVE only — it is
// rendered into the prompt of an agent job running on that machine and shown on
// the runners views; it never changes PATH, never calls the tool, and is not a
// scheduling constraint (labels / projects are). The same shape lives at the top
// level of config.yaml (the server-local runner) and of worker.yaml (that worker).
type Tool struct {
	// Name is the name the agent sees; required, unique case-insensitively.
	Name string `yaml:"name" json:"name"`
	// Desc is a one-line purpose / caveat; optional.
	Desc string `yaml:"desc,omitempty" json:"desc,omitempty"`
	// Bin is the executable name or absolute path; optional, defaults to Name. It is
	// only shown to the agent and checked by validate / doctor.
	Bin string `yaml:"bin,omitempty" json:"bin,omitempty"`
}

// Limits on a tools declaration. They keep the rendered prompt section small: a
// runner's tool list is prepended to every agent job it runs.
const (
	ToolsMaxCount       = 50
	ToolNameMaxLen      = 64
	ToolDescMaxRunes    = 200
	ToolsListMaxBytes   = 4 << 10
	toolsLargestShown   = 3
	toolNamePatternText = "[A-Za-z0-9._-]"
)

var toolNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// EffectiveBin is the executable the tool resolves to: Bin, else Name.
func (t Tool) EffectiveBin() string {
	if b := strings.TrimSpace(t.Bin); b != "" {
		return b
	}
	return t.Name
}

// ToolLine renders one tool as the prompt list line, e.g.
//
//   - `ffmpeg` — transcoder
//   - `plc-sim`（`D:\tools\plcsim.exe`）— simulator
//
// It is the single renderer of a line, so the size check in ValidateTools and the
// prompt the job carries measure the same text.
func ToolLine(t Tool) string {
	var b strings.Builder
	b.WriteString("- `")
	b.WriteString(t.Name)
	b.WriteString("`")
	sep := " — "
	if bin := strings.TrimSpace(t.Bin); bin != "" && bin != t.Name {
		b.WriteString("（`")
		b.WriteString(bin)
		b.WriteString("`）")
		sep = "— " // the full-width parenthesis already spaces it
	}
	if d := strings.TrimSpace(t.Desc); d != "" {
		b.WriteString(sep)
		b.WriteString(d)
	}
	return b.String()
}

// ValidateTools checks a tools declaration: name required / unique / charset /
// length, desc length, count, and the total size of the rendered list. An empty
// list is valid (the runner declares nothing). Errors name the yaml key (`tools`),
// which is the same in config.yaml and worker.yaml.
func ValidateTools(tools []Tool) error {
	if len(tools) > ToolsMaxCount {
		return fmt.Errorf("tools: %d entries, at most %d", len(tools), ToolsMaxCount)
	}
	seen := make(map[string]int, len(tools))
	total := 0
	for i, t := range tools {
		switch {
		case t.Name == "":
			return fmt.Errorf("tools[%d]: name is required", i)
		case len(t.Name) > ToolNameMaxLen:
			return fmt.Errorf("tools[%d]: name %q is longer than %d", i, t.Name, ToolNameMaxLen)
		case !toolNamePattern.MatchString(t.Name):
			return fmt.Errorf("tools[%d]: name %q may only contain %s", i, t.Name, toolNamePatternText)
		}
		key := strings.ToLower(t.Name)
		if j, dup := seen[key]; dup {
			return fmt.Errorf("tools[%d]: name %q duplicates [%d] (names are case-insensitive)", i, t.Name, j)
		}
		seen[key] = i
		if n := utf8.RuneCountInString(strings.TrimSpace(t.Desc)); n > ToolDescMaxRunes {
			return fmt.Errorf("tools[%d] %s: desc is %d characters, at most %d", i, t.Name, n, ToolDescMaxRunes)
		}
		total += len(ToolLine(t)) + 1
	}
	if total > ToolsListMaxBytes {
		return fmt.Errorf("tools: rendered list is %d bytes, over %d; largest: %s", total, ToolsListMaxBytes, largestTools(tools))
	}
	return nil
}

// largestTools names the longest rendered entries, largest first — the operator
// needs to know which desc to shorten, not just that the total was too long.
func largestTools(tools []Tool) string {
	type entry struct {
		name string
		size int
	}
	all := make([]entry, 0, len(tools))
	for _, t := range tools {
		all = append(all, entry{name: t.Name, size: len(ToolLine(t))})
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].size > all[j].size })
	if len(all) > toolsLargestShown {
		all = all[:toolsLargestShown]
	}
	parts := make([]string, 0, len(all))
	for _, e := range all {
		parts = append(parts, fmt.Sprintf("%s (%d bytes)", e.name, e.size))
	}
	return strings.Join(parts, ", ")
}

// MissingToolBins returns the declared tools whose executable this machine cannot
// find (exec.LookPath), as "name (bin)" strings. It backs the validate / doctor
// WARNING — a declaration may describe another environment, so a miss never fails.
func MissingToolBins(tools []Tool) []string {
	var out []string
	for _, t := range tools {
		bin := t.EffectiveBin()
		if _, err := exec.LookPath(bin); err != nil {
			out = append(out, fmt.Sprintf("%s (%s)", t.Name, bin))
		}
	}
	return out
}
