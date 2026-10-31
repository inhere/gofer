// Package i18n is gofer's language model: the canonical language tags, the ONE
// alias normalizer every entry point uses, where a text's language comes from,
// and the two text catalogs — short strings (gookit/i18n INI files under lang/)
// and long prompt templates (the prompts subpackage).
//
// Design: docs/design/2026-10-27-i18n-design.md §3 / §4.
package i18n

import (
	"sort"
	"strconv"
	"strings"
)

// Lang is a canonical language tag. Only these values travel: every input
// (config values, GOFER_LANG, LANG / LC_ALL, Accept-Language) goes through
// Normalize first, so a stored or transmitted value is always canonical.
type Lang string

const (
	// EN is English.
	EN Lang = "en"
	// ZhCN is Simplified Chinese.
	ZhCN Lang = "zh-CN"
	// Default is the language of code that carries no language at all: the
	// language-less compatibility wrappers (the XxxIn(lang, ...) functions' short
	// forms), Resolve with nothing understood, and tests that pin one text.
	// It is NOT what an unconfigured deployment gets: a language nobody named
	// reads as AutoValue, resolved by Auto() (the server machine's language, else
	// en) — design 2026-10-27-i18n-design.md, "Decided (2026-10-30): default auto".
	Default = ZhCN
)

// AutoValue is the config value that asks for the machine's own language
// (server.language / projects.<p>.language). An unset language reads as it too.
const AutoValue = "auto"

// IsAuto reports whether a configured language value means "pick it from the
// machine": the explicit "auto" (any case) or nothing at all.
func IsAuto(in string) bool {
	s := strings.ToLower(strings.TrimSpace(in))
	return s == "" || s == AutoValue
}

// detectSystem reads the machine's language preferences; tests replace it.
var detectSystem = systemLanguages

// Auto is the language an unconfigured (or "auto") deployment runs in: the
// machine's language when it is one gofer has text for, else English.
func Auto() Lang {
	if l := FromSystem(); l != "" {
		return l
	}
	return EN
}

// supported lists the canonical languages in documentation order.
var supported = []Lang{EN, ZhCN}

// Supported returns the canonical languages, in documentation order.
func Supported() []Lang {
	out := make([]Lang, 0, len(supported))
	return append(out, supported...)
}

// List renders the supported languages for an error message ("en, zh-CN").
func List() string {
	names := make([]string, 0, len(supported))
	for _, l := range supported {
		names = append(names, string(l))
	}
	return strings.Join(names, ", ")
}

// Aliases maps an accepted spelling to its canonical language. A key is what
// Normalize's key form of an input looks like: lower-cased, '_' read as '-',
// with the encoding (.UTF-8) and modifier (@euro) dropped. Every alias table
// entry is covered by the table test.
var Aliases = map[string]Lang{
	// en
	"en":      EN,
	"en-us":   EN,
	"en-gb":   EN,
	"english": EN,
	"eng":     EN,
	// A C / POSIX locale has no language of its own; its text is English.
	"c":     EN,
	"posix": EN,

	// zh-CN (the only Chinese catalog: zh-TW / zh-HK / zh-Hant read as zh-CN,
	// they get the Simplified text — see the design's implementation record)
	"zh":         ZhCN,
	"zh-cn":      ZhCN,
	"zh-hans":    ZhCN,
	"zh-hans-cn": ZhCN,
	"zh-sg":      ZhCN,
	"zh-tw":      ZhCN,
	"zh-hk":      ZhCN,
	"zh-hant":    ZhCN,
	"cn":         ZhCN,
	"chinese":    ZhCN,
	"中文":         ZhCN,
	"简体中文":       ZhCN,
}

// Normalize is the ONE place a language name is read: it maps any accepted
// spelling to its canonical tag and reports whether it was understood.
// Matching is case-insensitive, '-' and '_' are equivalent, and the encoding /
// modifier suffix (.UTF-8, @euro) is ignored. A regional tag of a known base
// language (en-AU, zh-MO) reads as that base language.
func Normalize(in string) (Lang, bool) {
	key := normalizeKey(in)
	if key == "" {
		return "", false
	}
	if l, ok := Aliases[key]; ok {
		return l, true
	}
	if i := strings.IndexByte(key, '-'); i > 0 {
		if l, ok := Aliases[key[:i]]; ok {
			return l, true
		}
	}
	return "", false
}

// normalizeKey is the form the alias table is keyed by.
func normalizeKey(in string) string {
	s := strings.ToLower(strings.TrimSpace(in))
	if i := strings.IndexAny(s, ".@"); i >= 0 {
		s = s[:i]
	}
	s = strings.ReplaceAll(s, "_", "-")
	return strings.TrimSpace(s)
}

// MustNormalize is Normalize for a value that must be understood (a catalog
// constant, a test): it returns Default when in is not recognized.
func MustNormalize(in string) Lang {
	if l, ok := Normalize(in); ok {
		return l
	}
	return Default
}

// Resolve picks the language of a text from its sources, highest priority
// first: the first understood candidate wins, and an empty list (or one with
// nothing understood) is Default (a caller with no language at all; an
// unconfigured deployment gets Config.Language(), which is auto). Callers pass the chain of §3 of the design —
// e.g. a job: the project's language, then the server's.
func Resolve(candidates ...Lang) Lang {
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if l, ok := Normalize(string(c)); ok {
			return l
		}
	}
	return Default
}

// FromAcceptLanguage reads the first understood language of an Accept-Language
// header, by the client's q order (a header without q values keeps its order).
// It returns "" when nothing is understood, so the caller falls back to its
// next source; "*" is skipped, since it names no language.
func FromAcceptLanguage(header string) Lang {
	type cand struct {
		lang Lang
		q    float64
		pos  int
	}
	var cands []cand
	for pos, part := range strings.Split(header, ",") {
		tag, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if tag == "" || tag == "*" {
			continue
		}
		q := 1.0
		for _, p := range strings.Split(params, ";") {
			k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
			if !ok || strings.ToLower(strings.TrimSpace(k)) != "q" {
				continue
			}
			if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				q = f
			}
		}
		l, ok := Normalize(tag)
		if !ok {
			continue
		}
		cands = append(cands, cand{lang: l, q: q, pos: pos})
	}
	if len(cands) == 0 {
		return ""
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].q > cands[j].q })
	return cands[0].lang
}

// LanguageDefaultNote explains what an unset language means: it is what the
// startup log and `config validate` say. LanguageDefaultNoteIn renders it.
const LanguageDefaultNote = "server.language is not set: the language is chosen automatically " +
	"from the server machine's system language (en when it cannot be detected) — " +
	"set server.language explicitly to pin it (available: auto, en, zh-CN)"

// LanguageDefaultNoteIn is LanguageDefaultNote in lang.
func LanguageDefaultNoteIn(lang Lang) string { return T(lang, "cli.setup.language.unset_note") }
