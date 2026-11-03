package job

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/i18n/prompts"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/msgprefix"
)

// The child-receipt say path (design 2026-11-03-resident-job-child-wakeup T5).
//
// The fixture is the remote-say one (session_say_errors_test.go): a live session entry
// whose every say is recorded by recordingSessionSender, so "did the parent get ONE
// message and what did it say" is an assertion instead of a guess.

// wakeupSessionFixture builds a service with one live REMOTE session (the parent) and
// returns it with the sender that records its says.
func wakeupSessionFixture(t *testing.T) (*Service, *recordingSessionSender, *jobEntry) {
	t.Helper()
	s, sender := remoteSayService(t, 0)
	parent := addRemoteSaySession(t, s, "parent-job", StatusAwaitingInput, 1)
	return s, sender, parent
}

// addChildJob stores a finished child job row (the receipt reports its state).
func addChildJob(t *testing.T, s *Service, id, status string, exit int, title string) JobResult {
	t.Helper()
	res := JobResult{
		ID: id, ProjectKey: "self", Agent: "exec", Runner: "local", Status: status,
		ExitCode: exit, Title: title, Cwd: ".", ResultDir: t.TempDir(),
	}
	if err := s.persist(res); err != nil {
		t.Fatalf("persist child %s: %v", id, err)
	}
	return res
}

// addChildReceipt stores the wakeup row a child registers on its parent.
func addChildReceipt(t *testing.T, s *Service, w jobstore.WakeupRecord) jobstore.WakeupRecord {
	t.Helper()
	if w.ID == "" {
		w.ID = "wk-" + RandomSuffix()
	}
	if w.Kind == "" {
		w.Kind = jobstore.WakeupKindEvent
	}
	if w.Mode == "" {
		w.Mode = jobstore.WakeupModeContinuous
	}
	if w.EventTypesJSON == "" {
		w.EventTypesJSON = jobstore.EncodeStringList([]string{EventJobTerminal})
	}
	w.Enabled = 1
	w.CreatedAt = s.nowFn().Unix()
	w.ExpiresAt = s.nowFn().Add(time.Hour).Unix()
	if err := s.meta.InsertWakeup(w); err != nil {
		t.Fatalf("insert wakeup: %v", err)
	}
	return w
}

// childWakeRow reloads a wakeup row (nil when it is gone).
func childWakeRow(t *testing.T, s *Service, id string) jobstore.WakeupRecord {
	t.Helper()
	w, ok, err := s.meta.GetWakeup(id)
	if err != nil {
		t.Fatalf("get wakeup %s: %v", id, err)
	}
	if !ok {
		t.Fatalf("wakeup %s is gone", id)
	}
	return w
}

// sayTexts returns the prompts of every say the sender was asked to send.
func sayTexts(sender *recordingSessionSender) []string {
	sent := sender.sentCommands()
	out := make([]string, 0, len(sent))
	for _, c := range sent {
		out = append(out, c.prompt)
	}
	return out
}

// TestWakeupSessionDeliversWhenIdle: an idle resident parent is woken at once, with a
// receipt naming the child, its state, its exit code and the command that shows the
// result.
func TestWakeupSessionDeliversWhenIdle(t *testing.T) {
	t.Parallel()
	s, sender, parent := wakeupSessionFixture(t)
	addChildJob(t, s, "child-1", StatusDone, 0, "a child title")
	w := addChildReceipt(t, s, jobstore.WakeupRecord{JobID: parent.result.ID, FilterJobID: "child-1", AutoChild: 1})

	s.deliverChildReceipt(w, EventJobTerminal)

	texts := sayTexts(sender)
	assert.Eq(t, 1, len(texts))
	say := texts[0]
	assert.True(t, msgprefix.IsSystem(say), "say = %q, want a gofer system message", say)
	for _, want := range []string{"child-1", StatusDone, "gofer job logs child-1", "0"} {
		assert.True(t, strings.Contains(say, want), "say = %q, want it to contain %q", say, want)
	}
	// The receipt echoes nothing of the child beyond identity and outcome.
	assert.False(t, strings.Contains(say, "a child title"), "say = %q, want no child title", say)
	// The sentinel was written first and the batch remembers which row it carried.
	if row := childWakeRow(t, s, w.ID); row.ContinuationJobID != jobstore.WakeupPendingSay {
		t.Fatalf("continuation = %q, want the pending-say sentinel", row.ContinuationJobID)
	}
}

