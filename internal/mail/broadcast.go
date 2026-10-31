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

// eachScanLimit is the page size of an each:project: / each:agent: scan; the scan keeps paging
// until a short page, so a project with more live sessions than that is not cut short.
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
	// DeliveredAt (unix seconds, 0 = not delivered yet) and Replied (the recipient answered this
	// copy) are filled by BroadcastSummary only, the sender's later view of the group.
	DeliveredAt int64
	Replied     bool
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
	org, err := s.checkedOrigin(in)
	if err != nil {
		return BroadcastResult{}, err
	}
	if err := s.checkBroadcastRate(org); err != nil {
		return BroadcastResult{}, err
	}
	targets, skipped, err := s.planCheckedBroadcast(in, org)
	if err != nil {
		return BroadcastResult{}, err
	}
	out := BroadcastResult{Matched: len(targets) + len(skipped), Skipped: len(skipped)}
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

// Preview resolves a broadcast without storing anything (POST /v1/mail with dry_run): the sender
// sees who the letter would reach and who a selector would skip. It does not count against the
// broadcast rate — a preview is not a send — but every other rule (body, urgent, recipient
// ceiling, no recipient) answers exactly as Broadcast would.
func (s *Service) Preview(in BroadcastInput) (BroadcastResult, error) {
	org, err := s.checkedOrigin(in)
	if err != nil {
		return BroadcastResult{}, err
	}
	targets, skipped, err := s.planCheckedBroadcast(in, org)
	if err != nil {
		return BroadcastResult{}, err
	}
	out := BroadcastResult{Matched: len(targets) + len(skipped), Skipped: len(skipped)}
	for _, t := range targets {
		out.Recipients = append(out.Recipients, BroadcastRecipient{Address: t.Address, Kind: t.Kind, ID: t.ID})
	}
	out.Recipients = append(out.Recipients, skipped...)
	return out, nil
}

// checkedOrigin applies the rules that depend on neither the addresses nor the store: a validated
// sender and a body a broadcast may carry (no urgent broadcast).
func (s *Service) checkedOrigin(in BroadcastInput) (Origin, error) {
	org, err := in.Origin.Validate()
	if err != nil {
		return Origin{}, err
	}
	if err := s.checkBody(in.Body); err != nil {
		return Origin{}, err
	}
	if in.Urgent {
		return Origin{}, ErrUrgentBroadcast
	}
	if len(in.To) == 0 {
		return Origin{}, ErrNoRecipients
	}
	return org, nil
}

// planCheckedBroadcast resolves the addresses and applies the recipient rules: at least one
// recipient, at most mail.broadcast_max_recipients.
func (s *Service) planCheckedBroadcast(in BroadcastInput, org Origin) ([]Target, []BroadcastRecipient, error) {
	targets, skipped, err := s.planBroadcast(in, org)
	if err != nil {
		return nil, nil, err
	}
	if len(targets) == 0 {
		return nil, nil, ErrNoRecipients
	}
	if max := s.broadcastMaxRecipients(); max > 0 && len(targets) > max {
		return nil, nil, fmt.Errorf("%w: %d recipients (at most %d); narrow the selector",
			ErrBroadcastTooMany, len(targets), max)
	}
	return targets, skipped, nil
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
		out := make([]Target, 0, eachScanLimit)
		// One page is not the whole answer: a project (or one agent) may hold more live sessions
		// than a page and the tail must not be dropped (gofer-ndy5). Page until a short page;
		// the recipient ceiling still decides whether such a selector may be sent.
		for {
			sessions, err := s.store.ListAgentSessions(opts)
			if err != nil {
				return nil, err
			}
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
			if len(sessions) < eachScanLimit {
				break
			}
			opts.Offset += eachScanLimit
		}
		return out, nil
	}
	return nil, fmt.Errorf("%w: each:%s: is not a selector", ErrInvalidAddress, in.addr.Scheme)
}

// BroadcastSummary is one broadcast's addressee table as the sender reads it afterwards: the
// state counts of the group and one row per copy (设计 2026-10-22「D3」).
type BroadcastSummary struct {
	ID         int64
	Count      int
	Delivered  int
	Answered   int
	Expired    int
	Pending    int
	Recipients []BroadcastRecipient
}

// BroadcastSummary reads the copies of broadcast id, oldest first. A broadcast id <= 0 (a single
// letter) has no summary and returns nil. A copy counts as replied once its state is answered.
func (s *Service) BroadcastSummary(id int64) (*BroadcastSummary, error) {
	if id <= 0 {
		return nil, nil
	}
	rows, err := s.store.ListMail(jobstore.ListMailOpts{BroadcastID: id, Limit: jobstore.MailListMaxLimit, OldestFirst: true})
	if err != nil {
		return nil, err
	}
	sum := &BroadcastSummary{ID: id, Count: len(rows), Recipients: make([]BroadcastRecipient, 0, len(rows))}
	for _, row := range rows {
		switch row.State {
		case jobstore.MailAnswered:
			sum.Answered++
			sum.Delivered++
		case jobstore.MailDelivered:
			sum.Delivered++
		case jobstore.MailExpired:
			sum.Expired++
		case jobstore.MailPending, jobstore.MailDelivering:
			sum.Pending++
		}
		sum.Recipients = append(sum.Recipients, BroadcastRecipient{
			Address: row.ToAddress, Kind: row.ToKind, ID: row.ToID, MailID: row.ID, State: row.State,
			DeliveredAt: row.DeliveredAt, Replied: row.State == jobstore.MailAnswered,
		})
	}
	return sum, nil
}
