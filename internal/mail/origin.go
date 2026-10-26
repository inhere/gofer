package mail

import (
	"fmt"
	"strings"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
)

// Where a letter came from (设计「发件出处与信任级别」). The server cannot tell the web console
// from a CLI or an agent holding the same token, so there is no "the user themself" level: a
// letter is from a SESSION (identified by the session header — an attribution inside one
// caller, not an authentication), from a CALLER (no session identity), or from a JOB (its own
// credential). The entry layer authenticates and checks the session; this type only has to be
// a legal combination.

// Origin is who sent a letter.
type Origin struct {
	// Kind is jobstore.MailKindSession / MailKindCaller / MailKindJob.
	Kind string
	// ID is the session id or the job id; empty for a caller.
	ID string
	// Caller is the authenticated caller behind the letter (the session's owner, the caller
	// itself, or the caller that owns the job credential).
	Caller string
}

// Validate normalises the origin and refuses a combination the mailbox cannot stamp: a session
// or job sender needs its id, everything needs a known kind. It returns the normalised origin.
func (o Origin) Validate() (Origin, error) {
	norm := Origin{Kind: strings.TrimSpace(o.Kind), ID: strings.TrimSpace(o.ID), Caller: strings.TrimSpace(o.Caller)}
	switch norm.Kind {
	case jobstore.MailKindSession, jobstore.MailKindJob:
		if norm.ID == "" {
			return Origin{}, fmt.Errorf("%w: %s sender needs an id", ErrInvalidOrigin, norm.Kind)
		}
	case jobstore.MailKindCaller:
		if norm.ID != "" {
			return Origin{}, fmt.Errorf("%w: a caller sender has no id", ErrInvalidOrigin)
		}
	case jobstore.MailKindGofer:
		// gofer itself (a 退信): no id, no caller.
		if norm.ID != "" || norm.Caller != "" {
			return Origin{}, fmt.Errorf("%w: a gofer sender has no id and no caller", ErrInvalidOrigin)
		}
	case "":
		return Origin{}, fmt.Errorf("%w: kind required", ErrInvalidOrigin)
	default:
		return Origin{}, fmt.Errorf("%w: unknown kind %q", ErrInvalidOrigin, norm.Kind)
	}
	return norm, nil
}

// Address is the address this origin can be reached at, i.e. what a reply is sent to.
func (o Origin) Address() string { return AddressOf(o.Kind, o.ID, o.Caller) }

// Display renders the origin in the default language.
func (o Origin) Display() string { return o.DisplayLang(i18n.Default) }

// DisplayLang renders the human label of an origin in lang.
func (o Origin) DisplayLang(lang i18n.Lang) string {
	switch o.Kind {
	case jobstore.MailKindSession:
		return withCaller(i18n.T(lang, "mail.origin.session", "id", o.ID), o.Caller, lang)
	case jobstore.MailKindJob:
		return withCaller("job "+o.ID, o.Caller, lang)
	case jobstore.MailKindCaller:
		if o.Caller == "" {
			return i18n.T(lang, "mail.origin.caller")
		}
		return i18n.T(lang, "mail.origin.caller") + " " + o.Caller
	case jobstore.MailKindGofer:
		return "gofer"
	}
	return i18n.T(lang, "mail.origin.unknown")
}

// OriginOf reads the origin of a stored letter.
func OriginOf(m jobstore.Mail) Origin {
	return Origin{Kind: m.FromKind, ID: m.FromID, Caller: m.FromCaller}
}

// AddressOf spells the address of a stored letter's end ("session:sess-a", "job:job-7",
// "caller:alice") — what a reply's to_address carries.
func AddressOf(kind, id, caller string) string {
	switch kind {
	case jobstore.MailKindSession:
		return "session:" + id
	case jobstore.MailKindJob:
		return "job:" + id
	case jobstore.MailKindCaller:
		return "caller:" + caller
	case jobstore.MailKindGofer:
		// gofer has no id and no caller: the address is just its name, so a 退信 still has a
		// party to show (and a conversation to belong to). Nothing sends INTO it.
		return "gofer"
	}
	return ""
}

func withCaller(label, caller string, lang i18n.Lang) string {
	if caller == "" {
		return label
	}
	if i18n.Resolve(lang) == i18n.EN {
		return label + " (caller " + caller + ")"
	}
	return label + "（调用方 " + caller + "）"
}
