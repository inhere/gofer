package job

import (
	"regexp"
	"strings"
)

// FindingsSectionTitle is the report section an agent lists what it found outside its
// scope but did not touch (gofer-3nxa.3, the 「交付约定」 section asks for it).
const FindingsSectionTitle = "发现但不碰"

var (
	findingsHeadingRE = regexp.MustCompile(`^\s{0,3}(#{1,6})\s+(.*?)\s*#*\s*$`)
	findingsItemRE    = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s+(?:\[[ xX]\]\s+)?(.*)$`)
	findingsFenceRE   = regexp.MustCompile("^\\s{0,3}(```|~~~)")
)

// findingsTitleAliases are the other headings accepted for the findings section.
var findingsTitleAliases = []string{"Out of scope", "Out-of-scope findings"}

// ParseFindings extracts the list items of the LAST 「发现但不碰」 section of an agent's
// markdown report (also accepted: "Out of scope" / "Out-of-scope findings", any case,
// any heading level) — see ParseReportSection. The web console has the same parser in
// web/src/utils/findings.ts — their tests share cases.
func ParseFindings(report string) []string {
	return ParseReportSection(report, append([]string{FindingsSectionTitle}, findingsTitleAliases...)...)
}

// ParseReportSection extracts the list items of the LAST section of an agent's markdown
// report whose heading names one of titles (gofer-3nxa.3 「发现但不碰」, gofer-3nxa.2
// 「可复用经验」). A heading matches after dropping emphasis / quote marks and a trailing
// colon, either exactly or case-insensitively with '-' read as a space; any heading
// level counts. The section runs to the next heading of the same or a higher level; an
// item's indented continuation lines are joined to it with a space. Headings inside
// fenced code blocks are ignored, and a later section replaces an earlier one (the
// report's end is the answer).
func ParseReportSection(report string, titles ...string) []string {
	var (
		items   []string
		inside  bool
		level   int
		inFence bool
	)
	for _, raw := range strings.Split(report, "\n") {
		line := strings.TrimRight(raw, " \t\r")
		if findingsFenceRE.MatchString(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if m := findingsHeadingRE.FindStringSubmatch(line); m != nil {
			if isSectionTitle(m[2], titles) {
				items, inside, level = nil, true, len(m[1])
				continue
			}
			if inside && len(m[1]) <= level {
				inside = false
			}
			continue
		}
		if !inside || strings.TrimSpace(line) == "" {
			continue
		}
		if m := findingsItemRE.FindStringSubmatch(line); m != nil {
			if text := strings.TrimSpace(m[1]); text != "" {
				items = append(items, text)
			}
			continue
		}
		if len(items) > 0 && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) {
			items[len(items)-1] += " " + strings.TrimSpace(line)
		}
	}
	return items
}

// isSectionTitle reports whether a heading's text names one of titles, after dropping
// emphasis/quote marks and a trailing colon.
func isSectionTitle(heading string, titles []string) bool {
	t := strings.TrimRight(strings.TrimSpace(heading), ":： ")
	t = strings.TrimSpace(strings.Trim(t, "*_`「」\"'"))
	t = strings.TrimRight(t, ":： ")
	for _, title := range titles {
		if t == title || normalizeSectionTitle(t) == normalizeSectionTitle(title) {
			return true
		}
	}
	return false
}

// normalizeSectionTitle lower-cases a title, reads '-' as a space and collapses runs of
// whitespace ("Out-of-Scope  Findings" == "out of scope findings").
func normalizeSectionTitle(t string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(strings.ToLower(t), "-", " ")), " ")
}
