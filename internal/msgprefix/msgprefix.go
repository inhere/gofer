// Package msgprefix is the ONE table of the prefixes gofer puts in front of the
// text it delivers into an agent session (design 2026-10-22-message-prefixes, D1;
// localized forms: design 2026-10-27-i18n-design.md §5 / §9.3):
//
//	[来自 web，<user>] …         a person's new message from the web
//	[from web, <user>] …
//	[来自 web 回复，<user>] …    a person answering a session's question
//	[web reply, <user>] …
//	[gofer · job 完成] …         gofer system messages, one prefix per type
//	[gofer · job done] …
//	[gofer · 退信] …             a letter gofer could not deliver, returned to its sender
//	[gofer · bounce] …
//
// The split is the point: anything opening with a person's prefix was written by a
// person, anything opening with "[gofer · " is machine text gofer generated (a job
// end notice, a wakeup, a work-item request, a memory injection, a hint). Every
// generator and every recogniser uses this package, so the two halves cannot drift
// apart — they did once (a job-end notice wore the web's reply prefix and the agent
// read it as the person talking).
//
// Two languages: gofer GENERATES the prefix in the language of the session it is
// speaking to (its project's language, design §3), and the recognisers accept every
// known spelling, because a session holds messages written under the language it
// had when they were sent (a project's language can change, a worker's copy of a
// message can be older than the server's settings). "[gofer · " is therefore
// language-independent, and only the type inside the bracket differs.
//
// The package depends on internal/i18n only (G022): it is the bottom of the
// delivery stack.
package msgprefix

import (
	"strings"

	"github.com/inhere/gofer/internal/i18n"
)

// Message kinds. One per row of the prefix table.
const (
	// WebMessage is a person's new message from the web.
	WebMessage = "web-message"
	// WebReply is a person answering a question a session asked (a relay turn).
	WebReply = "web-reply"
	// JobDone is a "your job has finished" notice.
	JobDone = "job-done"
	// Wakeup is a wakeup instruction (a continuation that a timer / event started).
	Wakeup = "wakeup"
	// WorkHandoff is a work item's hand-over request.
	WorkHandoff = "work-handoff"
	// WorkReport is a work item's progress-report request.
	WorkReport = "work-report"
	// Steward is the steward relaying a person's words.
	Steward = "steward"
	// Memory is an injected memory (workspace / repository).
	Memory = "memory"
	// Hint is any other gofer hint for the agent (a topic reminder, a generic
	// system notice that names no type of its own).
	Hint = "hint"
	// Bounce is a letter gofer could not deliver, returned to its sender (内部/mail
	// 退信): the body of that letter is a gofer notice, so the recipient reads it as
	// machine text and not as the addressee writing back.
	Bounce = "bounce"
)

// systemOpen is the head every system prefix shares, in every language: it is what
// makes a message "gofer speaking" even when its type is one this build does not
// know.
const systemOpen = "[gofer · "

// DefaultUser is the name used for a person whose name is not known (an anonymous
// web caller, a reply recorded before the speaker was kept).
const DefaultUser = "web"

// personSpec is how one person kind's prefix is written in one language: the
// opening words, the separator before the speaker's name and the closing bracket
// with the one space that separates the prefix from the body.
type personSpec struct {
	open  string
	sep   string
	close string
}

// personPrefixes maps a person kind to its spelling per language. Both halves of
// the separators are part of the table (zh uses a full-width comma, en a comma and
// a space), because the recogniser only needs the OPENING words and the generator
// the whole shape.
var personPrefixes = map[string]map[i18n.Lang]personSpec{
	WebMessage: {
		i18n.ZhCN: {open: "[来自 web", sep: "，", close: "] "},
		i18n.EN:   {open: "[from web", sep: ", ", close: "] "},
	},
	WebReply: {
		i18n.ZhCN: {open: "[来自 web 回复", sep: "，", close: "] "},
		i18n.EN:   {open: "[web reply", sep: ", ", close: "] "},
	},
}

