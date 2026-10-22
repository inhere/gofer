package mail

import (
	"fmt"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/sessionrelay"
)

// Broadcast (设计 2026-10-22「信箱广播」): one letter to several recipients. Every recipient gets
// its OWN letter row — same thread, same broadcast group — so each copy walks the existing state
// machine and delivery path untouched; only the grouping is new.
//
// The sender must ask for it: `--to` repeated, or a selector with the `each:` prefix. An address
// without that prefix keeps its single-recipient meaning (several matches still park the letter).

// Broadcast-limit sentinels; the entry layer maps them onto 4xx.
var (
	// ErrNoRecipients: the addresses resolved to nobody deliverable — nothing is stored.
	ErrNoRecipients = fmt.Errorf("mail: broadcast has no recipient")
	// ErrBroadcastTooMany: more recipients than mail.broadcast_max_recipients.
	ErrBroadcastTooMany = fmt.Errorf("mail: broadcast has too many recipients")
	// ErrUrgentBroadcast: urgent and a broadcast are incompatible (nothing wakes a group at once).
	ErrUrgentBroadcast = fmt.Errorf("mail: an urgent broadcast is not supported")
	// ErrBroadcastAmbiguous: an address of a broadcast did not name exactly one recipient.
	ErrBroadcastAmbiguous = fmt.Errorf("mail: broadcast address is not a single recipient")
)

// eachScanLimit bounds how many sessions one each:project: / each:agent: selector scans.
const eachScanLimit = 200

// BroadcastInput is one broadcast to send. To is every address given (repeated --to); an address
// with the each: prefix fans out to every match.
type BroadcastInput struct {
	To      []string
	Body    string
	Urgent  bool
	Origin  Origin
	Project string
	Visible func(jobstore.AgentSession) bool
	Roots   func(projectKey string) []string
	// Authorize is SendInput.Authorize, applied to EVERY recipient: an explicitly named one
	// without permission refuses the whole broadcast, a selector's is skipped and counted.
	Authorize func(Target) error
}

// BroadcastRecipient is one addressee of a broadcast as the sender sees it afterwards.
type BroadcastRecipient struct {
	// Address is the address that selected this recipient (one of BroadcastInput.To).
	Address string
	Kind    string
	ID      string
	MailID  int64
	State   string
	// Skipped marks a selector match the sender may not deliver to; Reason says why.
	Skipped bool
	Reason  string
}

// BroadcastResult is what a broadcast produced: the copies (one letter each) and the addressee
// table. Matched counts the sessions the addresses selected, Skipped the ones dropped by
// permission.
type BroadcastResult struct {
	Mails       []jobstore.Mail
	Recipients  []BroadcastRecipient
	ThreadID    string
	BroadcastID int64
	Matched     int
	Skipped     int
}

// targetKey identifies one recipient end (caller-less sessions and jobs are told apart by kind).
func targetKey(t Target) string {
	if t.Kind == jobstore.MailKindCaller {
		return t.Kind + "/" + t.Caller
	}
	return t.Kind + "/" + t.ID
}

// Broadcast stores one letter per recipient. It is Send for several recipients: the addresses
// are resolved first (an explicit address must name exactly one deliverable recipient), then
// judged per recipient, then written — so a refused broadcast leaves no letter behind.
func (s *Service) Broadcast(in BroadcastInput) (BroadcastResult, error) {
	org, err := in.Origin.Validate()
	if err != nil {
		return BroadcastResult{}, err
	}
	if err := s.checkBody(in.Body); err != nil {
		return BroadcastResult{}, err
	}
	if in.Urgent {
		return BroadcastResult{}, ErrUrgentBroadcast
	}
	if len(in.To) == 0 {
		return BroadcastResult{}, ErrNoRecipients
	}
	if err := s.checkBroadcastRate(org); err != nil {
		return BroadcastResult{}, err
	}
	targets, skipped, err := s.planBroadcast(in, org)
	if err != nil {
		return BroadcastResult{}, err
	}
	matched := len(targets) + len(skipped)
	if len(targets) == 0 {
		return BroadcastResult{}, ErrNoRecipients
	}
	if max := s.broadcastMaxRecipients(); max > 0 && len(targets) > max {
		return BroadcastResult{}, fmt.Errorf("%w: %d recipients (at most %d); narrow the selector",
			ErrBroadcastTooMany, len(targets), max)
	}
	out := BroadcastResult{Matched: matched, Skipped: len(skipped)}
	for _, t := range targets {
		res := resolution{target: t}
		mi := jobstore.MailInput{
			ThreadID: out.ThreadID,
			FromKind: org.Kind, FromID: org.ID, FromCaller: org.Caller,
			ToAddress: t.Address, ToKind: t.Kind, ToID: t.ID, ToCaller: t.Caller,
			JobID: mailJobID(t.Kind, t.ID, org), Body: in.Body,
			DeliverBy: s.deliverBy(),
		}
		r, err := s.storeLetter(mi, res, org)
		if err != nil {
			return out, err
		}
		out.ThreadID = r.Mail.ThreadID
		if out.BroadcastID == 0 {
			out.BroadcastID = r.Mail.ID
		}
		if _, err := s.store.SetMailBroadcastID(r.Mail.ID, out.BroadcastID); err != nil {
			return out, err
		}
		if r.Mail.BroadcastID == 0 {
			r.Mail.BroadcastID = out.BroadcastID
		}
		out.Mails = append(out.Mails, r.Mail)
		out.Recipients = append(out.Recipients, BroadcastRecipient{
			Address: t.Address, Kind: t.Kind, ID: t.ID, MailID: r.Mail.ID, State: r.Mail.State,
		})
	}
	for _, st := range skipped {
		out.Recipients = append(out.Recipients, st)
	}
	return out, nil
}

