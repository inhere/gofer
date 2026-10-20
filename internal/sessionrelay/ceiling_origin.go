package sessionrelay

import (
	"errors"
	"fmt"
	"strings"
)

// The relay runs internal jobs on somebody's behalf (path A / B / C and the messenger),
// and those jobs carry the caller-scope ceiling owner of an HTTP request: the relay job of
// a restricted caller must be bounded by that caller's ceiling (design
// docs/design/2026-10-18-caller-scope-ceilings-design.md「上限所有者」, 「内部作业」).
//
// The display label (`by`: "web", a user name, "gofer", "gofer-mail", "gofer-nudge:<id>")
// is NOT that owner: it is what the human and the audit rows read. The ceiling owner
// travels separately, from where the request came from:
//
//   - an HTTP route: the authenticated caller;
//   - a nudge: the nudge's creator;
//   - a letter: the letter's sender;
//   - a gofer system notice: the session's owner.
//
// A delivery that cannot name its owner is refused while the deployment restricts at least
// one caller (fail closed: an unnameable owner must not be read as "unrestricted"). A
// deployment that restricts nobody cannot have a restricted delivery, so the refusal does
// not touch it — the same rule the job credential lookup applies (httpapi.authz_ceiling.go).

// ErrNoCeilingOwner reports a relay delivery whose caller-scope ceiling owner cannot be
// named while the server restricts some caller. The delivery is refused rather than run
// unbounded.
var ErrNoCeilingOwner = errors.New("sessionrelay: cannot determine the caller that owns this delivery")

// SetRestrictedCallers wires the predicate answering "does this deployment give any caller
// a ceiling" (config.ServerConfig.CeilingCallers() non-empty). nil — the zero value behind
// a bare service — means no caller is restricted, so an empty owner is allowed.
func (s *Service) SetRestrictedCallers(fn func() bool) { s.restricted = fn }

// sessionOwner is the ceiling owner of a delivery gofer started itself (a job end notice):
// the session's owner, the person behind that terminal. "" when the session has none (the
// store lookup failed, or nobody owns it), which is refused while callers are restricted.
func (s *Service) sessionOwner(sid string) string {
	a, ok, err := s.store.GetAgentSession(sid)
	if err != nil || !ok {
		return ""
	}
	return strings.TrimSpace(a.CallerID)
}

// ceilingOwner returns the ceiling owner of one delivery and fails closed when there is
// none and the deployment restricts callers. what names the delivery in the error (the
// caller has to be able to fix it).
func (s *Service) ceilingOwner(origin, what string) (string, error) {
	origin = strings.TrimSpace(origin)
	if origin != "" || s.restricted == nil || !s.restricted() {
		return origin, nil
	}
	return "", fmt.Errorf("%w: %s carries no caller id, and this server restricts callers — "+
		"give the delivery a caller (a session owner, nudge creator or letter sender) or remove the ceiling",
		ErrNoCeilingOwner, what)
}
