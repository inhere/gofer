package i18n

// Protocol text: the strings gofer writes into an agent prompt and reads back.
//
// They cannot simply be translated — the same heading or prefix is a marker
// ("this section is already in the prompt") or a parser input (the report's
// findings / lessons sections) — so every known localized spelling is
// recognized, and this table is the ONE place they are listed. Design
// 2026-10-27-i18n-design.md §5.

// Section is a protocol section of an agent prompt or report: a heading (or
// line prefix) whose exact text gofer parses back.
type Section string

const (
	// SectionAcceptance is the heading of the acceptance-criteria section.
	SectionAcceptance Section = "acceptance"
	// SectionContract is the heading of the delivery-contract section
	// (scope discipline / knowledge capture / memory flag).
	SectionContract Section = "contract"
	// SectionFindings is the report section listing what the agent found
	// outside its scope but did not touch.
	SectionFindings Section = "findings"
	// SectionKnowledge is the report section listing reusable lessons.
	SectionKnowledge Section = "knowledge"
	// SectionDeadline is the opening of the time-limit line appended to a
	// batch job's prompt; it also marks the line as already present.
	SectionDeadline Section = "deadline"
)

// sectionTitles are the known spellings of each section: the localized heading
// gofer writes today, plus the older spellings a prompt or report may still
// carry (an already-injected prompt, a peer re-submit, a report written before
// the language changed).
var sectionTitles = map[Section]map[Lang]string{
	SectionAcceptance: {EN: "## Acceptance criteria", ZhCN: "## 验收标准"},
	SectionContract:   {EN: "## Delivery contract", ZhCN: "## 交付约定"},
	SectionFindings:   {EN: "Out-of-scope findings", ZhCN: "发现但不碰"},
	SectionKnowledge:  {EN: "Reusable lessons", ZhCN: "可复用经验"},
	SectionDeadline:   {EN: "This job's time limit", ZhCN: "本 job 时限"},
}

// sectionAliases are further accepted spellings that are not the current text
// of any language.
var sectionAliases = map[Section][]string{
	// The English findings heading was introduced by the report parser before
	// the prompt had an English text.
	SectionFindings: {"Out of scope"},
}

// Title returns the current text of section in lang — the heading gofer writes,
// and the marker it looks for.
func Title(lang Lang, section Section) string {
	titles := sectionTitles[section]
	if t := titles[Resolve(lang)]; t != "" {
		return t
	}
	return titles[Default]
}

// Titles returns every known spelling of section, the current ones first. A
// parser or an "already present" check must accept all of them.
func Titles(section Section) []string {
	var out []string
	for _, lang := range supported {
		if t := sectionTitles[section][lang]; t != "" {
			out = append(out, t)
		}
	}
	return append(out, sectionAliases[section]...)
}
