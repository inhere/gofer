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
// "caller" (or no address at all) is the sender's own capture box; "agent:" belongs to the
// third phase and is refused for now.
const (
	SchemeSession = "session"
	SchemeJob     = "job"
	SchemeTopic   = "topic"
	SchemePath    = "path"
	SchemeCaller  = "caller"
	SchemeAgent   = "agent"
)

// Address is one parsed address: the scheme and the value after its first colon (a path may
// itself contain colons — a Windows drive letter, say — so only the FIRST one separates).
type Address struct {
	Raw    string
	Scheme string
	Value  string
}

// ParseAddress parses what a caller addressed a letter to. An empty (or blank) address and the
// bare word "caller" both mean the sender's own capture box; an unknown scheme is
// ErrInvalidAddress; agent: is ErrUnsupportedAddress (the third phase).
func ParseAddress(raw string) (Address, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return Address{Raw: "", Scheme: SchemeCaller}, nil
	}
	scheme, value := trimmed, ""
	if i := strings.Index(trimmed, ":"); i >= 0 {
		scheme, value = trimmed[:i], strings.TrimSpace(trimmed[i+1:])
	}
	scheme = strings.ToLower(strings.TrimSpace(scheme))
	switch scheme {
	case SchemeCaller:
		if value != "" {
			return Address{}, fmt.Errorf("%w: caller takes no value", ErrInvalidAddress)
		}
		return Address{Raw: trimmed, Scheme: SchemeCaller}, nil
	case SchemeSession, SchemeJob, SchemeTopic, SchemePath:
		if value == "" {
			return Address{}, fmt.Errorf("%w: %s: needs a value", ErrInvalidAddress, scheme)
		}
		return Address{Raw: trimmed, Scheme: scheme, Value: value}, nil
	case SchemeAgent:
		return Address{}, fmt.Errorf("%w: %s: (host agents are the third phase)", ErrUnsupportedAddress, SchemeAgent)
	}
	return Address{}, fmt.Errorf("%w: unknown scheme %q", ErrInvalidAddress, scheme)
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
	}
	return resolution{}, fmt.Errorf("%w: %s:", ErrInvalidAddress, in.addr.Scheme)
}

func (s *Service) resolveSession(in resolveInput) (resolution, error) {
	a, ok, err := s.store.GetAgentSession(in.addr.Value)
	if err != nil {
		return resolution{}, err
	}
	if !ok || !sessionrelay.IsDeliverableState(a.State) {
		return resolution{reason: in.addr.Raw + " names no deliverable session"}, nil
	}
	if in.visible != nil && !in.visible(a) {
		// The reader may not see this session, so it is not a recipient either: reporting it
		// back would be a disclosure the caller has no right to.
		return resolution{reason: in.addr.Raw + " names no deliverable session"}, nil
	}
	return resolution{target: Target{
		Kind: jobstore.MailKindSession, ID: a.SessionID, Address: in.addr.Raw,
		Topic: a.Topic, Source: a.TopicSource,
		Offline: a.State == jobstore.SessionOffline, Deliver: true,
	}}, nil
}

func (s *Service) resolveJob(in resolveInput) (resolution, error) {
	rec, ok, err := s.store.GetJob(in.addr.Value)
	if err != nil {
		return resolution{}, err
	}
	if !ok || job.IsTerminal(rec.Status) {
		return resolution{reason: in.addr.Raw + " names no live job"}, nil
	}
	return resolution{target: Target{
		Kind: jobstore.MailKindJob, ID: in.addr.Value, Address: in.addr.Raw, Deliver: true,
	}}, nil
}

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