// TestWakeupSessionParksWhileBusy: a receipt that arrives mid-turn does NOT interrupt
// the turn; it waits as the pending sentinel and is delivered when the session comes
// back to awaiting_input.
func TestWakeupSessionParksWhileBusy(t *testing.T) {
	t.Parallel()
	s, sender, parent := wakeupSessionFixture(t)
	parent.mu.Lock()
	parent.result.Status = StatusRunning
	parent.mu.Unlock()
	addChildJob(t, s, "child-busy", StatusFailed, 3, "")
	w := addChildReceipt(t, s, jobstore.WakeupRecord{JobID: parent.result.ID, FilterJobID: "child-busy", AutoChild: 1})

	s.deliverChildReceipt(w, EventJobTerminal)
	assert.Eq(t, 0, len(sayTexts(sender)))
	if row := childWakeRow(t, s, w.ID); row.ContinuationJobID != jobstore.WakeupPendingSay {
		t.Fatalf("continuation = %q, want the pending-say sentinel", row.ContinuationJobID)
	}

	// The turn ends: the session reports awaiting_input and the parked receipt goes out.
	parent.mu.Lock()
	parent.result.Status = StatusAwaitingInput
	parent.result.TurnNo = 2
	parent.mu.Unlock()
	s.onParentAwaitingInput(parent.result.ID)

	texts := sayTexts(sender)
	assert.Eq(t, 1, len(texts))
	assert.True(t, strings.Contains(texts[0], "child-busy"), "say = %q", texts[0])
}

// TestWakeupSessionMergesChildren: two children finishing while the parent is busy are
// ONE message — a session takes one prompt at a time, so two receipts must not stack.
func TestWakeupSessionMergesChildren(t *testing.T) {
	t.Parallel()
	s, sender, parent := wakeupSessionFixture(t)
	parent.mu.Lock()
	parent.result.Status = StatusRunning
	parent.mu.Unlock()
	addChildJob(t, s, "child-a", StatusDone, 0, "")
	addChildJob(t, s, "child-b", StatusFailed, 2, "")
	wA := addChildReceipt(t, s, jobstore.WakeupRecord{JobID: parent.result.ID, FilterJobID: "child-a", AutoChild: 1})
	wB := addChildReceipt(t, s, jobstore.WakeupRecord{JobID: parent.result.ID, FilterJobID: "child-b", AutoChild: 1})

	s.deliverChildReceipt(wA, EventJobTerminal)
	s.deliverChildReceipt(wB, EventJobTerminal)
	assert.Eq(t, 0, len(sayTexts(sender)), "a busy session was interrupted")

	parent.mu.Lock()
	parent.result.Status = StatusAwaitingInput
	parent.result.TurnNo = 2
	parent.mu.Unlock()
	s.onParentAwaitingInput(parent.result.ID)

	texts := sayTexts(sender)
	assert.Eq(t, 1, len(texts), "receipts = %v, want one merged message", texts)
	assert.True(t, strings.Contains(texts[0], "child-a") && strings.Contains(texts[0], "child-b"),
		"say = %q, want both children", texts[0])
}

