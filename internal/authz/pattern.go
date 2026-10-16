// Pattern matching and expansion for grants (design §2「展开规则」).
//
// A grant stores a pattern, not a permission list: `tunnel.*`, `job.*.others`, `*`.
// Match is the raw glob; Expand is what a grant goes through, because the design puts
// four rules on the expansion itself:
//
//   - no pattern ever matches auth.manage (「任何授权都匹配不到 auth.manage」);
//   - a wildcard never expands to a dangerous permission — dangerous permissions must
//     be spelled out literally, so that a `job.*` grant made today does not silently
//     widen when a new dangerous permission appears tomorrow;
//   - a session grant only expands to permissions that may be granted to a session;
//   - a pattern must match at least one permission, so a grant can never be stored with
//     an empty meaning.
//
// Expansion is also the unit of the stored snapshot: a grant saves Expansion.Snapshot,
// and NewSince compares it with a fresh expansion to report 「有新权限待确认」 after the
// catalog grew.

package authz

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Errors returned by Expand and NewSince. Callers (the grant API and the web form) match
// them with errors.Is to tell the user which rule refused the pattern.
var (
	// ErrNoMatch: the pattern matches no permission after the filters of design §2, so
	// there is nothing to grant (「保存时模式必须至少匹配一项」).
	ErrNoMatch = errors.New("pattern matches no permission")
	// ErrUnknownSubject: the subject kind is not one of users / caller / session.
	ErrUnknownSubject = errors.New("unknown grant subject kind")
)

// SubjectKind is who a grant is issued to (design §2). Only SubjectSession narrows what
// a pattern may mean; SubjectUsers and SubjectCaller expand a pattern the same way.
type SubjectKind string

const (
	SubjectUsers   SubjectKind = "users"   // 全体用户类调用方
	SubjectCaller  SubjectKind = "caller"  // caller:<id>
	SubjectSession SubjectKind = "session" // session:<sid>
)

// Label is the label the permission reference and the web UI use.
func (k SubjectKind) Label() string {
	switch k {
	case SubjectUsers:
		return "全体用户"
	case SubjectCaller:
		return "调用方"
	case SubjectSession:
		return "会话"
	default:
		return string(k)
	}
}

// Valid reports whether k is one of the three subject kinds of design §2.
func (k SubjectKind) Valid() bool {
	switch k {
	case SubjectUsers, SubjectCaller, SubjectSession:
		return true
	default:
		return false
	}
}

// How a subject is SPELLED in a stored grant (design §2): the `users` subject is the bare
// literal, the other two carry their kind as a prefix, so a row of `auth_grants` is
// readable on its own and a subject id can never be confused with a permission.
const (
	SubjectCallerPrefix  = "caller:"
	SubjectSessionPrefix = "session:"
)

// ParseSubject splits a stored grant subject into its kind and its id. The `users`
// subject has no id (the second result is empty); the other two carry the id after their
// prefix. Anything else — including a prefix with nothing after it or a manager the design
// does not have (`worker:`, `job:`) — is ErrUnknownSubject, so a row no entry face can
// interpret is refused rather than silently read as one of the three.
func ParseSubject(subject string) (SubjectKind, string, error) {
	subject = strings.TrimSpace(subject)
	if subject == string(SubjectUsers) {
		return SubjectUsers, "", nil
	}
	if rest, ok := strings.CutPrefix(subject, SubjectCallerPrefix); ok {
		if id := strings.TrimSpace(rest); id != "" {
			return SubjectCaller, id, nil
		}
	}
	if rest, ok := strings.CutPrefix(subject, SubjectSessionPrefix); ok {
		if id := strings.TrimSpace(rest); id != "" {
			return SubjectSession, id, nil
		}
	}
	return "", "", fmt.Errorf("%w: %q", ErrUnknownSubject, subject)
}

// SubjectOf renders the stored spelling of a grant subject from its kind and id; it is the
// inverse of ParseSubject for the id it keeps. An unknown kind yields "" — the caller is a
// store or a handler that validated its input.
func SubjectOf(kind SubjectKind, id string) string {
	switch kind {
	case SubjectUsers:
		return string(SubjectUsers)
	case SubjectCaller:
		return SubjectCallerPrefix + id
	case SubjectSession:
		return SubjectSessionPrefix + id
	default:
		return ""
	}
}

