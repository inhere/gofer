package stage

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/stagenote"
)

// The autonomy levels' view of a step change (plan F4). At every step change — `done` that
// advanced, `back`, an approval that advanced — both levels write one "would do" journal line
// naming what the level would do about the new step; `remind` also does the one thing it may
// do, sending the current session a "continue" turn. Nothing else is ever done on the flow's
// behalf: merging, pushing and releasing stay a person's, and no level moves a step.

// The actions a level would take.
const (
	// ActionSendContinue: the next step is the agent's, so the session is sent "continue
	// with <step>".
	ActionSendContinue = "send_continue"
	// ActionWaitCard: the next step waits for a person (review), so a 「等我」 card is raised.
	ActionWaitCard = "raise_wait_card"
)

// What moved the flow; part of the rule name.
const (
	triggerDone    = "done"
	triggerBack    = "back"
	triggerApprove = "approve"
)

// agentSteps are the steps whose work is the agent's: after entering one of them the agent
// carries on. review waits for a person; done has nothing left to do.
var agentSteps = map[string]bool{StepDesign: true, StepPlan: true, StepDoing: true, StepVerify: true}

// IsAgentStep reports whether the work of a step belongs to the agent.
func IsAgentStep(step string) bool { return agentSteps[step] }

// wouldDo is one recorded "would do".
type wouldDo struct{ Action, Step, Rule string }

// wouldAfter is what a level would do once the flow entered step (by trigger); ok=false when
// there is nothing to do (the flow finished).
func wouldAfter(trigger, step string) (wouldDo, bool) {
	// The rule name says what moved the flow and which kind of step it entered; after a back
	// the entered step is the previous one.
	where := "_next_"
	if trigger == triggerBack {
		where = "_prev_"
	}
	switch {
	case IsAgentStep(step):
		return wouldDo{ActionSendContinue, step, trigger + where + "agent"}, true
	case step == StepReview:
		return wouldDo{ActionWaitCard, step, trigger + where + "review"}, true
	}
	return wouldDo{}, false
}

// journal is the line of a "would do": parseable head, readable tail.
func (w wouldDo) journal() string {
	what := "send the session a continue turn for step " + w.Step
	if w.Action == ActionWaitCard {
		what = "raise a waiting card: " + w.Step + " needs a person's approval"
	}
	return fmt.Sprintf("stage would: %s %s [rule: %s] (%s)", w.Action, w.Step, w.Rule, what)
}

var wouldRe = regexp.MustCompile(`^stage would: (\w+) (\w+) \[rule: (\w+)\]`)

// parseWould reads a stage_action journal line back.
func parseWould(text string) (wouldDo, bool) {
	m := wouldRe.FindStringSubmatch(text)
	if m == nil {
		return wouldDo{}, false
	}
	return wouldDo{Action: m[1], Step: m[2], Rule: m[3]}, true
}

// Outcomes of handing the reminder to a session.
const (
	// DeliveredNow: the session took the turn.
	DeliveredNow = "delivered"
	// DeliveredQueued: the session sits on a seat that cannot take it now; the turn is queued
	// on the seat and goes out when a session is back there.
	DeliveredQueued = "queued"
	// NotDelivered: the session is offline or gone; nothing was queued.
	NotDelivered = "offline"
)

// Delivery is one reminder to hand to a session.
type Delivery struct {
	WorkItemID string
	SessionID  string
	Step       string
	// Text is the whole message, system prefix included.
	Text string
}

// DeliverFunc hands a reminder to a session over the existing delivery paths. It returns one
// of DeliveredNow / DeliveredQueued / NotDelivered, or an error for a failure.
type DeliverFunc func(ctx context.Context, d Delivery) (string, error)

// deliverTimeout bounds one reminder's delivery.
const deliverTimeout = 30 * time.Second

// SetLevel sets the autonomy level (record | remind) of a flow in progress. The entry layer
// must admit only a person.
func (s *Service) SetLevel(id, level, by string) (View, error) {
	ws, item, err := s.load(id)
	if err != nil {
		return View{}, err
	}
	level = strings.ToLower(strings.TrimSpace(level))
	cur, err := s.store.SetWorkStageLevel(ws.WorkItemID, level, by)
	if err != nil {
		return View{}, err
	}
	return s.viewOf(cur, item.ProjectKey), nil
}

// afterStep runs what the level does once a step change was applied: the reminder, at
// remind, when the new step is the agent's.
func (s *Service) afterStep(item jobstore.WorkItem, level string, w wouldDo) {
	if level != jobstore.StageLevelRemind || w.Action != ActionSendContinue || s.deliver == nil {
		return
	}
	s.async(func() { s.remind(item, w.Step) })
}

func (s *Service) async(fn func()) {
	if s.sync {
		fn()
		return
	}
	s.inflight.Add(1)
	go func() {
		defer s.inflight.Done()
		fn()
	}()
}

// Drain waits for the "continue" turns still being sent, so none writes to the store after
// it closes (G051). serve calls it through the HTTP server's Drain.
func (s *Service) Drain() { s.inflight.Wait() }

// remind sends the current session of the work item the "continue" turn for step and
// journals how it went. A flow that moved on again before the turn goes out gets nothing.
func (s *Service) remind(item jobstore.WorkItem, step string) {
	ws, ok, err := s.store.GetWorkStage(item.ID)
	if err != nil || !ok || !ws.Open() || ws.StepName() != step {
		return
	}
	sid := ""
	if list, err := s.store.ListWorkItemSessions(item.ID); err == nil {
		for _, x := range list {
			if x.Role == "current" {
				sid = x.SessionID
				break
			}
		}
	}
	if sid == "" {
		s.noteRemind(item.ID, "stage remind: no current session to send the continue turn for "+step+" to")
		return
	}
	text := stagenote.RemindLine(s.langOf(item.ProjectKey), stagenote.Brief{
		WorkItemID: item.ID, Step: step, StepNo: ws.Step + 1, StepTotal: len(ws.Steps), Status: ws.Status,
	})
	ctx, cancel := context.WithTimeout(context.Background(), deliverTimeout)
	defer cancel()
	out, err := s.deliver(ctx, Delivery{WorkItemID: item.ID, SessionID: sid, Step: step, Text: text})
	short := jobstore.SessionLabel(sid)
	switch {
	case err != nil:
		s.noteRemind(item.ID, fmt.Sprintf("stage remind: continue %s not delivered to session %s: %v", step, short, err))
	case out == DeliveredNow:
		s.noteRemind(item.ID, fmt.Sprintf("stage remind: continue %s sent to session %s", step, short))
	case out == DeliveredQueued:
		s.noteRemind(item.ID, fmt.Sprintf("stage remind: continue %s queued for session %s (its seat has nobody online; it goes out when a session is back)", step, short))
	default:
		s.noteRemind(item.ID, fmt.Sprintf("stage remind: continue %s not delivered, session %s is offline", step, short))
	}
}

func (s *Service) noteRemind(id, text string) {
	if err := s.store.AppendStageJournal(id, text, "system", jobstore.WorkLevelDetail); err != nil {
		slog.Warn("stage remind journal", "work_item", id, "err", err)
	}
}

// langOf is the language of the text sent to a session of project.
func (s *Service) langOf(project string) i18n.Lang {
	if c := s.config(); c != nil {
		return c.LanguageFor(project)
	}
	return i18n.Default
}