// TestWakeupSessionKeepsPendingOnRetryableErrors: a say refused for a reason that will
// pass later ("a person just spoke", "the worker is away", "the server is draining")
// keeps the receipt pending — it is never dropped and never reported as failed.
func TestWakeupSessionKeepsPendingOnRetryableErrors(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, s *Service, sender *recordingSessionSender, parent *jobEntry)
	}{
		{"input already queued", func(_ *testing.T, _ *Service, _ *recordingSessionSender, parent *jobEntry) {
			parent.mu.Lock()
			parent.sessionCommandPending = true
			parent.mu.Unlock()
		}},
		{"worker offline", func(_ *testing.T, _ *Service, sender *recordingSessionSender, _ *jobEntry) {
			sender.online = false
		}},
		{"upgrade draining", func(_ *testing.T, s *Service, _ *recordingSessionSender, _ *jobEntry) {
			s.admissionMu.Lock()
			s.admissionClosed = true
			s.admissionMu.Unlock()
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, sender, parent := wakeupSessionFixture(t)
			addChildJob(t, s, "child-err", StatusDone, 0, "")
			w := addChildReceipt(t, s, jobstore.WakeupRecord{JobID: parent.result.ID, FilterJobID: "child-err", AutoChild: 1})
			tc.setup(t, s, sender, parent)

			s.deliverChildReceipt(w, EventJobTerminal)

			assert.Eq(t, 0, len(sayTexts(sender)), "a refused say was not retried silently")
			if row := childWakeRow(t, s, w.ID); row.ContinuationJobID != jobstore.WakeupPendingSay {
				t.Fatalf("continuation = %q, want the pending-say sentinel", row.ContinuationJobID)
			}
			assert.Eq(t, 1, row(t, s, w.ID).Enabled, "the receipt was retired instead of kept")
		})
	}
}

// row is childWakeRow under a shorter name for the table above.
func row(t *testing.T, s *Service, id string) jobstore.WakeupRecord {
	t.Helper()
	return childWakeRow(t, s, id)
}

// TestWakeupSessionAppendsManualInstruction: a manually registered wakeup's instruction
// rides the receipt as the caller's own text, after the fixed template.
func TestWakeupSessionAppendsManualInstruction(t *testing.T) {
	t.Parallel()
	s, sender, parent := wakeupSessionFixture(t)
	addChildJob(t, s, "child-manual", StatusDone, 0, "")
	w := addChildReceipt(t, s, jobstore.WakeupRecord{
		JobID: parent.result.ID, FilterJobID: "child-manual", Instruction: "then run the release check",
	})

	s.deliverChildReceipt(w, EventJobTerminal)

	texts := sayTexts(sender)
	assert.Eq(t, 1, len(texts))
	assert.True(t, strings.Contains(texts[0], "then run the release check"), "say = %q", texts[0])
	assert.True(t, strings.Index(texts[0], "child-manual") < strings.Index(texts[0], "then run the release check"),
		"say = %q, want the instruction after the template", texts[0])
}

// TestWakeupSessionNeedsInputTellsAPerson: the "needs a person" class says out loud that
// the parent cannot answer for the child, in every language, and is rate limited to one
// receipt per child per minute.
func TestWakeupSessionNeedsInputTellsAPerson(t *testing.T) {
	t.Parallel()
	// The template half: both languages carry the "you cannot answer for it" line.
	for lang, want := range map[i18n.Lang]string{i18n.ZhCN: "请转告人", i18n.EN: "tell a person"} {
		text := prompts.MustRender(lang, prompts.NameChildReceipt, prompts.ChildReceiptData{
			Children:   []prompts.ChildReceiptLine{{JobID: "child-x", Status: StatusAwaitingApproval, NeedsInput: true}},
			NeedsInput: true,
		})
		assert.True(t, strings.Contains(text, want), "%s receipt = %q, want %q", lang, text, want)
	}

	s, sender, parent := wakeupSessionFixture(t)
	addChildJob(t, s, "child-ask", StatusAwaitingApproval, 0, "")
	w := addChildReceipt(t, s, jobstore.WakeupRecord{
		JobID: parent.result.ID, FilterJobID: "child-ask", AutoChild: 1,
		EventTypesJSON: jobstore.EncodeStringList([]string{EventJobTerminal, EventJobAwaitingApproval}),
	})

	s.deliverChildReceipt(w, EventJobAwaitingApproval)
	texts := sayTexts(sender)
	assert.Eq(t, 1, len(texts))
	assert.True(t, strings.Contains(texts[0], "child-ask"), "say = %q", texts[0])
	if lang := s.notifyLang("self"); lang == i18n.ZhCN {
		assert.True(t, strings.Contains(texts[0], "请转告人"), "say = %q, want the relay line", texts[0])
	}

	// The same child raises another approval inside the window: no second turn.
	s.deliverChildReceipt(w, EventJobAwaitingApproval)
	assert.Eq(t, 1, len(sayTexts(sender)), "a burst of approvals each woke the parent")
	if row := childWakeRow(t, s, w.ID); row.ContinuationJobID != jobstore.WakeupPendingSay {
		t.Fatalf("continuation = %q, want the rate-limited receipt kept as pending", row.ContinuationJobID)
	}
}

