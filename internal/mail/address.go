package mail

import (
	"fmt"
	"sort"
	"strings"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/sessionrelay"
)

// Addresses (设计「地址写法」): where a letter is sent. "session:<sid>" and "job:<id>" name one
// recipient directly; "topic:<kw>" and "path:<sub>" find one by what a session works on;
// "agent:<名字>[@<项目>]" sends to the resident session gofer keeps for that agent (设计
// 2026-10-22 mail-agent-address); "caller" (or no address at all) is the sender's own capture
// box.
const (
	SchemeSession = "session"
	SchemeJob     = "job"
	SchemeTopic   = "topic"
	SchemePath    = "path"
	SchemeCaller  = "caller"
	SchemeAgent   = "agent"
	// SchemeProject addresses every deliverable session of one project (each: only).
	SchemeProject = "project"
)

// EachPrefix turns an address into a selector for ALL it matches (设计 2026-10-22「D1」):
// `each:topic:login` is every session the topic matches, not "exactly one or park it". An
// address without the prefix keeps its meaning exactly (several matches still park).
const EachPrefix = "each:"

// Address is one parsed address: the scheme and the value after its first colon (a path may
// itself contain colons — a Windows drive letter, say — so only the FIRST one separates).
type Address struct {
	Raw    string
	Scheme string
	Value  string
	// Project is the `@<项目>` of an agent: address (empty = the sender's own project).
	Project string
	// Each marks the `each:` prefix: the address fans out to every deliverable match instead
	// of requiring exactly one.
	Each bool
}

// ParseAddress parses what a caller addressed a letter to. An empty (or blank) address and the
// bare word "caller" both mean the sender's own capture box; an unknown scheme is
// ErrInvalidAddress; agent: is ErrUnsupportedAddress (the third phase).
func ParseAddress(raw string) (Address, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return Address{Raw: "", Scheme: SchemeCaller}, nil
	}
	each := false
	if len(trimmed) > len(EachPrefix) && strings.EqualFold(trimmed[:len(EachPrefix)], EachPrefix) {
		each = true
		trimmed = strings.TrimSpace(trimmed[len(EachPrefix):])
	}
	scheme, value := trimmed, ""
	if i := strings.Index(trimmed, ":"); i >= 0 {
		scheme, value = trimmed[:i], strings.TrimSpace(trimmed[i+1:])
	}
	scheme = strings.ToLower(strings.TrimSpace(scheme))
	rawAddr := trimmed
	if each {
		rawAddr = EachPrefix + trimmed
	}
	switch scheme {
	case SchemeCaller:
		if value != "" {
			return Address{}, fmt.Errorf("%w: caller takes no value", ErrInvalidAddress)
		}
		if each {
			return Address{}, fmt.Errorf("%w: each: takes a selector (topic: / path: / project: / agent:), not the caller box", ErrInvalidAddress)
		}
		return Address{Raw: rawAddr, Scheme: SchemeCaller}, nil
	case SchemeSession, SchemeJob, SchemeTopic, SchemePath:
		if value == "" {
			return Address{}, fmt.Errorf("%w: %s: needs a value", ErrInvalidAddress, scheme)
		}
		if each && (scheme == SchemeSession || scheme == SchemeJob) {
			return Address{}, fmt.Errorf("%w: each: takes a selector (topic: / path: / project: / agent:), not %s:", ErrInvalidAddress, scheme)
		}
		return Address{Raw: rawAddr, Scheme: scheme, Value: value, Each: each}, nil
	case SchemeProject:
		if value == "" {
			return Address{}, fmt.Errorf("%w: %s: needs a value", ErrInvalidAddress, scheme)
		}
		if !each {
			return Address{}, fmt.Errorf("%w: project: is a selector: write each:project:<key>", ErrInvalidAddress)
		}
		return Address{Raw: rawAddr, Scheme: scheme, Value: value, Each: true}, nil
	case SchemeAgent:
		name, project := splitAgentValue(value)
		if name == "" {
			return Address{}, fmt.Errorf("%w: agent: needs a configured agent name (agent:<名字>[@<项目>])", ErrInvalidAddress)
		}
		// each:agent:<名字> stays a broadcast selector over the sessions that already exist
		// (设计「不做」: a broadcast never starts a session), so its @<项目> is not meaningful.
		if each {
			return Address{Raw: rawAddr, Scheme: scheme, Value: name, Project: project, Each: true}, nil
		}
		return Address{Raw: rawAddr, Scheme: scheme, Value: name, Project: project}, nil
	}
	return Address{}, fmt.Errorf("%w: unknown scheme %q", ErrInvalidAddress, scheme)
}