// systemPrefixes maps a system kind to its whole prefix per language.
var systemPrefixes = map[string]map[i18n.Lang]string{
	JobDone: {
		i18n.ZhCN: "[gofer · job 完成]",
		i18n.EN:   "[gofer · job done]",
	},
	Wakeup: {
		i18n.ZhCN: "[gofer · 唤醒]",
		i18n.EN:   "[gofer · wakeup]",
	},
	WorkHandoff: {
		i18n.ZhCN: "[gofer · 工作项交接]",
		i18n.EN:   "[gofer · work handoff]",
	},
	WorkReport: {
		i18n.ZhCN: "[gofer · 工作项汇报]",
		i18n.EN:   "[gofer · work report]",
	},
	Steward: {
		i18n.ZhCN: "[gofer · 管家带话]",
		i18n.EN:   "[gofer · steward relay]",
	},
	Memory: {
		i18n.ZhCN: "[gofer · 记忆]",
		i18n.EN:   "[gofer · memory]",
	},
	Hint: {
		i18n.ZhCN: "[gofer · 提示]",
		i18n.EN:   "[gofer · hint]",
	},
	Bounce: {
		i18n.ZhCN: "[gofer · 退信]",
		i18n.EN:   "[gofer · bounce]",
	},
}

// DEPRECATED(v0.185): remove in v0.188 — the prefixes gofer used before the
// table above. They are still RECOGNISED (sessions and workers hold old messages:
// a transcript has old turns, an older worker may still send one), never generated.
const (
	// legacyWebReply was the person's session-question reply: it opened with
	// "gofer", which is exactly why it had to change.
	legacyWebReply = "[gofer web 回复]"
	// legacyNotice wrapped a system notice: "[gofer 通知] [gofer job 完成] …".
	legacyNotice = "[gofer 通知]"
	// legacyJobDone was the job-end tag.
	legacyJobDone = "[gofer job 完成]"
	// legacyGofer is the head every old system prefix shared ("[gofer …]").
	legacyGofer = "[gofer"
)

// kindsByPrefix is the reverse of the current table: every known spelling (both
// languages) to its kind, so a recogniser reads a message written under either
// language.
var kindsByPrefix = func() map[string]string {
	out := make(map[string]string, 0)
	for kind, byLang := range systemPrefixes {
		for _, p := range byLang {
			out[p] = kind
		}
	}
	return out
}()

// The default-language spelling of each person prefix and system kind. They are
// CONSTANTS because call sites declare their own constants from them (a package's
// "already carries it" marker); TestDefaultSpellingsMatchTheTable keeps them equal
// to the table's default-language entry, so the table stays the one source.
//
// A generator that knows its recipient's language uses Open / PrefixIn / FormatIn
// instead; these are for recognisers and for a generator whose recipient is
// unknown (the default language is still written).
const (
	// WebMessageOpen is the head of a person's web message prefix.
	WebMessageOpen = "[来自 web"
	// WebReplyOpen is the head of a person's session-question reply prefix. It is
	// checked before WebMessageOpen, which it shares its first characters with.
	WebReplyOpen = "[来自 web 回复"

	// JobDoneOpen opens a job-end notice.
	JobDoneOpen = "[gofer · job 完成]"
	// WakeupOpen opens a wakeup instruction.
	WakeupOpen = "[gofer · 唤醒]"
	// WorkHandoffOpen opens a work item's hand-over request.
	WorkHandoffOpen = "[gofer · 工作项交接]"
	// WorkReportOpen opens a work item's report request.
	WorkReportOpen = "[gofer · 工作项汇报]"
	// StewardOpen opens a message the steward relays for a person.
	StewardOpen = "[gofer · 管家带话]"
	// MemoryOpen opens an injected memory block line.
	MemoryOpen = "[gofer · 记忆]"
	// HintOpen opens a gofer hint (topic reminder, generic notice).
	HintOpen = "[gofer · 提示]"
	// BounceOpen opens a returned letter (退信).
	BounceOpen = "[gofer · 退信]"
)

