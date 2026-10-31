package today

import (
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/work"
)

// The shims give the golden scenario one way to call the text producers.

func shimDecisions(lang i18n.Lang, svc *Service) ([]Card, error) { return svc.DecisionsIn(lang, true) }

func shimToday(lang i18n.Lang, svc *Service, q Query) (Response, error) {
	q.Lang = lang
	return svc.Today(q)
}

func shimLanes(lang i18n.Lang, w *work.Service) (LanesView, error) {
	return NewLanesBuilder(w).BuildForIn(lang, nil)
}

func shimMemorySuggestions(lang i18n.Lang, svc *Service) ([]MemorySuggestion, error) {
	return svc.ListMemorySuggestionsIn(lang, "all")
}

func shimEnrollCard(lang i18n.Lang, pending []EnrollPending) (Card, bool) {
	return WorkerEnrollCardIn(lang, pending)
}