// splitAgentValue splits an agent: value into the name and the optional `@<项目>` (the LAST '@'
// separates: a configured name cannot contain one).
func splitAgentValue(v string) (name, project string) {
	v = strings.TrimSpace(v)
	if i := strings.LastIndex(v, "@"); i >= 0 {
		return strings.TrimSpace(v[:i]), strings.TrimSpace(v[i+1:])
	}
	return v, ""
}

// Target is the recipient an address resolved to. Deliver is false when there is no delivery
// path for it (a caller has no session and no job to be sent into — its mail is the capture
// box). Topic / Offline describe the session it names (设计: an offline session still takes
// part in resolution, flagged as possibly gone).
type Target struct {
	Kind    string
	ID      string
	Caller  string
	Address string
	Topic   string
	Source  string
	Offline bool
	Deliver bool
}

// Candidate is a session a topic: / path: address almost named: the letter goes to the capture
// box with these, so a person (or its author) can pick one instead of the mailbox guessing.
type Candidate struct {
	Kind    string
	ID      string
	Topic   string
	Source  string
	Offline bool
	// Why lists why the session matched (sessionrelay's hit kinds: scope / scope_weak / topic
	// / peer_name / project).
	Why []string
}

// Display is the candidate as a person reads it: 「会话 sess-a（gofer 开发，可能不在线）」.
func (c Candidate) Display() string {
	label := "会话 " + c.ID
	if c.Topic != "" {
		label += "（" + c.Topic
		if c.Offline {
			label += "，可能不在线"
		}
		label += "）"
	} else if c.Offline {
		label += "（可能不在线）"
	}
	return label
}

// resolution is one address resolved: the target plus the candidates when there is nothing
// unique to deliver to.
type resolution struct {
	target     Target
	candidates []Candidate
	// reason explains a hold the resolution itself decided (a caller address, a full thread).
	reason string
}

type resolveInput struct {
	addr    Address
	origin  Origin
	project string
	visible func(jobstore.AgentSession) bool
	roots   func(projectKey string) []string
}

// findLimit caps what a topic: / path: lookup scans. The search ranks before it truncates, so a
// generous cap only costs a scan.
const findLimit = 50

// resolve turns an address into exactly one recipient, or into the candidates that were not
// one. It never guesses: several or no matches mean the letter is parked, not sent.
func (s *Service) resolve(in resolveInput) (resolution, error) {
	switch in.addr.Scheme {
	case SchemeCaller:
		return resolution{
			target: Target{
				Kind: jobstore.MailKindCaller, Caller: in.origin.Caller,
				Address: in.addr.Raw, Deliver: false,
			},
			reason: "caller: the sender's own capture box",
		}, nil
	case SchemeSession:
		return s.resolveSession(in)
	case SchemeJob:
		return s.resolveJob(in)
	case SchemeTopic:
		return s.resolveByFind(in, true)
	case SchemePath:
		return s.resolveByFind(in, false)
	case SchemeAgent:
		return s.residentAgent(in)
	}
	return resolution{}, fmt.Errorf("%w: %s:", ErrInvalidAddress, in.addr.Scheme)
}

// maxAmbiguousIDs caps how many full ids an ambiguous session prefix names in its error.
const maxAmbiguousIDs = 8

