package steward

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// Events are cheap notes of things the steward should look at — a session of an open item
// went offline / ended, a reminder came due, drafts piled up. They are only noted by
// default (the next review picks them up); steward.event_wake additionally lets them wake
// the steward, batched and throttled so a flapping session cannot start a model session
// every minute.

const eventSep = "|"

func eventItemID(e jobstore.StewardEvent) string {
	id, _, ok := strings.Cut(e.Ref, eventSep)
	if !ok || id == "*" {
		return ""
	}
	return id
}

// NoteDue is the work service's due hook: a reminder / park deadline was announced.
func (s *Service) NoteDue(w jobstore.WorkItem, reason string, at int64) {
	if s == nil {
		return
	}
	if c, _ := s.cfg(); !c.Enabled {
		return
	}
	ref := strings.Join([]string{w.ID, "due", strconv.FormatInt(at, 10)}, eventSep)
	if _, err := s.store.AddStewardEvent(jobstore.StewardEventDue, ref, fmt.Sprintf("工作项 %s「%s」%s", w.ID, w.Title, reason)); err != nil {
		slog.Warn("steward.event_failed", "event", "steward.event_failed", "err", err)
	}
}

// scanEvents notes the events that are visible only by looking: sessions of open items
// that went offline / ended, and a pile of unsorted drafts.
func (s *Service) scanEvents(now time.Time) {
	items, err := s.store.ListWorkItems(jobstore.WorkListOpts{Limit: 2000})
	if err != nil {
		return
	}
	ids := make([]string, 0, len(items))
	byID := map[string]jobstore.WorkItem{}
	drafts := 0
	for _, it := range items {
		ids = append(ids, it.ID)
		byID[it.ID] = it
		if it.Unsorted {
			drafts++
		}
	}
	cur, err := s.store.CurrentWorkSessionIDs(ids)
	if err != nil {
		return
	}
	for id, sids := range cur {
		for _, sid := range sids {
			a, ok, gerr := s.store.GetAgentSession(sid)
			if gerr != nil || !ok {
				continue
			}
			if a.State != jobstore.SessionOffline && a.State != jobstore.SessionEnded {
				continue
			}
			ref := strings.Join([]string{id, sid, a.State, strconv.FormatInt(a.LastSeenAt, 10)}, eventSep)
			_, _ = s.store.AddStewardEvent(jobstore.StewardEventSession, ref,
				fmt.Sprintf("工作项 %s「%s」的会话 %s 已%s", id, byID[id].Title, shortID(sid), map[string]string{
					jobstore.SessionOffline: "离线", jobstore.SessionEnded: "结束"}[a.State]))
		}
	}
	if drafts >= config.StewardEventDraftsThreshold {
		_, _ = s.store.AddStewardEvent(jobstore.StewardEventDrafts, "*"+eventSep+"drafts"+eventSep+now.Format("2006-01-02"),
			fmt.Sprintf("有 %d 个未整理的草稿工作项", drafts))
	}
}

func shortID(sid string) string {
	if len(sid) > 12 {
		return sid[:12]
	}
	return sid
}

// maybeWakeOnEvents starts an event-driven review when it is allowed and due: the switch
// is on, events are pending and the throttle window since the last wake has passed.
func (s *Service) maybeWakeOnEvents(ctx context.Context, now time.Time, c config.StewardConfig) {
	if !c.EventWake || c.AgentName() == "" {
		return
	}
	pending, err := s.store.PendingStewardEvents(1)
	if err != nil || len(pending) == 0 {
		return
	}
	if last := s.kvInt(kvLastEventWake); last > 0 && now.Sub(time.Unix(last, 0)) < c.EventThrottle() {
		return
	}
	s.setKV(kvLastEventWake, strconv.FormatInt(now.Unix(), 10))
	if _, err := s.RunReview(ctx, ReviewOpts{Trigger: TriggerEvent}); err != nil {
		slog.Info("steward.event_wake_skipped", "event", "steward.event_wake_skipped", "err", err)
	}
}

// ---------------------------------------------------------------- tick / run

// Tick runs one scan: reconcile the session with the configuration, note events, run the
// daily review when it is time, and wake on events when allowed.
func (s *Service) Tick(ctx context.Context, now time.Time) {
	s.tickMu.Lock()
	defer s.tickMu.Unlock()
	s.Reconcile()
	c, w := s.cfg()
	if !c.Enabled {
		return
	}
	s.scanEvents(now)
	if c.AgentName() == "" {
		return
	}
	h, m := c.ReviewClock(w)
	todayAt := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
	day := now.Format("2006-01-02")
	if !now.Before(todayAt) && now.Before(todayAt.Add(reviewWindow)) && s.kv(kvLastReviewDay) != day {
		s.setKV(kvLastReviewDay, day)
		if _, err := s.RunReview(ctx, ReviewOpts{Trigger: TriggerDaily}); err != nil {
			slog.Warn("steward.daily_review_failed", "event", "steward.daily_review_failed", "err", err)
		}
		return
	}
	s.maybeWakeOnEvents(ctx, now, c)
}

// Run ticks until stop is closed. A review the server was killed in the middle of is
// closed as failed on the first pass.
func (s *Service) Run(stop <-chan struct{}) {
	s.life.Lock()
	if s.closed {
		s.life.Unlock()
		return
	}
	closing := s.closingCh()
	s.runs.Add(1)
	s.life.Unlock()
	defer s.runs.Done()
	_ = s.store.FailStaleStewardReviews(s.nowFn().Add(-time.Minute).Unix())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-stop:
		case <-closing:
		case <-ctx.Done(): // Run returned
		}
		cancel()
	}()
	t := time.NewTicker(tickEvery)
	defer t.Stop()
	s.Tick(ctx, s.nowFn())
	for {
		select {
		case <-stop:
			return
		case <-closing:
			return
		case <-t.C:
			s.Tick(ctx, s.nowFn())
		}
	}
}
