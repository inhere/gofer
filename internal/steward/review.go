package steward

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/i18n/prompts"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/settings"
	"github.com/inhere/gofer/internal/work"
)

// Review triggers.
const (
	TriggerDaily  = "daily"
	TriggerManual = "manual"
	TriggerEvent  = "event"
)

// ReviewOpts selects what to review.
type ReviewOpts struct {
	// Trigger is daily | manual | event ("" = manual).
	Trigger string
	// Force runs the review even when no work item changed since the last one.
	Force bool
}

// ReviewResult says what a review run did.
type ReviewResult struct {
	ReviewID int64 `json:"review_id"`
	// Skipped is true when nothing needed reviewing: no session was started.
	Skipped bool     `json:"skipped"`
	Reason  string   `json:"reason,omitempty"`
	JobID   string   `json:"job_id,omitempty"`
	Items   []string `json:"items,omitempty"`
	Events  int      `json:"events"`
}

// reviewItems picks the work items one review handles: the unfinished ones that changed
// since the cutoff plus the due ones (daily / manual), or the ones the pending events point
// at (event); most urgent first, capped at max. The second result is how many more there
// were.
func (s *Service) reviewItems(trigger string, cutoff int64, events []jobstore.StewardEvent, max int) ([]work.ItemView, int, error) {
	items, err := s.work.List(work.WorkListOptsOpen())
	if err != nil {
		return nil, 0, err
	}
	// Noise reduction (design 2026-10-20 §关键流程): items whose sessions were all
	// short-lived are not reviewed at all — no matter what the events say.
	items = s.work.DropShortItems(items, s.nowFn())
	evItems := map[string]bool{}
	for _, e := range events {
		if id := eventItemID(e); id != "" {
			evItems[id] = true
		}
	}
	var picked []work.ItemView
	for _, it := range items {
		switch trigger {
		case TriggerEvent:
			if evItems[it.ID] {
				picked = append(picked, it)
			}
		default:
			// The stored activity time: the session heartbeats the card shows are not "the
			// item changed" (those are what the session events cover).
			if it.WorkItem.LastActivityAt > cutoff || it.Due || evItems[it.ID] {
				picked = append(picked, it)
			}
		}
	}
	sortForPrime(picked)
	more := 0
	if max > 0 && len(picked) > max {
		more = len(picked) - max
		picked = picked[:max]
	}
	return picked, more, nil
}

