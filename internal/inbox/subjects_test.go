package inbox

import (
	"testing"

	"github.com/inhere/gofer/internal/today"
)

func TestSubjectsOfNamesTypeAndRef(t *testing.T) {
	t.Parallel()
	cards := []today.Card{
		{Key: "relay:s1", Kind: today.KindRelay, Refs: today.Refs{SessionID: "s1"}},
		{Key: "review:j1", Kind: today.KindReview, Refs: today.Refs{JobID: "j1"}},
		{Key: "work:w1", Kind: "work"}, // no waiting fact: left out
	}
	got := SubjectsOf("en", cards)
	if len(got) != 2 {
		t.Fatalf("subjects = %+v, want 2", got)
	}
	if s := got[0]; s.Key != "session:s1" || s.Type != SubjectSession || s.Ref != "s1" || s.Category != CatAnswer {
		t.Errorf("session subject = %+v", s)
	}
	if s := got[1]; s.Key != "job:j1" || s.Type != SubjectJob || s.Ref != "j1" || s.Category != CatReview {
		t.Errorf("job subject = %+v", s)
	}
	if n := len(Cards(got)); n != 2 {
		t.Errorf("Cards = %d", n)
	}
}

func TestWaitingSubjectsNilStore(t *testing.T) {
	t.Parallel()
	extra := today.Card{Key: "server.health:x", Kind: today.KindServerHealth}
	got, fyi, err := WaitingSubjects(WaitingInput{Lang: "en", Extra: []today.Card{extra}}, nil)
	if err != nil || len(fyi) != 0 || len(got) != 1 || got[0].Category != CatAttention {
		t.Fatalf("got=%+v fyi=%v err=%v", got, fyi, err)
	}
}
