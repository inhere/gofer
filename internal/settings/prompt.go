package settings

import "strings"

// Prompt overrides (design 2026-10-20 §「提示词的覆盖方式」): every prompt has two keys — the
// plain key replaces the whole instruction the code would send, and its `.extra` key appends
// the operator's own words to whichever instruction is in effect (the default one, so an
// extra keeps following the default's updates, or a replacement). The two helpers below are
// the single composition rule both consumers (the summarizer, the steward) use.

// ExtraHeading separates the instruction (the code default or the replacement) from the
// operator's extra text, so the model can tell whose words are whose.
const ExtraHeading = "## 使用者补充"

// ChoosePrompt returns replacement when it holds anything but whitespace, else def. The
// replacement is kept verbatim (an empty key means "use the default").
func ChoosePrompt(def, replacement string) string {
	if strings.TrimSpace(replacement) != "" {
		return replacement
	}
	return def
}

// ComposePrompt appends extra to body under ExtraHeading. A blank extra returns body
// unchanged, so a default prompt is byte-for-byte what the code wrote.
func ComposePrompt(body, extra string) string {
	extra = strings.TrimSpace(extra)
	if extra == "" {
		return body
	}
	return strings.TrimRight(body, "\n") + "\n\n" + ExtraHeading + "\n" + extra
}
