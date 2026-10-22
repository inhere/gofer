// Package msgprefix is the ONE table of the prefixes gofer puts in front of the
// text it delivers into an agent session (design 2026-10-22-message-prefixes, D1):
//
//	[来自 web，<user>] …         a person's new message from the web
//	[来自 web 回复，<user>] …    a person answering a session's question
//	[gofer · job 完成] …         gofer system messages, one prefix per type
//
// The split is the point: anything opening with "[来自 web" was written by a
// person, anything opening with "[gofer · " is machine text gofer generated (a job
// end notice, a wakeup, a work-item request, a memory injection, a hint). Every
// generator and every recogniser uses this package, so the two halves cannot drift
// apart — they did once (a job-end notice wore the web's reply prefix and the agent
// read it as the person talking).
//
// The package depends on nothing else in internal/ (G022): it is the bottom of the
// delivery stack.
package msgprefix

import "strings"

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
)

// A person's prefix opens with these words; the person's name and a closing
// bracket follow ("[来自 web，alice] ").
const (
	// WebMessageOpen is the head of a person's web message prefix.
	WebMessageOpen = "[来自 web"
	// WebReplyOpen is the head of a person's session-question reply prefix. It is
	// checked before WebMessageOpen, which it shares its first characters with.
	WebReplyOpen = "[来自 web 回复"
)

// A gofer system prefix is "[gofer · <type>]" with one space after the bracket.
// These are whole prefixes (no part of them varies), so a generator can use them
// as constants and a recogniser can compare what it reads.
const (
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
)

// systemOpen is the head every system prefix shares: it is what makes a message
// "gofer speaking" even when its type is one this build does not know.
const systemOpen = "[gofer · "

// DefaultUser is the name used for a person whose name is not known (an anonymous
// web caller, a reply recorded before the speaker was kept).
const DefaultUser = "web"

// systemPrefixes maps a system kind to its whole prefix.
var systemPrefixes = map[string]string{
	JobDone:     JobDoneOpen,
	Wakeup:      WakeupOpen,
	WorkHandoff: WorkHandoffOpen,
	WorkReport:  WorkReportOpen,
	Steward:     StewardOpen,
	Memory:      MemoryOpen,
	Hint:        HintOpen,
}

// kindsByPrefix is the reverse of systemPrefixes (a recogniser reads a prefix).
var kindsByPrefix = map[string]string{
	JobDoneOpen:     JobDone,
	WakeupOpen:      Wakeup,
	WorkHandoffOpen: WorkHandoff,
	WorkReportOpen:  WorkReport,
	StewardOpen:     Steward,
	MemoryOpen:      Memory,
	HintOpen:        Hint,
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

// SystemPrefixes returns the whole prefix of every system kind (documentation,
// tests and the outward-facing说明 of the table).
func SystemPrefixes() []string {
	return []string{JobDoneOpen, WakeupOpen, WorkHandoffOpen, WorkReportOpen, StewardOpen, MemoryOpen, HintOpen}
}

// Person reports whether kind is a person's message kind (the other kinds are
// gofer's own).
func Person(kind string) bool {
	return kind == WebMessage || kind == WebReply
}

// Prefix returns the prefix of kind, with the one space that separates it from the
// body, or "" for an unknown kind. user is the person's name for a person's kind
// and ignored otherwise; an empty name falls back to DefaultUser (a person's
// message always carries a name, so the two halves stay distinguishable).
func Prefix(kind, user string) string {
	switch kind {
	case WebMessage:
		return personPrefix(WebMessageOpen, user)
	case WebReply:
		return personPrefix(WebReplyOpen, user)
	}
	if p, ok := systemPrefixes[kind]; ok {
		return p + " "
	}
	return ""
}

// Format renders one delivered message: the prefix of kind, then body.
func Format(kind, user, body string) string {
	return Prefix(kind, user) + body
}

func personPrefix(open, user string) string {
	user = strings.TrimSpace(user)
	if user == "" {
		user = DefaultUser
	}
	return open + "，" + user + "] "
}

// Classify reads the prefix of a delivered text. kind is the message kind ("" when
// the text carries no prefix this package knows), fromPerson tells the two halves
// apart: true = a person wrote it, false = gofer generated it. Unknown text
// answers ("", false), so a plain human prompt is never mistaken for a notice.
func Classify(text string) (kind string, fromPerson bool) {
	k, person, _ := classify(text)
	return k, person
}

// IsSystem reports whether text is a gofer system message (the current prefixes
// and, until v0.188, the old ones).
func IsSystem(text string) bool {
	_, person, gofer := classify(text)
	return gofer && !person
}

// IsPerson reports whether text was written by a person (the current web prefixes
// and, until v0.188, the old "[gofer web 回复]").
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
	case strings.HasPrefix(t, WebReplyOpen):
		return WebReply, true, true
	case strings.HasPrefix(t, WebMessageOpen):
		return WebMessage, true, true
	// DEPRECATED(v0.185): remove in v0.188 (see the constants above).
	case strings.HasPrefix(t, legacyWebReply):
		return WebReply, true, true
	case strings.HasPrefix(t, legacyGofer):
		return legacySystemKind(t), false, true
	}
	return "", false, false
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