// RunReview runs one review: it records the run, tells the (started on demand) steward
// session which work items to look at, and finishes the record in the background when the
// turn ends. Nothing happens — and no session starts — when nothing changed.
func (s *Service) RunReview(ctx context.Context, o ReviewOpts) (ReviewResult, error) {
	if o.Trigger == "" {
		o.Trigger = TriggerManual
	}
	c, err := s.ready()
	if err != nil {
		return ReviewResult{}, err
	}
	s.reviewMu.Lock()
	if s.reviewOn {
		s.reviewMu.Unlock()
		return ReviewResult{}, ErrBusy
	}
	s.reviewOn = true
	s.reviewMu.Unlock()
	release := func() {
		s.reviewMu.Lock()
		s.reviewOn = false
		s.reviewMu.Unlock()
	}

	now := s.nowFn()
	day := now.Format("2006-01-02")
	events, _ := s.store.PendingStewardEvents(200)
	items, more, err := s.reviewItems(o.Trigger, s.kvInt(kvReviewCutoff), events, c.MaxReviewItems())
	if err != nil {
		release()
		return ReviewResult{}, err
	}
	ids := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	pending, fresh, todayKeys := s.todayPending()
	mem, memFresh := s.memoryPending()
	if len(items) == 0 && !o.Force && !fresh && !memFresh {
		release()
		id, berr := s.store.BeginStewardReview(day, o.Trigger, nil)
		if berr == nil {
			_ = s.store.FinishStewardReview(id, jobstore.StewardReviewSkipped, "", "", s.reviewText("review.skipped"))
		}
		return ReviewResult{ReviewID: id, Skipped: true, Reason: s.reviewText("review.skipped")}, nil
	}
	notes, _ := s.NotesStatus()
	prompt := s.reviewPrompt(o.Trigger, day, items, more, events, notes.NeedSlim, now, pending, mem)

	rid, err := s.store.BeginStewardReview(day, o.Trigger, ids)
	if err != nil {
		release()
		return ReviewResult{}, err
	}
	s.rememberTodayPresented(todayKeys)
	s.rememberMemoryPresented(mem)
	turnBefore := 0
	s.askMu.Lock()
	s.mu.Lock()
	jobID, started, err := s.ensureSession(prompt)
	if err == nil && !started {
		if info, ok := s.host.Job(jobID); ok {
			turnBefore = info.TurnNo
		}
	}
	s.mu.Unlock()
	if err == nil && !started {
		err = s.say(ctx, jobID, prompt)
	}
	s.askMu.Unlock()
	if err != nil {
		_ = s.store.FinishStewardReview(rid, jobstore.StewardReviewFailed, "", jobID, err.Error())
		release()
		return ReviewResult{ReviewID: rid}, err
	}
	var evIDs []int64
	for _, e := range events {
		evIDs = append(evIDs, e.ID)
	}
	_ = s.store.MarkStewardEventsHandled(evIDs, now.Unix())

	if !s.spawn(func() {
		defer release()
		ok := s.waitTurnDone(jobID, turnBefore)
		end := s.nowFn()
		if ok {
			if o.Trigger != TriggerEvent {
				s.setKV(kvReviewCutoff, strconv.FormatInt(end.Unix(), 10))
			}
			_ = s.store.FinishStewardReview(rid, jobstore.StewardReviewDone, "", jobID, "")
		} else {
			_ = s.store.FinishStewardReview(rid, jobstore.StewardReviewFailed, "", jobID, s.reviewText("review.timeout"))
		}
		slog.Info("steward.review_done", "event", "steward.review_done", "review", rid, "trigger", o.Trigger, "ok", ok, "items", len(ids))
	}) { // closed: nobody will watch the turn
		_ = s.store.FinishStewardReview(rid, jobstore.StewardReviewFailed, "", jobID, s.reviewText("review.closed"))
		release()
	}
	return ReviewResult{ReviewID: rid, JobID: jobID, Items: ids, Events: len(events)}, nil
}

// waitTurnDone waits until the session finished the turn after `turnBefore` (it is between
// turns again), or the session is gone / the wait times out.
func (s *Service) waitTurnDone(jobID string, turnBefore int) bool {
	deadline := time.Now().Add(s.reviewTimeout)
	last := turnBefore
	for time.Now().Before(deadline) {
		info, ok := s.host.Job(jobID)
		if !ok {
			return last > turnBefore
		}
		last = info.TurnNo
		if info.AwaitingInput && info.TurnNo > turnBefore {
			return true
		}
		if !info.Live {
			return info.TurnNo > turnBefore
		}
		s.life.Lock()
		closing := s.closingCh()
		s.life.Unlock()
		select {
		case <-time.After(s.pollEvery):
		case <-closing:
			return false
		}
	}
	return false
}

// SetReviewSummary stores the steward's point of view on the running (or today's finished)
// review: what the daily digest appends as 管家点评.
func (s *Service) SetReviewSummary(text string) (jobstore.StewardReview, error) {
	return s.store.SetStewardReviewSummary(s.nowFn().Format("2006-01-02"), text)
}