// TestWakeupSessionConfirmsTerminalAndDisables: coming back to awaiting_input ON A
// LATER TURN proves the receipt was consumed. A terminal receipt then retires its row;
// nothing is re-sent.
func TestWakeupSessionConfirmsTerminalAndDisables(t *testing.T) {
	t.Parallel()
	s, sender, parent := wakeupSessionFixture(t)
	addChildJob(t, s, "child-done", StatusDone, 0, "")
	w := addChildReceipt(t, s, jobstore.WakeupRecord{JobID: parent.result.ID, FilterJobID: "child-done", AutoChild: 1})

	s.deliverChildReceipt(w, EventJobTerminal)
	assert.Eq(t, 1, len(sayTexts(sender)))

	// The session worked the receipt through and is idle again on turn 2.
	s.applyRemoteSessionState(parent, WorkerInflightJob{
		JobID: parent.result.ID, SessionStatus: StatusAwaitingInput, TurnNo: 2,
	}, false)

	got := childWakeRow(t, s, w.ID)
	assert.Eq(t, "", got.ContinuationJobID, "the confirmed batch kept its sentinel")
	assert.Eq(t, 0, got.Enabled, "the delivered terminal receipt stayed armed")
	assert.Eq(t, int64(1), got.FiredCount, "a delivered receipt counts as one fire")
	assert.Eq(t, 1, len(sayTexts(sender)), "the confirmed receipt was sent again")
}

// TestWakeupSessionOnceRowConsumed: a manual event wakeup is `once` by default, so its
// first confirmed delivery consumes it even when the child is not terminal yet.
func TestWakeupSessionOnceRowConsumed(t *testing.T) {
	t.Parallel()
	s, sender, parent := wakeupSessionFixture(t)
	addChildJob(t, s, "child-once", StatusAwaitingApproval, 0, "")
	w := addChildReceipt(t, s, jobstore.WakeupRecord{
		JobID: parent.result.ID, FilterJobID: "child-once", Mode: jobstore.WakeupModeOnce,
	})

	s.deliverChildReceipt(w, EventJobTerminal)
	assert.Eq(t, 1, len(sayTexts(sender)))
	s.applyRemoteSessionState(parent, WorkerInflightJob{
		JobID: parent.result.ID, SessionStatus: StatusAwaitingInput, TurnNo: 2,
	}, false)

	got := childWakeRow(t, s, w.ID)
	assert.Eq(t, 0, got.Enabled, "a once row survived its confirmed delivery")
	assert.Eq(t, "", got.ContinuationJobID)
}

// TestWakeupSessionKeepsSentinelWhenChildMovedOn: the child changed state between the
// send and the confirmation (an approval receipt was on its way when the child ended),
// so the row keeps its sentinel and the newer outcome fires again instead of being lost.
func TestWakeupSessionKeepsSentinelWhenChildMovedOn(t *testing.T) {
	t.Parallel()
	s, sender, parent := wakeupSessionFixture(t)
	addChildJob(t, s, "child-move", StatusAwaitingApproval, 0, "")
	w := addChildReceipt(t, s, jobstore.WakeupRecord{
		JobID: parent.result.ID, FilterJobID: "child-move", AutoChild: 1,
		EventTypesJSON: jobstore.EncodeStringList([]string{EventJobTerminal, EventJobAwaitingApproval}),
	})
	s.deliverChildReceipt(w, EventJobAwaitingApproval)
	assert.Eq(t, 1, len(sayTexts(sender)))

	// The child reached its terminal state after the receipt went out.
	child, ok := s.Get("child-move")
	assert.True(t, ok)
	child.Status = StatusDone
	if err := s.persist(child); err != nil {
		t.Fatalf("persist moved child: %v", err)
	}
	s.applyRemoteSessionState(parent, WorkerInflightJob{
		JobID: parent.result.ID, SessionStatus: StatusAwaitingInput, TurnNo: 2,
	}, false)

	got := childWakeRow(t, s, w.ID)
	assert.Eq(t, 1, got.Enabled, "the row was retired although its child moved on")
	// The newer outcome still reaches the parent: the row fires again.
	s.deliverChildReceipt(got, EventJobTerminal)
	texts := sayTexts(sender)
	assert.Eq(t, 2, len(texts))
	assert.True(t, strings.Contains(texts[1], StatusDone), "say = %q, want the new state", texts[1])
}

