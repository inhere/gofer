package team

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// The routing engine (plan 2026-11-26-team-plan.md §2.3). It is the DECIDING half only: given
// who sends a letter and where the address resolved to, Decide says which seats get it, under
// which rule, and what the letter must carry (hop, source tag). It reads the stored seats,
// teams and the route ledger and writes nothing; the mailbox stores the letters and writes the
// ledger rows around the delivery (pending before, settled after), so the engine never needs
// to import it (G022: mail → team, never the other way).
//
// Every rule reads the sender from the SERVER side: the origin the entry layer vouched for,
// matched against the seat a session / job currently sits in. Nothing in the letter text
// (a claimed role, a forged source tag) can raise a sender to leader (rule 8).

// Rule names (team_routes.rule).
const (
	// RuleSelf: the leader wrote to the team or to itself — recorded, never delivered (1).
	RuleSelf = "self"
	// RuleHumanNamed / RuleHumanDefault: a human (or anyone outside the team) wrote to the
	// team; `@member` in the body picks the members, else the leader gets it (2).
	RuleHumanNamed   = "human_named"
	RuleHumanDefault = "human_default"
	// RuleMemberResult: a member's result letter goes to the leader (3).
	RuleMemberResult = "member_result"
	// RuleRelay: a member names another member (4); over the loop guard it is handed to the
	// leader instead (outcome handed_to_leader).
	RuleRelay = "relay"
	// RuleLeaderDispatch: the leader writes to a member; it only breaks relay runs.
	RuleLeaderDispatch = "leader_dispatch"
	// RulePeerLeader: the leader of another team wrote; only this team's leader gets it (10).
	RulePeerLeader = "peer_leader"
	// RuleMemberFailed: the system letter a failed member job sends the leader (9).
	RuleMemberFailed = "member_failed"
	// RuleChainEnd: the system letter that puts a finished chain's last result in front of the
	// team creator (leader protocol, plan §2.4).
	RuleChainEnd = "chain_end"
	// RuleOutbound: a team seat wrote to somebody outside the team; recorded so the ledger
	// run is broken.
	RuleOutbound = "outbound"
	// RuleDirect: somebody outside the team addressed a member seat by name; delivered as
	// addressed.
	RuleDirect = "direct"
)

// SystemMemberFailed is mail.system_kind of the letter a failed member job produces;
// SystemChainEnd that of the letter a chain end produces.
const (
	SystemMemberFailed = "member_failed"
	SystemChainEnd     = "chain_end"
)

// Party is a sender as the entry layer vouched for it.
type Party struct {
	// Kind is jobstore.MailKindSession / MailKindJob / MailKindCaller / MailKindGofer.
	Kind   string
	ID     string
	Caller string
}

// Input is one letter about to be stored.
type Input struct {
	Origin Party
	// ToRaw is the address as written ("" = none: the sender's own box).
	ToRaw string
	// ViaTeamAddr says the address was `team:` (the letter was addressed to the team itself).
	ViaTeamAddr bool
	// TeamID is the team the address resolved through (team: or the seat: of a team seat).
	TeamID string
	// Target* is the resolved recipient: the session / job (empty when the seat is vacant) and
	// the seat it sits in when the address named one.
	TargetKind, TargetID, TargetSeatID string
	Body                               string
	// ReplyHop is the hop of the letter this one answers (0 = none).
	ReplyHop int
	// SourceMailID is the letter this one forwards or answers (0 = none); with IdemKey it
	// forms the route key.
	SourceMailID int64
	// IdemKey is an idempotency key the sender (or the server for a job) supplied.
	IdemKey string
	Now     time.Time
}

// Decision is what the engine decided. Applies=false means the letter is not a team matter and
// goes through the mailbox exactly as before.
type Decision struct {
	Applies bool
	// Team is the team whose ledger records the decision (the recipient's, or the sender's
	// for an outbound letter); Ledgers lists every ledger to write (a peer letter lands in
	// both leaders' teams).
	Team    jobstore.Team
	Ledgers []jobstore.Team
	Rule    string
	// FromRef is who the ledger says sent it.
	FromRef string
	// Recipients are the seats to deliver to; empty keeps the address's own recipient.
	Recipients []jobstore.Seat
	// Handed: the loop guard redirected the letter to the leader (outcome handed_to_leader).
	Handed bool
	// Drop: record only, deliver nothing (rule self).
	Drop bool
	// SenderLeader: the sender is, right now, the session / job sitting in the leader seat of
	// the team that sends (the leader protocol's eval binding keys on it).
	SenderLeader bool
	// Hold is set when the letter must be parked instead of delivered (a peer pair over the
	// loop limit: there is nobody to redirect to but the leader that is already the target).
	Hold string
	// Hop is the hop the letter carries; Tag the source line to prefix to its body ("" none).
	Hop int
	Tag string
	// Note tells the recipient why it got a letter it was not addressed with (a redirect).
	Note string

	keyBase string
}

