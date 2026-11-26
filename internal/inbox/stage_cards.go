package inbox

import (
	"strings"
	"unicode/utf8"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/stage"
	"github.com/inhere/gofer/internal/today"
)

// KindStage is the card kind of a stage flow that waits for a person.
const KindStage = today.KindStage

// stageSummaryRunes bounds the gate's missing items on the card.
const stageSummaryRunes = 200

// StageSource is what the flow cards read: the flows that wait (stage.Service implements it).
type StageSource interface {
	Waiting(allow func(string) bool) ([]stage.WaitingFact, error)
}

// StageCards builds the cards of flows that wait for a person (plan F4): the review step's
// approval, a gate that cannot be judged here, a failed verify run. Everything is derived from
// the stored flow on every read, so a card goes away with its fact and nothing is kept.
//
// The card's actions name existing write APIs the console calls: stage_approve is POST
// /v1/work-items/{id}/stage/approve, stage_back is POST .../stage/back with the typed text as
// the reason (a person's note sent back with the flow).
func StageCards(src StageSource, lang i18n.Lang, allow func(string) bool) ([]today.Card, error) {
	facts, err := src.Waiting(allow)
	if err != nil {
		return nil, err
	}
	cards := make([]today.Card, 0, len(facts))
	for _, f := range facts {
		summary := strings.Join(f.Missing, "; ")
		if utf8.RuneCountInString(summary) > stageSummaryRunes {
			summary = string([]rune(summary)[:stageSummaryRunes]) + "…"
		}
		c := today.Card{
			Key:          KindStage + ":" + f.WorkItemID,
			Kind:         KindStage,
			Subtype:      f.Kind,
			Title:        i18n.T(lang, "httpapi.inbox_stage_"+f.Kind, "title", f.Title, "step", f.Step),
			Summary:      summary,
			ProjectKey:   f.ProjectKey,
			ActivityAt:   f.Since,
			WaitingSince: f.Since,
			Refs:         today.Refs{WorkItemID: f.WorkItemID},
		}
		back := today.Action{ID: "stage_back", Label: i18n.T(lang, "today.action.stage_back"), NeedsText: true}
		open := today.Action{ID: "open", Label: i18n.T(lang, "today.action.open_work"), Style: "link", Value: "/work?id=" + f.WorkItemID}
		switch f.Kind {
		case stage.WaitVerifyFailed:
			c.Actions = []today.Action{back, open}
		default:
			c.Actions = []today.Action{
				{ID: "stage_approve", Label: i18n.T(lang, "today.action.approve"), Style: "primary"},
				back, open,
			}
		}
		cards = append(cards, c)
	}
	return cards, nil
}