// TestWakeupSessionTurnDiedIsNotDelivered: a turn that started but never came back to
// awaiting_input is NOT a delivered receipt — the turn counter alone would say it was.
func TestWakeupSessionTurnDiedIsNotDelivered(t *testing.T) {
	t.Parallel()
	s, sender, parent := wakeupSessionFixture(t)
	addChildJob(t, s, "child-died", StatusDone, 0, "")
	w := addChildReceipt(t, s, jobstore.WakeupRecord{JobID: parent.result.ID, FilterJobID: "child-died", AutoChild: 1})
	s.deliverChildReceipt(w, EventJobTerminal)

	// The turn started (counter moved) and the job then failed: no idle transition.
	parent.mu.Lock()
	parent.result.TurnNo = 2
	parent.result.Status = StatusFailed
	parent.mu.Unlock()
	s.onParentAwaitingInput(parent.result.ID)

	got := childWakeRow(t, s, w.ID)
	assert.Eq(t, jobstore.WakeupPendingSay, got.ContinuationJobID, "a dead turn was counted as a delivery")
	assert.Eq(t, 1, got.Enabled)
	assert.Eq(t, 1, len(sayTexts(sender)), "the undelivered receipt was sent again")
}

// TestWakeupSessionTimerKeepsOldPath: routing only intercepts EVENT wakeups. A timer on
// a live session keeps the historical behaviour (a continuation, never a say).
func TestWakeupSessionTimerKeepsOldPath(t *testing.T) {
	t.Parallel()
	s, sender, parent := wakeupSessionFixture(t)
	w := addChildReceipt(t, s, jobstore.WakeupRecord{
		JobID: parent.result.ID, Kind: jobstore.WakeupKindAt, At: s.nowFn().Unix() + 60,
	})

	if _, ok := s.residentWakeupTarget(w); ok {
		t.Fatal("a timer wakeup was routed to the say path")
	}
	s.dispatchWakeup(w, "timer")
	time.Sleep(50 * time.Millisecond) // the historical branch fires on its own goroutine
	assert.Eq(t, 0, len(sayTexts(sender)), "a timer wakeup spoke into the session")
	if got := childWakeRow(t, s, w.ID); got.ContinuationJobID == jobstore.WakeupPendingSay {
		t.Fatal("a timer wakeup was parked as a pending receipt")
	}
}

// TestWakeupSessionManualOnEndedParentKeepsBaseline: a manually registered event wakeup
// whose target already ended is NOT turned into a receipt — the baseline resume/rebuild
// path is what it has always been.
func TestWakeupSessionManualOnEndedParentKeepsBaseline(t *testing.T) {
	t.Parallel()
	s, sender, parent := wakeupSessionFixture(t)
	parent.mu.Lock()
	parent.result.Status = StatusDone
	parent.mu.Unlock()
	addChildJob(t, s, "child-manual-end", StatusDone, 0, "")
	w := addChildReceipt(t, s, jobstore.WakeupRecord{
		JobID: parent.result.ID, FilterJobID: "child-manual-end", Mode: jobstore.WakeupModeOnce,
	})

	res, resident := s.residentWakeupTarget(w)
	assert.True(t, resident, "the ended parent is still a resident session job")
	assert.False(t, s.routeChildReceipt(w, EventJobTerminal, res), "a manual wakeup on an ended job changed path")
	assert.Eq(t, 0, len(sayTexts(sender)))
	if got := childWakeRow(t, s, w.ID); got.ContinuationJobID != "" || got.Enabled != 1 {
		t.Fatalf("the manual wakeup was touched: continuation=%q enabled=%d", got.ContinuationJobID, got.Enabled)
	}
}