// KeyFor is the route key of the letter to one recipient seat ("" = the letter has no source
// identity to dedupe on).
func (d Decision) KeyFor(seatID string) string {
	if d.keyBase == "" || seatID == "" {
		return ""
	}
	return d.keyBase + ":" + seatID
}

// Router decides routes over the metadata store.
type Router struct {
	store *jobstore.Store
}

// NewRouter builds the routing engine.
func NewRouter(store *jobstore.Store) *Router { return &Router{store: store} }

// party classifies a sender: the seat it currently sits in and that seat's team.
type party struct {
	seat   jobstore.Seat
	seated bool
	team   jobstore.Team
	inTeam bool
}

func (p party) leader() bool { return p.inTeam && p.team.LeaderSeatID == p.seat.ID }
func (p party) member() bool { return p.inTeam && p.team.LeaderSeatID != p.seat.ID }

func (r *Router) seatOfRef(ref string) (jobstore.Seat, bool, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return jobstore.Seat{}, false, nil
	}
	m, err := r.store.SeatsByRefs([]string{ref})
	if err != nil {
		return jobstore.Seat{}, false, err
	}
	x, ok := m[ref]
	if !ok || x.ArchivedAt != 0 {
		return jobstore.Seat{}, false, nil
	}
	return x, true, nil
}

// sender reads the seat a vouched origin currently sits in. A caller (a human at the CLI / web)
// and a session or job outside any seat are not seated.
func (r *Router) sender(o Party) (party, error) {
	if o.Kind != jobstore.MailKindSession && o.Kind != jobstore.MailKindJob {
		return party{}, nil
	}
	x, ok, err := r.seatOfRef(o.ID)
	if err != nil || !ok {
		return party{}, err
	}
	p := party{seat: x, seated: true}
	t, in, err := r.store.TeamOfSeat(x.ID)
	if err != nil {
		return party{}, err
	}
	p.team, p.inTeam = t, in
	return p, nil
}

// IsLeader reports whether the origin is, right now, the session / job sitting in the leader
// seat of an active team, and returns that team. It is the one judgement every leader-only
// privilege uses (rule 8): it reads the seat table, never a letter.
func (r *Router) IsLeader(o Party) (jobstore.Team, bool, error) {
	p, err := r.sender(o)
	if err != nil || !p.leader() {
		return jobstore.Team{}, false, err
	}
	return p.team, true, nil
}

func (r *Router) leaderSeat(t jobstore.Team) (jobstore.Seat, error) {
	x, ok, err := r.store.GetSeat(t.LeaderSeatID)
	if err != nil {
		return jobstore.Seat{}, err
	}
	if !ok {
		return jobstore.Seat{}, fmt.Errorf("team %s: leader seat %s is gone", t.Name, t.LeaderSeatID)
	}
	return x, nil
}

// recipient resolves the team and seat a letter is going to.
func (r *Router) recipient(in Input) (jobstore.Team, bool, jobstore.Seat, bool, error) {
	var seat jobstore.Seat
	haveSeat := false
	switch {
	case in.TargetSeatID != "":
		x, ok, err := r.store.GetSeat(in.TargetSeatID)
		if err != nil {
			return jobstore.Team{}, false, jobstore.Seat{}, false, err
		}
		seat, haveSeat = x, ok && x.ArchivedAt == 0
	case in.TargetID != "":
		x, ok, err := r.seatOfRef(in.TargetID)
		if err != nil {
			return jobstore.Team{}, false, jobstore.Seat{}, false, err
		}
		seat, haveSeat = x, ok
	}
	if in.TeamID != "" {
		t, ok, err := r.store.GetTeam(in.TeamID)
		if err != nil {
			return jobstore.Team{}, false, jobstore.Seat{}, false, err
		}
		if ok && t.ArchivedAt == 0 {
			if !haveSeat {
				if x, err := r.leaderSeat(t); err == nil {
					seat, haveSeat = x, true
				}
			}
			return t, true, seat, haveSeat, nil
		}
	}
	if !haveSeat {
		return jobstore.Team{}, false, jobstore.Seat{}, false, nil
	}
	t, ok, err := r.store.TeamOfSeat(seat.ID)
	if err != nil || !ok {
		return jobstore.Team{}, false, seat, haveSeat, err
	}
	return t, true, seat, haveSeat, nil
}

