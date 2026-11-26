package inbox

import (
	"errors"
	"slices"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/stage"
	"github.com/inhere/gofer/internal/today"
)

type fakeStageSource struct {
	facts []stage.WaitingFact
	err   error
}

func (f fakeStageSource) Waiting(allow func(string) bool) ([]stage.WaitingFact, error) {
	var out []stage.WaitingFact
	for _, x := range f.facts {
		if allow == nil || allow(x.ProjectKey) {
			out = append(out, x)
		}
	}
	return out, f.err
}

func actionIDs(c today.Card) []string {
	var ids []string
	for _, a := range c.Actions {
		ids = append(ids, a.ID)
	}
	return ids
}

func TestStageCardsClassesAndActions(t *testing.T) {
	src := fakeStageSource{facts: []stage.WaitingFact{
		{WorkItemID: "w-1", Title: "login", ProjectKey: "p", Step: "review", Kind: stage.WaitReview, Since: 100},
		{WorkItemID: "w-2", Title: "export", ProjectKey: "p", Step: "verify", Kind: stage.WaitNeedsHuman, Missing: []string{"git unreadable"}, Since: 200},
		{WorkItemID: "w-3", Title: "sync", ProjectKey: "q", Step: "verify", Kind: stage.WaitVerifyFailed, Missing: []string{"run failed"}, Since: 300},
	}}
	cards, err := StageCards(src, i18n.ZhCN, nil)
	if err != nil || len(cards) != 3 {
		t.Fatalf("cards = %+v %v", cards, err)
	}
	waiting := Waiting(i18n.ZhCN, cards)
	if len(waiting) != 3 {
		t.Fatalf("waiting = %+v", waiting)
	}
	want := map[string]struct {
		cat     Category
		actions []string
	}{
		"stage:w-1": {CatApproval, []string{"stage_approve", "stage_back", "open"}},
		"stage:w-2": {CatAttention, []string{"stage_approve", "stage_back", "open"}},
		"stage:w-3": {CatAttention, []string{"stage_back", "open"}},
	}
	for _, c := range waiting {
		w, ok := want[c.Key]
		if !ok || Category(c.Waiting) != w.cat || !slices.Equal(actionIDs(c), w.actions) || c.Kind != today.KindStage || c.Refs.WorkItemID == "" {
			t.Fatalf("card %s = %+v, want %+v", c.Key, c, w)
		}
	}
	for _, c := range waiting {
		for _, a := range c.Actions {
			if a.ID == "stage_back" && !a.NeedsText {
				t.Fatalf("the back action must ask for the note: %+v", a)
			}
		}
	}
	// The subject of a stage card is the flow, so the list folds one entry per flow.
	subs := SubjectsOf(i18n.EN, cards)
	if len(subs) != 3 || subs[0].Type != "stage" || subs[0].Ref != "w-1" || subs[0].Category != CatApproval {
		t.Fatalf("subjects = %+v", subs)
	}
	// Project scope narrows the list; a failing source is an error.
	scoped, err := StageCards(src, i18n.EN, func(p string) bool { return p == "q" })
	if err != nil || len(scoped) != 1 || scoped[0].Refs.WorkItemID != "w-3" {
		t.Fatalf("scoped = %+v %v", scoped, err)
	}
	if _, err := StageCards(fakeStageSource{err: errors.New("boom")}, i18n.EN, nil); err == nil {
		t.Fatal("a failing source must fail the cards")
	}
}

func TestStageKindIsInboxActionAndNotAdvisable(t *testing.T) {
	if got := config.InboxDefaultColumn(today.KindStage); got != "action" {
		t.Fatalf("stage column = %q, want action", got)
	}
	if today.CanAdvise(today.KindStage) {
		t.Fatal("advice cannot be written for a stage card")
	}
}
