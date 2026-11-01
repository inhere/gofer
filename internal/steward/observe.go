package steward

import (
	"log/slog"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/settings"
)

// Observe-only (design 2026-11-01 §4.5, dsh's `observe`): the steward's PROACTIVE behaviour —
// the daily review and the event wake-up — only records what it would have done. While such a
// review is in flight and steward.act_proactively is off, the server does not apply the steward's
// effectful writes (state changes, journal lines, merge / memory suggestions, advice, messages to
// sessions, tidy-up jobs): the HTTP gate calls Observe instead, which stores the request for a
// person to read (`gofer steward observations`). A review a person starts (`steward review`) and
// a question a person asks (`steward ask`) always act.

// proactiveTrigger reports whether a review trigger is that count as the steward's own initiative.
func proactiveTrigger(trigger string) bool { return trigger == TriggerDaily || trigger == TriggerEvent }

// actsProactively reads steward.act_proactively; a read failure keeps the safe default (observe).
func (s *Service) actsProactively() bool {
	if s.settings != nil {
		if g, ok := s.settings.(interface{ GetBool(string) (bool, error) }); ok {
			v, err := g.GetBool(settings.KeyStewardActProactively)
			if err == nil {
				return v
			}
			slog.Warn("steward.settings_read_failed", "event", "steward.settings_read_failed", "key", settings.KeyStewardActProactively, "err", err)
		}
	}
	return false
}

// Observing reports whether the steward's effectful writes are recorded instead of applied right
// now: a proactive review is in flight and the steward may not act on its own.
func (s *Service) Observing() bool {
	s.reviewMu.Lock()
	on, trigger := s.reviewOn, s.reviewTrigger
	s.reviewMu.Unlock()
	return on && proactiveTrigger(trigger) && !s.actsProactively()
}

// Observe records one write the steward asked for during an observe-only review. It reports
// false (and records nothing) when the steward is not observing, so the caller applies the write.
func (s *Service) Observe(method, path, body string) bool {
	if !s.Observing() {
		return false
	}
	s.reviewMu.Lock()
	rid := s.reviewID
	s.reviewMu.Unlock()
	if err := s.store.AddStewardObservation(rid, method, path, body); err != nil {
		slog.Warn("steward.observe_failed", "event", "steward.observe_failed", "err", err)
	}
	return true
}

// Observations lists what the steward would have done, newest first.
func (s *Service) Observations(limit int) ([]jobstore.StewardObservation, error) {
	return s.store.ListStewardObservations(limit)
}