// reviewPrompt is one review's whole message: the dynamic material the code always appends
// (which items, which events) and then the instruction (reviewInstruction). Only the
// instruction is replaceable — the material must keep matching what was picked.
func (s *Service) reviewPrompt(trigger, day string, items []work.ItemView, more int, events []jobstore.StewardEvent, slim bool, now time.Time, todayPending int, mem *MemoryHygiene) string {
	lang := s.language()
	var b strings.Builder
	b.WriteString(stewardText(lang, "review.heading", "label", reviewTriggerLabel(lang, trigger), "day", day) + "\n\n")
	if len(items) == 0 {
		b.WriteString(stewardText(lang, "review.no_items") + "\n")
	} else {
		b.WriteString(stewardText(lang, "review.items_scope", "n", len(items)))
		if more > 0 {
			b.WriteString(stewardText(lang, "review.items_more", "n", more))
		}
		b.WriteString(stewardText(lang, "review.items_colon") + "\n")
		for _, it := range items {
			b.WriteString(itemLine(lang, it, now) + "\n")
		}
	}
	if len(events) > 0 {
		b.WriteString("\n" + stewardText(lang, "review.events_heading") + "\n")
		for i, e := range events {
			if i >= 30 {
				b.WriteString(stewardText(lang, "review.events_more", "n", len(events)-i) + "\n")
				break
			}
			b.WriteString(stewardText(lang, "review.event_line", "kind", e.Kind, "detail", runesCap(e.Detail, 120)) + "\n")
		}
	}
	b.WriteString("\n")
	b.WriteString(s.reviewInstruction(slim, todayPending, mem))
	return b.String()
}

// reviewInstruction is the instruction part of a review: the prompt.steward.review setting
// (else defaultReviewSteps), the step-numbered sections the code owns (the 「今天」 advice
// and the memory hygiene step), the closing step, and then the operator's extra block
// (prompt.steward.review.extra).
func (s *Service) reviewInstruction(slim bool, todayPending int, mem *MemoryHygiene) string {
	body := strings.TrimRight(settings.ChoosePrompt(prompts.MustRender(s.language(), prompts.NameStewardReview, struct{ Slim bool }{Slim: slim}), s.settingString(settings.KeyPromptStewardReview)), "\n")
	var b strings.Builder
	b.WriteString(body)
	b.WriteString("\n")
	step := nextStepNumber(body)
	if sec := todaySection(s.language(), step, todayPending); sec != "" {
		b.WriteString(sec)
		step++
	}
	if sec := memorySection(s.language(), step, mem); sec != "" {
		b.WriteString(sec)
		step++
	}
	b.WriteString(stewardText(s.language(), "review.summary_step", "step", step) + "\n")
	b.WriteString("\n" + stewardText(s.language(), "review.closing"))
	return settings.ComposePrompt(b.String(), s.settingString(settings.KeyPromptStewardReviewExtra))
}

// reviewTriggerLabel names a review trigger (daily / manual / event) in lang.
func reviewTriggerLabel(lang i18n.Lang, trigger string) string {
	return labeled(lang, "review", trigger)
}

// reviewText is a steward review catalog entry in the service's language.
func (s *Service) reviewText(key string) string {
	return stewardText(s.language(), key)
}

// nextStepNumber is the number the code-owned steps continue from: one past the highest
// numbered step of the (default or replaced) instruction, so a replacement that numbers its
// own steps keeps the appended sections in sequence, and one that has no steps still gets a
// sane start.
func nextStepNumber(body string) int {
	highest := 0
	for _, ln := range strings.Split(body, "\n") {
		head, _, ok := strings.Cut(strings.TrimSpace(ln), " ")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSuffix(head, "."))
		if err == nil && n > highest {
			highest = n
		}
	}
	return highest + 1
}

// defaultReviewSteps is the instruction a review gets when prompt.steward.review holds
// nothing (rewritten 2026-10-20 决策 7: test / try-out work does not deserve advice, and
// every suggestion names a next action).
func defaultReviewSteps(slim bool) string {
	return prompts.MustRender(i18n.ZhCN, prompts.NameStewardReview, struct{ Slim bool }{Slim: slim})
}
