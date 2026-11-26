package inbox

import (
	"strings"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/today"
	"github.com/inhere/gofer/internal/util"
)

// Subject types: the part of a subject key before the first colon.
const (
	SubjectSession = "session" // a terminal session (Ref = session id)
	SubjectJob     = "job"     // a job, including a managed ACP / suga session job (Ref = job id)
)

// Subject is one entry of the 「等我」 list seen by what it is about: the stable key the
// subject files under, its category, the kind of thing it is and that thing's id. The seat
// status and the notice record read this form; the cards behind the HTTP lists are Card.
type Subject struct {
	// Key is the subject key (the one Waiting collapses cards by), e.g. "job:<id>".
	Key      string
	Category Category
	// Type is SubjectSession / SubjectJob, or the other key prefix (decision, todo, plan ...).
	Type string
	// Ref is the id after the prefix: the terminal session id or the job id.
	Ref  string
	Card today.Card
}

// WaitingStore is the part of the job store the 「等我」 sources read.
type WaitingStore interface {
	TerminalSource
	ChildWakeSource
	ScheduleSource
	SeatScheduleSource
}

// WaitingInput carries what the caller decides: language and project scope. Store may be
// nil (no store sources then); Extra holds the cards of sources that need the caller's own
// permissions (an expiring token, the server health list), appended after the store ones.
type WaitingInput struct {
	Store WaitingStore
	Lang  i18n.Lang
	// Allow is the caller's project scope (nil = unrestricted).
	Allow func(string) bool
	Extra []today.Card
	// Stage, when non-nil, adds the flows that wait for a person (nil = no flow cards).
	Stage StageSource
	// Team, when non-nil, adds the teams whose leader stopped evaluating its turns.
	Team TeamSource
}

// WaitingSubjects is the one 「等我」 judgement: the structured-fact cards of the today queue
// plus the caller-visible failures of jobs a person submitted, child receipts that could not
// be delivered and schedules gofer paused, folded by Waiting. fyi is the person's own
// finished jobs (the inbox's second column). Every input is a store fact, so the answer
// does not change when the server restarts.
func WaitingSubjects(in WaitingInput, queue []today.Card) (waiting []Subject, fyi []today.Card, err error) {
	var failed, stranded, paused, seatSched, flows, teams []today.Card
	if in.Store != nil {
		if failed, fyi, err = TerminalCards(in.Store, in.Lang, in.Allow); err != nil {
			return nil, nil, err
		}
		// A child receipt that can no longer reach its resident parent is a call to action
		// too: only a person can pick the child's result up now.
		if stranded, err = ChildWakeCards(in.Store, in.Lang, in.Allow); err != nil {
			return nil, nil, err
		}
		// A schedule gofer stopped after repeated failures waits for a person too.
		if paused, err = SchedulePausedCards(in.Store, in.Lang, in.Allow); err != nil {
			return nil, nil, err
		}
		// A seat schedule whose text had nobody to go to waits for a person to open the seat.
		if seatSched, err = SeatScheduleCards(in.Store, in.Lang, in.Allow); err != nil {
			return nil, nil, err
		}
	}
	if in.Stage != nil {
		// A stage flow waiting for a person's approval or look.
		if flows, err = StageCards(in.Stage, in.Lang, in.Allow); err != nil {
			return nil, nil, err
		}
	}
	if in.Team != nil {
		if teams, err = TeamCards(in.Team, in.Lang, in.Allow); err != nil {
			return nil, nil, err
		}
	}
	cards := make([]today.Card, 0, util.CapSum(len(queue), len(failed), len(stranded), len(paused), len(seatSched), len(flows), len(teams), len(in.Extra)))
	cards = append(append(append(append(append(cards, queue...), failed...), stranded...), paused...), seatSched...)
	cards = append(cards, flows...)
	cards = append(cards, teams...)
	cards = append(cards, in.Extra...)
	return SubjectsOf(in.Lang, cards), fyi, nil
}

// SubjectsOf is Waiting with the subject spelled out: the same folding and order, one
// Subject per waiting entry. The result is never nil.
func SubjectsOf(lang i18n.Lang, cards []today.Card) []Subject {
	waiting := Waiting(lang, cards)
	out := make([]Subject, 0, len(waiting))
	for _, c := range waiting {
		key, _, _ := signalsOf(c)
		typ, ref, _ := strings.Cut(key, ":")
		out = append(out, Subject{Key: key, Category: Category(c.Waiting), Type: typ, Ref: ref, Card: c})
	}
	return out
}

// Cards drops the subject view: the cards of the list, in order.
func Cards(subjects []Subject) []today.Card {
	out := make([]today.Card, 0, len(subjects))
	for _, s := range subjects {
		out = append(out, s.Card)
	}
	return out
}
