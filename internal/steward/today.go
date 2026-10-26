package steward

import (
	"strings"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/i18n/prompts"
)

// N3 T4 管家建议: the steward advises on the 「今天」 decision queue during a review. The
// queue lives in internal/today (which reads the steward's status, so the steward cannot
// import it): the server wires a counter of the cards that still have no advice.

// SetTodayUnadvised supplies the keys of the 「今天」 cards without advice. A review with
// no changed work item still runs when one of them was never put before the steward
// (a card it already saw and chose to skip does not wake it again); nil (or an error)
// leaves the advice step in every review and never forces one.
func (s *Service) SetTodayUnadvised(fn func() ([]string, error)) { s.todayUnadvised = fn }

// kvTodayPresented holds the unadvised card keys the last review put before the steward.
const kvTodayPresented = "steward.today_presented"

// todayPending returns the unadvised cards (-1 when unknown), whether any of them is
// new to the steward, and the keys to remember once a review runs.
func (s *Service) todayPending() (pending int, fresh bool, keys []string) {
	if s.todayUnadvised == nil {
		return -1, false, nil
	}
	keys, err := s.todayUnadvised()
	if err != nil {
		return -1, false, nil
	}
	seen := map[string]bool{}
	for _, k := range strings.Split(s.kv(kvTodayPresented), "\n") {
		seen[k] = true
	}
	for _, k := range keys {
		if !seen[k] {
			fresh = true
			break
		}
	}
	return len(keys), fresh, keys
}

// rememberTodayPresented records the cards a review has put before the steward.
func (s *Service) rememberTodayPresented(keys []string) {
	if keys != nil {
		s.setKV(kvTodayPresented, strings.Join(keys, "\n"))
	}
}

// todaySection is the review step that asks for advice; empty when nothing waits.
func todaySection(lang i18n.Lang, step, pending int) string {
	if pending == 0 {
		return ""
	}
	out := prompts.MustRender(lang, prompts.NameStewardToday,
		prompts.StewardTodayData{Step: step, Pending: max(pending, 0)})
	return out + "\n"
}