// resolveSession answers a session:<sid> address with the same rule as `--session`: an exact id,
// else a prefix that names exactly one session (ended ones count for the match, so a short id
// of a finished session says "held", not "not found"). No match is ErrSessionNotFound, several
// are ErrSessionAmbiguous naming their full ids. Sessions the reader may not see do not count.
func (s *Service) resolveSession(in resolveInput) (resolution, error) {
	a, ok, err := s.store.GetAgentSession(in.addr.Value)
	if err != nil {
		return resolution{}, err
	}
	if ok && in.visible != nil && !in.visible(a) {
		ok = false
	}
	if !ok {
		hits, err := s.store.FindAgentSessionsByPrefix(in.addr.Value, 200)
		if err != nil {
			return resolution{}, err
		}
		var seen []jobstore.AgentSession
		for _, h := range hits {
			// The reader may not see this session, so it is not a recipient either: naming
			// it back would be a disclosure the caller has no right to.
			if in.visible == nil || in.visible(h) {
				seen = append(seen, h)
			}
		}
		switch len(seen) {
		case 0:
			return resolution{}, fmt.Errorf("%w: %s matches no session (see `gofer session ls --all`)", ErrSessionNotFound, in.addr.Raw)
		case 1:
			a = seen[0]
		default:
			ids := make([]string, 0, maxAmbiguousIDs)
			for i, h := range seen {
				if i == maxAmbiguousIDs {
					break
				}
				ids = append(ids, h.SessionID)
			}
			more := ""
			if len(seen) > len(ids) {
				more = fmt.Sprintf(" (+%d more)", len(seen)-len(ids))
			}
			return resolution{}, fmt.Errorf("%w: %s matches %d sessions: %s%s; give more characters",
				ErrSessionAmbiguous, in.addr.Raw, len(seen), strings.Join(ids, ", "), more)
		}
	}
	if !sessionrelay.IsDeliverableState(a.State) {
		return resolution{reason: in.addr.Raw + " names no deliverable session"}, nil
	}
	return resolution{target: Target{
		Kind: jobstore.MailKindSession, ID: a.SessionID, Address: in.addr.Raw,
		Topic: a.Topic, Source: a.TopicSource,
		Offline: a.State == jobstore.SessionOffline, Deliver: true,
	}}, nil
}

// resolveJob answers a job:<id> address. A job: address names its recipient exactly, so there is
// nothing to park: a job that no longer exists or that can never take a letter (an ended job, a
// one-off run with no ACP session) is REFUSED here, with what to do instead. Parking such a
// letter for a person to assign made the sender wait 24h for an answer nobody would ever give
// (gofer-0n6r).
func (s *Service) resolveJob(in resolveInput) (resolution, error) {
	rec, ok, err := s.store.GetJob(in.addr.Value)
	if err != nil {
		return resolution{}, err
	}
	if !ok {
		return resolution{}, fmt.Errorf("%w: %s names no job (see `gofer job ls`)%s", ErrJobNotFound, in.addr.Raw, jobMailInstead)
	}
	if job.IsTerminal(rec.Status) {
		return resolution{}, fmt.Errorf("%w: %s already ended (%s) and will never read a letter; read its report instead (`gofer job show %s`)%s",
			ErrJobCannotReceive, in.addr.Raw, rec.Status, in.addr.Value, jobMailInstead)
	}
	if s.sessionJob != nil {
		if session, found := s.sessionJob(in.addr.Value); found && !session {
			return resolution{}, fmt.Errorf("%w: %s is a one-off job (no ACP session), so nothing can be said into it%s",
				ErrJobCannotReceive, in.addr.Raw, jobMailInstead)
		}
	}
	return resolution{target: Target{
		Kind: jobstore.MailKindJob, ID: in.addr.Value, Address: in.addr.Raw, Deliver: true,
	}}, nil
}

