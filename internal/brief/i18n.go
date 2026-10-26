package brief

// i18n.go is the ONE place a brief gets its wording (design 2026-10-27-i18n-design.md
// §4 / P1f): the text lives in internal/i18n/lang/{en,zh-CN}/brief.ini, and every shape
// of a brief — the issue brief, the plan brief, the prime's plan lines, the
// post-compact refocus section — takes its language from one caller input
// (Options.Language, PrimePlanLinesIn, RenderRefocusIn).

import "github.com/inhere/gofer/internal/i18n"

// briefText looks key up in lang. It is the one wrapper of the catalog: an unknown key
// returns the key itself and a key missing in lang falls back to English (the key-set
// test keeps both catalogs equal).
func briefText(lang i18n.Lang, key string, args ...any) string {
	return i18n.T(i18n.Resolve(lang), key, args...)
}