// Match reports whether pattern matches name. `*` matches any run of characters —
// including `.` and the empty run — and every other byte matches literally. Patterns
// are not regular expressions: `?`, `[`, `\` and the rest have no special meaning.
//
// Match is the raw glob, and it is what a route declaration wants when it checks a
// single permission name. A pattern-based GRANT must not be decided with Match alone:
// go through Expand, which additionally refuses auth.manage and refuses to reach a
// dangerous permission through a wildcard.
func Match(pattern, name string) bool {
	// Single pass with one backtrack point: the most recent `*` and the position it is
	// currently swallowing from. A second star supersedes the first, which is enough
	// because `*` matches the empty run as well.
	pi, ni := 0, 0
	star, resume := -1, 0
	for ni < len(name) {
		switch {
		case pi < len(pattern) && pattern[pi] == '*':
			star, resume = pi, ni
			pi++
		case pi < len(pattern) && pattern[pi] == name[ni]:
			pi++
			ni++
		case star >= 0:
			resume++
			pi, ni = star+1, resume
		default:
			return false
		}
	}
	// The name is consumed: only trailing stars may remain in the pattern.
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
}

// Expansion is what one grant pattern means: the permissions it matches right now,
// after the filters of design §2.
type Expansion struct {
	// Pattern is the pattern as written by the grant.
	Pattern string
	// Subject is the subject kind the pattern was expanded for.
	Subject SubjectKind
	// Permissions are the matched catalog entries in design (§1) order. Expand never
	// returns an empty expansion.
	Permissions []Permission
}

// Names returns the matched permission names in design order.
func (e Expansion) Names() []string {
	out := make([]string, 0, len(e.Permissions))
	for _, p := range e.Permissions {
		out = append(out, p.Name)
	}
	return out
}

// Snapshot returns the matched names sorted, which is what a grant stores (design §2
// 「保存时…保存展开快照」). Sorted so that comparing a stored snapshot with a fresh
// expansion never depends on catalog order.
func (e Expansion) Snapshot() []string {
	out := e.Names()
	slices.Sort(out)
	return out
}

// Expand expands a grant pattern for subject and applies the safety rules of design §2:
//
//   - auth.manage — the only root-risk permission — is never matched by any pattern;
//   - a wildcard never expands to a dangerous permission: a dangerous permission is
//     handed out only when the pattern spells its name literally;
//   - a session subject only expands to permissions that may be granted to a session;
//   - a pattern that matches nothing after these filters is an error (ErrNoMatch), so a
//     grant can never be stored with an empty meaning.
//
// A permission name that spells a dangerous permission literally is refused for a
// session subject as well, because no dangerous permission is session-grantable.
func Expand(pattern string, subject SubjectKind) (Expansion, error) {
	if !subject.Valid() {
		return Expansion{}, fmt.Errorf("%w: %q", ErrUnknownSubject, string(subject))
	}
	perms, err := expandAgainst(catalog, pattern, subject)
	if err != nil {
		return Expansion{}, err
	}
	return Expansion{Pattern: pattern, Subject: subject, Permissions: perms}, nil
}

// expandAgainst is Expand against an explicit catalog. Tests use it to simulate a
// catalog upgrade — the "有新权限待确认" path of design §2 needs a catalog that grew
// while a grant was already stored.
func expandAgainst(cat []Permission, pattern string, subject SubjectKind) ([]Permission, error) {
	wildcard := strings.Contains(pattern, "*")
	out := make([]Permission, 0, len(cat))
	for _, p := range cat {
		if p.Name == PermAuthManage || p.Risk == RiskRoot {
			continue // no grant ever matches auth.manage
		}
		if !Match(pattern, p.Name) {
			continue
		}
		if wildcard && p.Risk == RiskDangerous {
			continue // dangerous permissions must be written out literally
		}
		if subject == SubjectSession && !p.SessionGrantable {
			continue
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: pattern %q", ErrNoMatch, pattern)
	}
	return out, nil
}

// NewSince returns the permissions pattern matches now that the grant's stored snapshot
// (Expansion.Snapshot, taken when the grant was saved) did not contain — the「有新权限
// 待确认」list the web shows after the catalog grew (design §2).
//
// Only permissions the expansion really means are reported: a permission that the
// filters of Expand keep out (auth.manage, a dangerous permission behind a wildcard, a
// permission that may not be granted to a session) is never "new" for that grant,
// because the grant never grants it. The result is in design order and is empty when
// nothing is new.
func NewSince(pattern string, subject SubjectKind, snapshot []string) ([]Permission, error) {
	e, err := Expand(pattern, subject)
	if err != nil {
		return nil, err
	}
	known := make(map[string]struct{}, len(snapshot))
	for _, name := range snapshot {
		known[name] = struct{}{}
	}
	out := make([]Permission, 0, len(e.Permissions))
	for _, p := range e.Permissions {
		if _, ok := known[p.Name]; ok {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}
