package inbox

import (
	"errors"
	"slices"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/team"
	"github.com/inhere/gofer/internal/today"
)

type fakeTeamSource struct {
	facts []team.AttentionFact
	err   error
}

func (f fakeTeamSource) Attention(allow func(string) bool) ([]team.AttentionFact, error) {
	var out []team.AttentionFact
	for _, x := range f.facts {
		if allow == nil || allow(x.ProjectKey) {
			out = append(out, x)
		}
	}
	return out, f.err
}

// A team whose leader missed its evaluations is one 「等我」 entry under the attention class,
// filed by the team; the card kind is an inbox kind in the action column.
func TestTeamCardsAreAttentionSubjects(t *testing.T) {
	src := fakeTeamSource{facts: []team.AttentionFact{
		{TeamID: "t1", Name: "core", ProjectKey: "p", Kind: team.AttentionMissingEval, Since: 100},
		{TeamID: "t2", Name: "other", ProjectKey: "q", Kind: team.AttentionMissingEval, Since: 200},
	}}
	cards, err := TeamCards(src, i18n.EN, nil)
	if err != nil || len(cards) != 2 {
		t.Fatalf("cards = %+v %v", cards, err)
	}
	c := cards[0]
	if c.Key != "team:t1" || c.Kind != today.KindTeam || c.Subtype != team.AttentionMissingEval || c.Refs.TeamID != "t1" || c.WaitingSince != 100 {
		t.Fatalf("card = %+v", c)
	}
	subs := SubjectsOf(i18n.EN, cards)
	if len(subs) != 2 || subs[0].Key != "team:t1" || subs[0].Category != CatAttention || subs[0].Type != "team" || subs[0].Ref != "t1" {
		t.Fatalf("subjects = %+v", subs)
	}
	waiting, _, err := WaitingSubjects(WaitingInput{Lang: i18n.EN, Team: src, Allow: func(p string) bool { return p == "p" }}, nil)
	if err != nil || len(waiting) != 1 || waiting[0].Key != "team:t1" {
		t.Fatalf("scoped waiting = %+v %v", waiting, err)
	}
	if _, err := TeamCards(fakeTeamSource{err: errors.New("boom")}, i18n.EN, nil); err == nil {
		t.Fatal("a failing source must fail the cards")
	}
	if config.InboxDefaultColumn(today.KindTeam) != "action" || today.CanAdvise(today.KindTeam) ||
		!slices.Equal(config.InboxKindEvents(today.KindTeam), []string{"session.awaiting_reply"}) {
		t.Fatal("team kind policy wrong")
	}
}