// Open returns the whole prefix of a SYSTEM kind in lang — the text gofer writes
// ("[gofer · job done]"), without the separating space. "" for an unknown kind or
// for a person's kind (a person's prefix carries a name: use PrefixIn).
func Open(lang i18n.Lang, kind string) string {
	return systemPrefixes[kind][i18n.Resolve(lang)]
}

// personOpen returns kind's opening words in lang ("[来自 web" / "[from web"): what
// a recogniser compares and what a generator puts before the speaker's name.
func personOpen(lang i18n.Lang, kind string) string {
	spec, ok := personPrefixes[kind][i18n.Resolve(lang)]
	if !ok {
		return ""
	}
	return spec.open
}

// PersonOpens returns every known spelling of a person kind's opening words, all
// languages ("[来自 web", "[from web"): a recogniser must accept both, since the
// session may hold a message written under another language.
func PersonOpens(kind string) []string {
	var out []string
	for _, lang := range i18n.Supported() {
		if p := personPrefixes[kind][lang].open; p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Opens returns every known spelling of a system kind, all languages. A recogniser
// (a "does this prompt already carry it" check, a prefix comparison) must accept
// all of them: the session may hold the text of the language it had when written.
func Opens(kind string) []string {
	var out []string
	for _, lang := range i18n.Supported() {
		if p := systemPrefixes[kind][lang]; p != "" {
			out = append(out, p)
		}
	}
	return out
}

// SystemPrefixes returns the whole prefix of every system kind in every language
// (documentation, tests and the outward-facing 说明 of the table).
func SystemPrefixes() []string {
	var out []string
	for _, kind := range systemKinds() {
		out = append(out, Opens(kind)...)
	}
	return out
}

// systemKinds lists the system kinds in documentation order.
func systemKinds() []string {
	return []string{JobDone, Wakeup, WorkHandoff, WorkReport, Steward, Memory, Hint, Bounce}
}

// Person reports whether kind is a person's message kind (the other kinds are
// gofer's own).
func Person(kind string) bool {
	return kind == WebMessage || kind == WebReply
}

// PrefixIn returns the prefix of kind in lang, with the one space that separates it
// from the body, or "" for an unknown kind. user is the person's name for a
// person's kind and ignored otherwise; an empty name falls back to DefaultUser (a
// person's message always carries a name, so the two halves stay distinguishable).
func PrefixIn(lang i18n.Lang, kind, user string) string {
	if spec, ok := personPrefixes[kind]; ok {
		return personSpecOf(spec, lang, user)
	}
	if p := Open(lang, kind); p != "" {
		return p + " "
	}
	return ""
}

// personSpecOf renders one person prefix in lang, falling back to the default
// language for a language the table does not carry.
func personSpecOf(spec map[i18n.Lang]personSpec, lang i18n.Lang, user string) string {
	s, ok := spec[i18n.Resolve(lang)]
	if !ok {
		s = spec[i18n.Default]
	}
	user = strings.TrimSpace(user)
	if user == "" {
		user = DefaultUser
	}
	return s.open + s.sep + user + s.close
}

// FormatIn renders one delivered message in lang: the prefix of kind, then body.
func FormatIn(lang i18n.Lang, kind, user, body string) string {
	return PrefixIn(lang, kind, user) + body
}

// Prefix is PrefixIn in the DEFAULT language — the form for a generator that does
// not know its recipient's language (a hook process, a caller whose text travels to
// a session it never read). A caller that knows the recipient's project uses
// PrefixIn; the two render the same table, so they cannot drift apart.
//
// DEPRECATED(v0.198): a generator that learns its recipient's language switches to
// PrefixIn; remove this form once every generator does (design 2026-10-27-i18n §6).
func Prefix(kind, user string) string {
	return PrefixIn(i18n.Default, kind, user)
}

// Format is FormatIn in the DEFAULT language. See Prefix.
//
// DEPRECATED(v0.198): see Prefix.
func Format(kind, user, body string) string {
	return FormatIn(i18n.Default, kind, user, body)
}

// Classify reads the prefix of a delivered text. kind is the message kind ("" when
// the text carries no prefix this package knows), fromPerson tells the two halves
// apart: true = a person wrote it, false = gofer generated it. Unknown text
// answers ("", false), so a plain human prompt is never mistaken for a notice.
//
// Both languages are recognised: this is a read path, and the text may have been
// written under any language the deployment ever had.
func Classify(text string) (kind string, fromPerson bool) {
	k, person, _ := classify(text)
	return k, person
}

// IsSystem reports whether text is a gofer system message (both languages, and —
// until v0.188 — the old prefixes).
func IsSystem(text string) bool {
	_, person, gofer := classify(text)
	return gofer && !person
}

// IsPerson reports whether text was written by a person (both languages' web
// prefixes and — until v0.188 — the old "[gofer web 回复]").
func IsPerson(text string) bool {
	_, person, _ := classify(text)
	return person
}

// classify is the single parser behind Classify / IsSystem / IsPerson. gofer
// reports whether the text carries ANY prefix of this package (a system one or a
// person one) — the test of "this text is gofer-delivered, not a bare prompt".
func classify(text string) (kind string, fromPerson, gofer bool) {
	t := strings.TrimSpace(text)
	switch {
	case strings.HasPrefix(t, systemOpen):
		return systemKind(t), false, true
	case hasPersonOpen(t, WebReply):
		return WebReply, true, true
	case hasPersonOpen(t, WebMessage):
		return WebMessage, true, true
	// DEPRECATED(v0.185): remove in v0.188 (see the constants above).
	case strings.HasPrefix(t, legacyWebReply):
		return WebReply, true, true
	case strings.HasPrefix(t, legacyGofer):
		return legacySystemKind(t), false, true
	}
	return "", false, false
}

// hasPersonOpen reports whether t opens with kind's person prefix in any language.
// The opening words are enough (the speaker's name and the bracket follow), and
// the reply is checked before the message because its zh spelling shares the
// message's first characters.
func hasPersonOpen(t string, kind string) bool {
	for _, spec := range personPrefixes[kind] {
		if strings.HasPrefix(t, spec.open) {
			return true
		}
	}
	return false
}

// systemKind reads the type out of "[gofer · <type>]". A type this build does not
// know answers "": the text is still system (systemOpen said so), just not one of
// the known kinds.
func systemKind(t string) string {
	rest := t[len(systemOpen):]
	i := strings.Index(rest, "]")
	if i < 0 {
		return ""
	}
	return kindsByPrefix[systemOpen+rest[:i]+"]"]
}

// legacySystemKind classifies a pre-table system message: the old wrappers, the old
// per-type words, and the bare "[gofer] …" hint.
// DEPRECATED(v0.185): remove in v0.188 together with the legacy constants.
func legacySystemKind(t string) string {
	switch {
	case strings.Contains(t, legacyJobDone):
		// The old job-end notice came both alone and wrapped ("[gofer 通知] [gofer
		// job 完成] …"): one kind, one notice.
		return JobDone
	case strings.HasPrefix(t, legacyGofer+" wakeup]"):
		return Wakeup
	case strings.HasPrefix(t, legacyGofer+" 工作项交接请求"):
		return WorkHandoff
	case strings.HasPrefix(t, legacyGofer+" 工作项汇报请求"):
		return WorkReport
	case strings.HasPrefix(t, legacyGofer+" 管家带话]"):
		return Steward
	case strings.HasPrefix(t, legacyGofer+" 记忆"):
		return Memory
	}
	return Hint
}
