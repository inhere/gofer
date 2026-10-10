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

// ParseFindings extracts the list items of the LAST 「发现但不碰」 section of an agent's
// markdown report (also accepted: "Out of scope" / "Out-of-scope findings", any case,
// any heading level). The section runs to the next heading of the same or a higher
// level; an item's indented continuation lines are joined to it with a space. Headings
// inside fenced code blocks are ignored. The web console has the same parser in
// web/src/utils/findings.ts — their tests share cases.
func ParseFindings(report string) []string {
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
			if isFindingsTitle(m[2]) {
				// A later section replaces an earlier one: the report's end is the answer.
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

// isFindingsTitle reports whether a heading's text names the findings section, after
// dropping emphasis/quote marks and a trailing colon.
func isFindingsTitle(title string) bool {
	t := strings.TrimRight(strings.TrimSpace(title), ":： ")
	t = strings.TrimSpace(strings.Trim(t, "*_`「」\"'"))
	t = strings.TrimRight(t, ":： ")
	if t == FindingsSectionTitle {
		return true
	}
	t = strings.Join(strings.Fields(strings.ReplaceAll(strings.ToLower(t), "-", " ")), " ")
	return t == "out of scope" || t == "out of scope findings"
}