// namedMember finds `@name`. The `@` must not continue a word: `user@host` is an e-mail-like
// token, not a mention (Go's regexp has no look-behind, so the character before the `@` is part
// of the match and group 2 is the name).
var namedMember = regexp.MustCompile(`(^|[^\p{L}\p{N}_.+%\-])@([\p{L}\p{N}_.\-]+)`)

// codeFence / codeSpan are the code a mention must not be read from: a fenced block (an
// unterminated fence runs to the end of the body) and an inline span on one line.
var (
	codeFence = regexp.MustCompile("(?s)```.*?(```|$)")
	codeSpan  = regexp.MustCompile("`[^`\n]*`")
)

// mentionedNames lists the `@name` mentions of a body in order, leaving out e-mail-like tokens
// and anything inside inline or fenced code (gofer-x6v6).
func mentionedNames(body string) []string {
	body = codeSpan.ReplaceAllString(codeFence.ReplaceAllString(body, " "), " ")
	var out []string
	for _, h := range namedMember.FindAllStringSubmatch(body, -1) {
		// A name ends a sentence with its dot or dash: "ask @alice." names alice.
		if n := strings.TrimRight(h[2], ".-"); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// namedSeats are the seats of the team an `@name` in the body points at (members and leader).
func (r *Router) namedSeats(t jobstore.Team, body string) ([]jobstore.Seat, error) {
	hits := mentionedNames(body)
	if len(hits) == 0 {
		return nil, nil
	}
	members, err := r.store.ListTeamMembers(t.ID)
	if err != nil {
		return nil, err
	}
	byName := map[string]jobstore.Seat{}
	add := func(id string) error {
		x, ok, err := r.store.GetSeat(id)
		if err != nil || !ok || x.ArchivedAt != 0 {
			return err
		}
		byName[strings.ToLower(x.Name)] = x
		return nil
	}
	if err := add(t.LeaderSeatID); err != nil {
		return nil, err
	}
	for _, m := range members {
		if err := add(m.SeatID); err != nil {
			return nil, err
		}
	}
	var out []jobstore.Seat
	seen := map[string]bool{}
	for _, h := range hits {
		x, ok := byName[strings.ToLower(h)]
		if ok && !seen[x.ID] {
			seen[x.ID] = true
			out = append(out, x)
		}
	}
	return out, nil
}

func fromRefOf(o Party, p party) string {
	switch {
	case p.seated:
		return "seat:" + p.seat.ID
	case o.Kind == jobstore.MailKindCaller:
		return "caller:" + o.Caller
	case o.Kind == jobstore.MailKindGofer:
		return "gofer"
	}
	return o.Kind + ":" + o.ID
}

// Decide applies the ten rules (plan §2.3) in order. See the package comment.
func (r *Router) Decide(in Input) (Decision, error) {
	if in.Origin.Kind == jobstore.MailKindGofer {
		return Decision{}, nil // gofer's own notices are terminal (rule 9)
	}
	snd, err := r.sender(in.Origin)
	if err != nil {
		return Decision{}, err
	}
	rteam, hasTeam, rseat, hasSeat, err := r.recipient(in)
	if err != nil {
		return Decision{}, err
	}
	implicit := false
	if snd.member() && strings.TrimSpace(in.ToRaw) == "" {
		// A member's letter without a recipient is its result letter (rule 3).
		rteam, hasTeam, implicit = snd.team, true, true
		if x, err := r.leaderSeat(rteam); err == nil {
			rseat, hasSeat = x, true
		}
	}
	d := Decision{FromRef: fromRefOf(in.Origin, snd), SenderLeader: snd.leader()}
	if !hasTeam {
		if !snd.inTeam {
			return Decision{}, nil
		}
		d.Applies, d.Team, d.Ledgers, d.Rule = true, snd.team, []jobstore.Team{snd.team}, RuleOutbound
		d.Hop = in.ReplyHop + 1
		if hasSeat {
			d.Recipients = nil
		}
		return d, nil
	}
	d.Applies, d.Team, d.Ledgers = true, rteam, []jobstore.Team{rteam}
	d.keyBase = keyBase(in)
	label := senderLabel(in.Origin, snd)
	tag := func(hop int) string {
		return fmt.Sprintf("[team %s · from %s · hop %d]", rteam.Name, label, hop)
	}
	toLeader := func() ([]jobstore.Seat, error) {
		x, err := r.leaderSeat(rteam)
		if err != nil {
			return nil, err
		}
		return []jobstore.Seat{x}, nil
	}
	sameTeam := snd.inTeam && snd.team.ID == rteam.ID
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	switch {
	case sameTeam && snd.leader():
		if in.ViaTeamAddr || (hasSeat && rseat.ID == snd.seat.ID) || (hasSeat && rseat.ID == rteam.LeaderSeatID) {
			d.Rule, d.Drop, d.Hop = RuleSelf, true, in.ReplyHop
			return d, nil
		}
		d.Rule, d.Hop = RuleLeaderDispatch, in.ReplyHop+1
		d.Tag = tag(d.Hop)
	case sameTeam && snd.member():
		d.Hop = in.ReplyHop + 1
		switch {
		case implicit || in.ViaTeamAddr || (hasSeat && rseat.ID == rteam.LeaderSeatID):
			d.Rule = RuleMemberResult
			if d.Recipients, err = toLeader(); err != nil {
				return Decision{}, err
			}
		case hasSeat && rseat.ID == snd.seat.ID:
			return Decision{}, nil // a member writing itself is not routed
		default:
			d.Rule = RuleRelay
			handed, note, gerr := r.relayGuard(rteam, snd.seat.ID, rseat.ID, now)
			if gerr != nil {
				return Decision{}, gerr
			}
			if handed {
				d.Handed, d.Note = true, note
				if d.Recipients, err = toLeader(); err != nil {
					return Decision{}, err
				}
			}
		}
		d.Tag = tag(d.Hop)
	case snd.leader():
		// The leader of another team (rule 10): only this team's leader reads it, `@member`
		// does not apply, and the two leaders count as one pair for the loop guard.
		d.Rule, d.Hop = RulePeerLeader, in.ReplyHop+1
		label = snd.seat.Name + "@" + snd.seat.ProjectKey
		d.Ledgers = append(d.Ledgers, snd.team)
		lead, lerr := r.leaderSeat(rteam)
		if lerr != nil {
			return Decision{}, lerr
		}
		d.Recipients = []jobstore.Seat{lead}
		handed, note, gerr := r.relayGuard(rteam, snd.seat.ID, lead.ID, now)
		if gerr != nil {
			return Decision{}, gerr
		}
		if handed {
			d.Hold = note
		}
		d.Tag = tag(d.Hop)
	default:
		// Anybody else — a human, a session outside every team, a member of another team.
		d.Hop = in.ReplyHop
		switch {
		case in.ViaTeamAddr:
			named, nerr := r.namedSeats(rteam, in.Body)
			if nerr != nil {
				return Decision{}, nerr
			}
			if len(named) > 0 {
				d.Rule, d.Recipients = RuleHumanNamed, named
			} else {
				d.Rule = RuleHumanDefault
				if d.Recipients, err = toLeader(); err != nil {
					return Decision{}, err
				}
			}
			d.Tag = tag(d.Hop)
		default:
			d.Rule = RuleDirect
		}
	}
	return d, nil
}

// keyBase is the key a letter dedupes on: the sender's idempotency key, else the letter it
// forwards or answers. A fresh letter has no source identity, so it has no key.
func keyBase(in Input) string {
	switch {
	case strings.TrimSpace(in.IdemKey) != "":
		return "k:" + strings.TrimSpace(in.IdemKey)
	case in.SourceMailID > 0:
		return fmt.Sprintf("m%d", in.SourceMailID)
	}
	return ""
}

func senderLabel(o Party, p party) string {
	switch {
	case p.seated && p.inTeam:
		return p.seat.Name
	case p.seated:
		return p.seat.Name + "@" + p.seat.ProjectKey
	case o.Kind == jobstore.MailKindCaller:
		return "human"
	}
	return o.Kind + " " + o.ID
}

// The relay loop guard works on the route ledger, not on a chain depth (plan §2.3 rule 4).
const dayLength = 24 * time.Hour

// counted reports whether a ledger row is a letter that reached a mailbox.
func counted(o string) bool {
	return o == jobstore.TeamOutcomeDelivered || o == jobstore.TeamOutcomeQueued || o == jobstore.TeamOutcomeMerged
}

// ignored rows neither count nor break a run: they never reached anybody.
func ignored(row jobstore.TeamRoute) bool {
	switch row.Outcome {
	case jobstore.TeamOutcomePending, jobstore.TeamOutcomeUnknown, jobstore.TeamOutcomeDup,
		jobstore.TeamOutcomeDroppedSelf:
		return true
	case jobstore.TeamOutcomeHeld:
		// A letter parked for a person (an outbound one lands in a caller box) still ends a
		// run; one parked because its seat was vacant never reached anybody.
		return row.Rule != RuleOutbound
	}
	return row.Rule == RuleMemberFailed || row.Rule == RuleChainEnd
}

func rowPair(row jobstore.TeamRoute) (string, string, bool) {
	from := strings.TrimPrefix(row.FromRef, "seat:")
	if from == row.FromRef || len(row.ToSeatIDs) != 1 {
		return "", "", false
	}
	return from, row.ToSeatIDs[0], true
}

// handedPair reads the sender seat of a relay row that was handed to the leader (its recipient
// is the leader, not the member it named).
func handedPair(row jobstore.TeamRoute) (string, string, bool) {
	from := strings.TrimPrefix(row.FromRef, "seat:")
	if from == row.FromRef {
		return "", "", false
	}
	return from, "", true
}

func samePair(a1, b1, a2, b2 string) bool {
	return (a1 == a2 && b1 == b2) || (a1 == b2 && b1 == a2)
}

// relayGuard says whether a relay from seat a to seat b must be handed to the leader instead:
// the head of the ledger is a run of letters between the same pair at least RelayRoundLimit
// long, or the team's member-to-member relays of the last 24 hours reached RelayDailyLimit.
// Anything else in the run — the leader's dispatch, a third party, a letter to a human —
// ends it. A limit of 0 switches that check off.
func (r *Router) relayGuard(t jobstore.Team, a, b string, now time.Time) (bool, string, error) {
	if t.RelayRoundLimit > 0 {
		rows, err := r.store.ListTeamRoutes(t.ID, t.RelayRoundLimit+64)
		if err != nil {
			return false, "", err
		}
		run := 0
		for _, row := range rows {
			if ignored(row) {
				continue
			}
			// A relay of this same pair that was already handed to the leader neither counts
			// nor ends the run: the pair stays held at the leader until something else (the
			// leader's dispatch, a third party, a person) breaks it, instead of getting another
			// RelayRoundLimit letters after every hand-over.
			if row.Rule == RuleRelay && row.Outcome == jobstore.TeamOutcomeHandedToLeader {
				if f, _, ok := handedPair(row); ok && (f == a || f == b) {
					continue
				}
			}
			f, to, ok := rowPair(row)
			if !ok || !counted(row.Outcome) || !samePair(f, to, a, b) {
				break
			}
			run++
		}
		if run >= t.RelayRoundLimit {
			return true, fmt.Sprintf("the same pair already exchanged %d letters in a row (limit %d)", run, t.RelayRoundLimit), nil
		}
	}
	if t.RelayDailyLimit > 0 {
		rows, err := r.store.ListTeamRoutesSince(t.ID, now.Add(-dayLength).Unix(), 0)
		if err != nil {
			return false, "", err
		}
		n := 0
		for _, row := range rows {
			if row.Rule == RuleRelay && counted(row.Outcome) {
				n++
			}
		}
		if n >= t.RelayDailyLimit {
			return true, fmt.Sprintf("the team already relayed %d member-to-member letters in 24h (limit %d)", n, t.RelayDailyLimit), nil
		}
	}
	return false, "", nil
}

// Failure is a failed member job and the team around it.
type Failure struct {
	Team   jobstore.Team
	Member jobstore.Seat
	Leader jobstore.Seat
}

// MemberFailure says whether a job is the one sitting in a member seat of an active team (rule
// 9). The leader's own jobs are not covered: a leader that fails has nobody above it to tell.
func (r *Router) MemberFailure(jobID string) (Failure, bool, error) {
	x, ok, err := r.seatOfRef(jobID)
	if err != nil || !ok {
		return Failure{}, false, err
	}
	t, in, err := r.store.TeamOfSeat(x.ID)
	if err != nil || !in || t.LeaderSeatID == x.ID {
		return Failure{}, false, err
	}
	lead, err := r.leaderSeat(t)
	if err != nil {
		return Failure{}, false, err
	}
	return Failure{Team: t, Member: x, Leader: lead}, true, nil
}
