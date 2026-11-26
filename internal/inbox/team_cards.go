package inbox

import (
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/team"
	"github.com/inhere/gofer/internal/today"
)

// KindTeam is the card kind of a team that needs a person.
const KindTeam = today.KindTeam

// TeamSource is what the team cards read: the teams that need a person (team.Service implements
// it).
type TeamSource interface {
	Attention(allow func(string) bool) ([]team.AttentionFact, error)
}

// TeamCards builds the cards of teams whose leader missed MissingEvalLimit evaluations in a row
// (plan 2026-11-26-team-plan.md §2.4). Everything is derived from the stored evaluations on every
// read, so a card goes away with its fact (the leader records one) and nothing is kept.
func TeamCards(src TeamSource, lang i18n.Lang, allow func(string) bool) ([]today.Card, error) {
	facts, err := src.Attention(allow)
	if err != nil {
		return nil, err
	}
	cards := make([]today.Card, 0, len(facts))
	for _, f := range facts {
		cards = append(cards, today.Card{
			Key:          KindTeam + ":" + f.TeamID,
			Kind:         KindTeam,
			Subtype:      f.Kind,
			Title:        i18n.T(lang, "httpapi.inbox_team_"+f.Kind, "team", f.Name, "n", team.MissingEvalLimit),
			Summary:      i18n.T(lang, "httpapi.inbox_team_"+f.Kind+"_summary", "team", f.Name),
			ProjectKey:   f.ProjectKey,
			ActivityAt:   f.Since,
			WaitingSince: f.Since,
			Refs:         today.Refs{TeamID: f.TeamID},
			Actions: []today.Action{
				{ID: "open", Label: i18n.T(lang, "today.action.open_team"), Style: "link", Value: "/team?id=" + f.TeamID},
			},
		})
	}
	return cards, nil
}