// TestWakeupSessionParentEndReportsOnce: the parent going away retires its automatic
// receipts and reports each one to a person exactly once (job.child_wake_failed), while
// a manual receipt waiting for delivery keeps the event it always had.
func TestWakeupSessionParentEndReportsOnce(t *testing.T) {
	t.Parallel()
	s, sender, parent := wakeupSessionFixture(t)
	addChildJob(t, s, "child-r1", StatusDone, 0, "")
	addChildJob(t, s, "child-r2", StatusRunning, 0, "")
	auto := addChildReceipt(t, s, jobstore.WakeupRecord{JobID: parent.result.ID, FilterJobID: "child-r1", AutoChild: 1})
	auto2 := addChildReceipt(t, s, jobstore.WakeupRecord{JobID: parent.result.ID, FilterJobID: "child-r2", AutoChild: 1})
	manual := addChildReceipt(t, s, jobstore.WakeupRecord{JobID: parent.result.ID, FilterJobID: "child-r1"})
	if err := s.meta.MarkWakeupPendingSay(manual.ID); err != nil {
		t.Fatalf("park the manual receipt: %v", err)
	}

	s.endParentChildWakes(parent.result.ID, childWakeReasonParentEnded)
	// A second end (a review decision after needs_review, a takeover) must not repeat it.
	s.endParentChildWakes(parent.result.ID, childWakeReasonParentEnded)

	assert.Eq(t, 2, countEvents(eventTypes(t, s, parent.result.ID), EventJobChildWakeFailed),
		"one report per automatic receipt, not one per row")
	assert.Eq(t, 1, countEvents(eventTypes(t, s, parent.result.ID), EventJobWakeupFailed))
	for _, id := range []string{auto.ID, auto2.ID, manual.ID} {
		if got := childWakeRow(t, s, id); got.Enabled != 0 {
			t.Fatalf("wakeup %s stayed armed after the parent ended", id)
		}
	}
	assert.Eq(t, 0, len(sayTexts(sender)), "a receipt was spoken into a parent that is gone")
	// The detail names the child, never its title or output.
	events, err := s.ListJobEvents(parent.result.ID, 0)
	if err != nil {
		t.Fatalf("ListJobEvents: %v", err)
	}
	for _, ev := range events {
		if ev.Type != EventJobChildWakeFailed {
			continue
		}
		assert.True(t, strings.Contains(ev.Detail, "child_job_id"), "detail = %q", ev.Detail)
		assert.False(t, strings.Contains(ev.Detail, "title"), "detail carried a title: %q", ev.Detail)
		// The reason is a MACHINE value the inbox and the notification text map to their own
		// wording; a gofer sentence here would surface verbatim in the card.
		assert.True(t, strings.Contains(ev.Detail, `"reason":"`+childWakeReasonParentEnded+`"`),
			"detail = %q, want reason %q", ev.Detail, childWakeReasonParentEnded)
	}
}

// TestWakeupSessionConcurrentTriggersSendOneBatch: two children finishing at the same
// moment produce ONE say — the per-parent receipt lock covers read, merge and send.
func TestWakeupSessionConcurrentTriggersSendOneBatch(t *testing.T) {
	t.Parallel()
	s, sender, parent := wakeupSessionFixture(t)
	addChildJob(t, s, "child-c1", StatusDone, 0, "")
	addChildJob(t, s, "child-c2", StatusDone, 0, "")
	wA := addChildReceipt(t, s, jobstore.WakeupRecord{JobID: parent.result.ID, FilterJobID: "child-c1", AutoChild: 1})
	wB := addChildReceipt(t, s, jobstore.WakeupRecord{JobID: parent.result.ID, FilterJobID: "child-c2", AutoChild: 1})

	var wg sync.WaitGroup
	for _, w := range []jobstore.WakeupRecord{wA, wB} {
		wg.Add(1)
		go func(w jobstore.WakeupRecord) {
			defer wg.Done()
			s.deliverChildReceipt(w, EventJobTerminal)
		}(w)
	}
	wg.Wait()

	texts := sayTexts(sender)
	if len(texts) != 1 {
		t.Fatalf("says = %v, want exactly one message for the batch", texts)
	}
	// Whatever could not be merged (the say slot was taken by the batch that won the
	// lock) is still a receipt waiting for delivery, never a dropped one.
	for _, w := range []jobstore.WakeupRecord{wA, wB} {
		got := childWakeRow(t, s, w.ID)
		if got.ContinuationJobID != "" && got.ContinuationJobID != jobstore.WakeupPendingSay {
			t.Fatalf("wakeup %s continuation = %q, want delivered or pending", w.ID, got.ContinuationJobID)
		}
	}
}

