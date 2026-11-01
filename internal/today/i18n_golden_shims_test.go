package today

import (
	"github.com/inhere/gofer/internal/i18n"
)

// The shims give the golden scenario one way to call the text producers.

func shimDecisions(lang i18n.Lang, svc *Service) ([]Card, error) { return svc.DecisionsIn(lang, true) }

func shimToday(lang i18n.Lang, svc *Service, q Query) (Response, error) {
	q.Lang = lang
	return svc.Today(q)
}