// jobMailInstead is the tail of a refused job: address: what the sender can do instead. (It avoids
// <…> placeholders: the text travels in a JSON body, where Go escapes angle brackets.)
const jobMailInstead = "; mail the session behind it (an address like session:…), wait for it to end and read its report, or leave a comment on it (`gofer job comment …`)"

// resolveByFind answers a topic: / path: address through the session finder (sessionrelay.Find
// — the matching and ranking live there). topic: keeps the sessions whose DECLARED topic
// matched (an inferred topic never takes part in delivery); path: keeps the strong scope hits,
// because the design only delivers on a unique strong match. Exactly one survivor is the
// recipient; anything else is parked with the survivors as candidates.
func (s *Service) resolveByFind(in resolveInput, byTopic bool) (resolution, error) {
	if s.finder == nil {
		return resolution{}, fmt.Errorf("%w: session lookup is not wired in this process", ErrInvalidAddress)
	}
	q := sessionrelay.FindQuery{
		Project: in.project, Limit: findLimit, Visible: in.visible, Roots: in.roots,
	}
	if byTopic {
		q.Q = in.addr.Value
	} else {
		q.Path = in.addr.Value
	}
	results, err := s.finder.Find(q)
	if err != nil {
		return resolution{}, err
	}
	kept := make([]sessionrelay.FindResult, 0, len(results))
	for _, r := range results {
		if byTopic {
			if topicDeclaredHit(r) {
				kept = append(kept, r)
			}
			continue
		}
		if strongScopeHit(r) {
			kept = append(kept, r)
		}
	}
	if len(kept) == 1 {
		r := kept[0]
		return resolution{target: Target{
			Kind: jobstore.MailKindSession, ID: r.Session.SessionID, Address: in.addr.Raw,
			Topic: r.Topic, Source: r.TopicSource, Offline: r.MaybeOffline, Deliver: true,
		}}, nil
	}
	// No strong hit at all: the weaker matches are still worth showing a person (they are what
	// the sender probably meant), so they become the candidates of a parked letter.
	if len(kept) == 0 && len(results) > 0 {
		kept = results
	}
	cands := make([]Candidate, 0, len(kept))
	for _, r := range kept {
		cands = append(cands, candidateOf(r))
	}
	return resolution{
		candidates: cands,
		reason:     fmt.Sprintf("%s matched %d sessions", in.addr.Raw, len(kept)),
	}, nil
}

// topicDeclaredHit reports whether the session's DECLARED topic matched the query (a topic hit
// whose source is self / human; an inferred topic is display-only).
func topicDeclaredHit(r sessionrelay.FindResult) bool {
	if r.TopicSource == sessionrelay.TopicSourceInferred {
		return false
	}
	for _, h := range r.Hits {
		if h.Kind == sessionrelay.HitTopic && h.Source != sessionrelay.TopicSourceInferred {
			return true
		}
	}
	return false
}

// strongScopeHit reports whether a scope EQUALS the queried path or is an ancestor of it — the
// design's strong命中 ("谁负责这个路径"); the weak kind (the query is an ancestor of the scope)
// is not enough to deliver on.
func strongScopeHit(r sessionrelay.FindResult) bool {
	for _, h := range r.Hits {
		if h.Kind == sessionrelay.HitScope {
			return true
		}
	}
	return false
}

func candidateOf(r sessionrelay.FindResult) Candidate {
	why := make([]string, 0, len(r.Hits))
	for _, h := range r.Hits {
		why = append(why, h.Kind)
	}
	sort.Strings(why)
	return Candidate{
		Kind: jobstore.MailKindSession, ID: r.Session.SessionID, Topic: r.Topic,
		Source: r.TopicSource, Offline: r.MaybeOffline, Why: why,
	}
}

// mailJobID stamps the job a letter is ABOUT when either end is a job, so job redaction and the
// secret scan can find the letters of a job (设计「敏感信息」).
func mailJobID(toKind, toID string, org Origin) string {
	if toKind == jobstore.MailKindJob {
		return toID
	}
	if org.Kind == jobstore.MailKindJob {
		return org.ID
	}
	return ""
}