// seedPendingInteraction stores one unanswered interaction on a child job.
func seedPendingInteraction(t *testing.T, s *Service, id, childID string) {
	t.Helper()
	if err := s.meta.UpsertInteraction(jobstore.InteractionRecord{
		ID: id, JobID: childID, Type: InteractionTypePermission, Prompt: "allow?",
		Status: InteractionPending, CreatedAt: s.nowFn().Unix(),
	}); err != nil {
		t.Fatalf("seed interaction %s: %v", id, err)
	}
}

// TestWakeupSessionRateLimitKeepsSentinel: a second "needs a person" receipt for the same
// child inside the one-minute window is NOT spoken, and — the point of the test — it stays
// durable as the pending sentinel. Without the sentinel the receipt would be lost for
// good: the first one was confirmed, so nothing is left to retry (plan T5 限频).
func TestWakeupSessionRateLimitKeepsSentinel(t *testing.T) {
	t.Parallel()
	s, sender, parent := wakeupSessionFixture(t)
	addChildJob(t, s, "child-rate", StatusRunning, 0, "")
	seedPendingInteraction(t, s, "int-1", "child-rate")
	w := addChildReceipt(t, s, jobstore.WakeupRecord{
		JobID: parent.result.ID, FilterJobID: "child-rate", AutoChild: 1,
		EventTypesJSON: jobstore.EncodeStringList([]string{EventJobTerminal, EventInteractionCreated}),
	})

	// The first approval wakes the parent and is confirmed by its next turn.
	s.deliverChildReceipt(w, EventInteractionCreated)
	assert.Eq(t, 1, len(sayTexts(sender)))
	s.applyRemoteSessionState(parent, WorkerInflightJob{
		JobID: parent.result.ID, SessionStatus: StatusAwaitingInput, TurnNo: 2,
	}, false)
	if got := childWakeRow(t, s, w.ID); got.ContinuationJobID != "" {
		t.Fatalf("the confirmed receipt kept its sentinel: %q", got.ContinuationJobID)
	}

	// The same child raises another approval inside the window.
	seedPendingInteraction(t, s, "int-2", "child-rate")
	s.deliverChildReceipt(w, EventInteractionCreated)

	assert.Eq(t, 1, len(sayTexts(sender)), "a second approval inside the window woke the parent")
	if got := childWakeRow(t, s, w.ID); got.ContinuationJobID != jobstore.WakeupPendingSay {
		t.Fatalf("continuation = %q, want the rate-limited receipt to stay a sentinel", got.ContinuationJobID)
	}
	assert.Eq(t, 1, childWakeRow(t, s, w.ID).Enabled)
}