// planBroadcast resolves the addresses into the recipients to write and the selector matches the
// sender may not deliver to (those are only reported). An explicitly named recipient without
// permission refuses the whole broadcast — a sender must not believe a letter went out.
func (s *Service) planBroadcast(in BroadcastInput, org Origin) (targets []Target, skipped []BroadcastRecipient, err error) {
	seen := make(map[string]bool, len(in.To))
	var explicit, fanout []Target
	for _, raw := range in.To {
		addr, err := ParseAddress(raw)
		if err != nil {
			return nil, nil, err
		}
		rin := resolveInput{addr: addr, origin: org, project: in.Project, visible: in.Visible, roots: in.Roots}
		if addr.Each {
			ts, err := s.resolveEach(rin)
			if err != nil {
				return nil, nil, err
			}
			fanout = append(fanout, ts...)
			continue
		}
		res, err := s.resolve(rin)
		if err != nil {
			return nil, nil, err
		}
		if res.target.Kind == "" || !res.target.Deliver || len(res.candidates) > 0 {
			reason := res.reason
			if reason == "" {
				reason = subjectWithoutTarget(addr.Raw, res.candidates)
			}
			return nil, nil, fmt.Errorf("%w: %s — %s; use each:%s to send to every match",
				ErrBroadcastAmbiguous, addr.Raw, reason, addr.Value)
		}
		explicit = append(explicit, res.target)
	}
	for _, t := range explicit {
		if seen[targetKey(t)] {
			continue
		}
		if in.Authorize != nil {
			if err := in.Authorize(t); err != nil {
				return nil, nil, err
			}
		}
		seen[targetKey(t)] = true
		targets = append(targets, t)
	}
	for _, t := range fanout {
		if seen[targetKey(t)] {
			continue
		}
		seen[targetKey(t)] = true
		if in.Authorize != nil {
			if err := in.Authorize(t); err != nil {
				// A selector is filtered, not refused: the sender is told how many were
				// dropped, and the id is not echoed back (it named a session it may not reach).
				skipped = append(skipped, BroadcastRecipient{Address: t.Address, Kind: t.Kind, Skipped: true, Reason: err.Error()})
				continue
			}
		}
		targets = append(targets, t)
	}
	return targets, skipped, nil
}

// resolveEach is resolve for a `each:` selector: every deliverable session it matches, never the
// park-with-candidates answer. The matching rules stay sessionrelay's (topic must be DECLARED,
// path must be a strong scope hit) and the reader filter still applies.
func (s *Service) resolveEach(in resolveInput) ([]Target, error) {
	switch in.addr.Scheme {
	case SchemeTopic, SchemePath:
		if s.finder == nil {
			return nil, fmt.Errorf("%w: session lookup is not wired in this process", ErrInvalidAddress)
		}
		q := sessionrelay.FindQuery{Project: in.project, Limit: findLimit, Visible: in.visible, Roots: in.roots}
		byTopic := in.addr.Scheme == SchemeTopic
		if byTopic {
			q.Q = in.addr.Value
		} else {
			q.Path = in.addr.Value
		}
		results, err := s.finder.Find(q)
		if err != nil {
			return nil, err
		}
		out := make([]Target, 0, len(results))
		for _, r := range results {
			if byTopic {
				if !topicDeclaredHit(r) {
					continue
				}
			} else if !strongScopeHit(r) {
				continue
			}
			out = append(out, Target{
				Kind: jobstore.MailKindSession, ID: r.Session.SessionID, Address: in.addr.Raw,
				Topic: r.Topic, Source: r.TopicSource, Offline: r.MaybeOffline, Deliver: true,
			})
		}
		return out, nil
	case SchemeProject, SchemeAgent:
		opts := jobstore.ListSessionsOpts{Limit: eachScanLimit}
		if in.addr.Scheme == SchemeProject {
			opts.Project = in.addr.Value
		} else {
			opts.Agent = in.addr.Value
		}
		sessions, err := s.store.ListAgentSessions(opts)
		if err != nil {
			return nil, err
		}
		out := make([]Target, 0, len(sessions))
		for _, a := range sessions {
			if !sessionrelay.IsDeliverableState(a.State) {
				continue
			}
			if in.visible != nil && !in.visible(a) {
				continue
			}
			out = append(out, Target{
				Kind: jobstore.MailKindSession, ID: a.SessionID, Address: in.addr.Raw,
				Topic: a.Topic, Source: a.TopicSource, Offline: a.State == jobstore.SessionOffline, Deliver: true,
			})
		}
		return out, nil
	}
	return nil, fmt.Errorf("%w: each:%s: is not a selector", ErrInvalidAddress, in.addr.Scheme)
}