// TestWakeupSessionRateLimitHoldsAcrossIdleTurns: coming back to awaiting_input flushes the
// pending receipts, and that flush must respect the window too — otherwise the minute is
// meaningless: one quick turn and the second approval goes out immediately.
func TestWakeupSessionRateLimitHoldsAcrossIdleTurns(t *testing.T) {
	t.Parallel()
	s, sender, parent := wakeupSessionFixture(t)
	addChildJob(t, s, "child-window", StatusRunning, 0, "")
	seedPendingInteraction(t, s, "int-w1", "child-window")
	w := addChildReceipt(t, s, jobstore.WakeupRecord{
		JobID: parent.result.ID, FilterJobID: "child-window", AutoChild: 1,
		EventTypesJSON: jobstore.EncodeStringList([]string{EventJobTerminal, EventInteractionCreated}),
	})
	s.deliverChildReceipt(w, EventInteractionCreated)
	assert.Eq(t, 1, len(sayTexts(sender)))

	// A short turn: the batch is confirmed and the session is idle again.
	parent.mu.Lock()
	parent.result.Status = StatusRunning
	parent.result.TurnNo = 2
	parent.mu.Unlock()
	// The child raises another approval while the parent works.
	seedPendingInteraction(t, s, "int-w2", "child-window")
	s.deliverChildReceipt(w, EventInteractionCreated)

	parent.mu.Lock()
	parent.result.Status = StatusAwaitingInput
	parent.result.TurnNo = 3
	parent.mu.Unlock()
	s.onParentAwaitingInput(parent.result.ID)

	assert.Eq(t, 1, len(sayTexts(sender)), "the idle flush ignored the rate window")
	if got := childWakeRow(t, s, w.ID); got.ContinuationJobID != jobstore.WakeupPendingSay {
		t.Fatalf("continuation = %q, want the window to keep the sentinel", got.ContinuationJobID)
	}
}

// TestWakeupSessionNeedsInputFollowsChildState: what a receipt SAYS about "needs a person"
// comes from the child's state, not from the events the row subscribes to. A row that also
// listens to interaction.created still reports a finished child as just finished.
func TestWakeupSessionNeedsInputFollowsChildState(t *testing.T) {
	t.Parallel()
	s, sender, parent := wakeupSessionFixture(t)
	addChildJob(t, s, "child-state", StatusDone, 0, "")
	w := addChildReceipt(t, s, jobstore.WakeupRecord{
		JobID: parent.result.ID, FilterJobID: "child-state", AutoChild: 1,
		EventTypesJSON: jobstore.EncodeStringList([]string{EventJobTerminal, EventInteractionCreated}),
	})

	s.deliverChildReceipt(w, EventJobTerminal)

	texts := sayTexts(sender)
	assert.Eq(t, 1, len(texts))
	for _, phrase := range []string{"请转告人", "tell a person", "无法代替人"} {
		assert.False(t, strings.Contains(texts[0], phrase),
			"say = %q, want no needs-input line for a finished child", texts[0])
	}
}

// TestWakeupSessionConfirmKeepsRetriggeredReceipt: a receipt that fires again AFTER its
// batch was spoken keeps its sentinel even when the child's state did not change — a second
// tool approval on a running job looks exactly like the first.
func TestWakeupSessionConfirmKeepsRetriggeredReceipt(t *testing.T) {
	t.Parallel()
	s, sender, parent := wakeupSessionFixture(t)
	addChildJob(t, s, "child-again", StatusRunning, 0, "")
	seedPendingInteraction(t, s, "int-a1", "child-again")
	w := addChildReceipt(t, s, jobstore.WakeupRecord{
		JobID: parent.result.ID, FilterJobID: "child-again", AutoChild: 1,
		EventTypesJSON: jobstore.EncodeStringList([]string{EventJobTerminal, EventInteractionCreated}),
	})
	s.deliverChildReceipt(w, EventInteractionCreated)
	assert.Eq(t, 1, len(sayTexts(sender)))

	// A second interaction lands after the batch was spoken; the child is still running, so
	// the receipt cannot go out yet (the say slot is taken).
	seedPendingInteraction(t, s, "int-a2", "child-again")
	s.deliverChildReceipt(w, EventInteractionCreated)
	assert.Eq(t, 1, len(sayTexts(sender)))

	// The session consumed the first batch.
	s.applyRemoteSessionState(parent, WorkerInflightJob{
		JobID: parent.result.ID, SessionStatus: StatusAwaitingInput, TurnNo: 2,
	}, false)

	got := childWakeRow(t, s, w.ID)
	assert.Eq(t, jobstore.WakeupPendingSay, got.ContinuationJobID,
		"the confirmation swallowed the interaction that arrived after the batch")
	assert.Eq(t, 1, got.Enabled)
}
