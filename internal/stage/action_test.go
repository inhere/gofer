package stage

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/msgprefix"
)

// sink records what the service handed to the delivery path.
type sink struct {
	mu  sync.Mutex
	got []Delivery
	out string
	err error
}

func (k *sink) deliver(_ context.Context, d Delivery) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.got = append(k.got, d)
	if k.err != nil {
		return "", k.err
	}
	if k.out == "" {
		return DeliveredNow, nil
	}
	return k.out, nil
}

func (k *sink) count() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.got)
}

// withDelivery rebuilds the fixture's service with a delivery sink and the fixture clock.
func (f *fix) withDelivery(k *sink) {
	f.svc = New(Deps{
		Store: f.store, Config: func() *config.Config { return f.cfg }, Deliver: k.deliver, Sync: true,
		Now: func() time.Time { return time.Unix(f.clock.Load(), 0) },
	})
}

func (f *fix) journalHas(id, kind, frag string) bool {
	f.t.Helper()
	list, err := f.store.ListWorkJournal(id, 500, 0)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, e := range list {
		if e.Kind == kind && strings.Contains(e.Text, frag) {
			return true
		}
	}
	return false
}

func (f *fix) attach(id, sid string) {
	f.t.Helper()
	if _, err := f.store.UpsertAgentSession(jobstore.AgentSession{SessionID: sid, Agent: "claude", ProjectKey: "p", Cwd: f.repo}); err != nil {
		f.t.Fatal(err)
	}
	if err := f.store.AttachWorkSession(id, sid, "human:a"); err != nil {
		f.t.Fatal(err)
	}
}

func TestSetLevelValidatesAndIsJournaled(t *testing.T) {
	f := newFix(t)
	id := f.item(TemplateSmall)
	if _, err := f.svc.SetLevel(id, "auto", "human:a"); !errors.Is(err, jobstore.ErrWorkInvalid) {
		t.Fatalf("auto level err = %v, want ErrWorkInvalid", err)
	}
	v, err := f.svc.SetLevel(id, " Remind ", "human:a")
	if err != nil || v.Level != jobstore.StageLevelRemind {
		t.Fatalf("SetLevel = %+v %v", v, err)
	}
	if !f.journalHas(id, jobstore.WorkJournalStage, "stage level: record > remind") {
		t.Fatal("the level change is not in the journal")
	}
	if _, err := f.svc.SetLevel("w-nope", "record", "human:a"); !errors.Is(err, jobstore.ErrWorkItemNotFound) {
		t.Fatalf("unknown item err = %v", err)
	}
}

func TestRecordLevelOnlyWritesTheWouldDoLine(t *testing.T) {
	f := newFix(t)
	k := &sink{}
	f.withDelivery(k)
	id := f.item(TemplateSmall)
	f.attach(id, "s-1")

	res, err := f.svc.Done(id, "session:s-1")
	if err != nil || !res.Advanced {
		t.Fatalf("done = %+v %v", res, err)
	}
	if !f.journalHas(id, jobstore.WorkJournalStageAction, "stage would: send_continue verify [rule: done_next_agent]") {
		t.Fatal("no would-do line for the verify step")
	}
	if k.count() != 0 {
		t.Fatalf("record level sent %d turns, want none", k.count())
	}
}

func TestRemindLevelSendsContinueOncePerStepChange(t *testing.T) {
	f := newFix(t)
	k := &sink{}
	f.withDelivery(k)
	id := f.item(TemplateSmall)
	f.attach(id, "s-1")
	if _, err := f.svc.SetLevel(id, "remind", "human:a"); err != nil {
		t.Fatal(err)
	}

	if res, err := f.svc.Done(id, "session:s-1"); err != nil || !res.Advanced {
		t.Fatalf("done = %+v %v", res, err)
	}
	if k.count() != 1 {
		t.Fatalf("turns after the advance = %d, want 1", k.count())
	}
	d := k.got[0]
	if d.SessionID != "s-1" || d.WorkItemID != id || d.Step != StepVerify {
		t.Fatalf("delivery = %+v", d)
	}
	if kind, person := msgprefix.Classify(d.Text); kind != msgprefix.Stage || person || !strings.Contains(d.Text, StepVerify) {
		t.Fatalf("text %q is not a stage-prefixed continue for verify", d.Text)
	}
	if !f.journalHas(id, jobstore.WorkJournalStage, "continue verify sent to session s-1") {
		t.Fatal("the delivery is not in the journal")
	}

	// The verify gate cannot pass here: a repeated done does not move the flow, so no new turn.
	for i := 0; i < 2; i++ {
		if res, err := f.svc.Done(id, "session:s-1"); err != nil || res.Advanced {
			t.Fatalf("repeat done %d = %+v %v", i, res, err)
		}
	}
	if k.count() != 1 {
		t.Fatalf("turns after repeated done = %d, want still 1", k.count())
	}
}

func TestRemindLevelDoesNotSendForAPersonsStep(t *testing.T) {
	f := newFix(t)
	k := &sink{}
	f.withDelivery(k)
	id := f.item(TemplateFeature)
	f.attach(id, "s-1")
	if _, err := f.svc.SetLevel(id, "remind", "human:a"); err != nil {
		t.Fatal(err)
	}
	f.stageAt(id, StepPlan)

	// plan -> review (back): the next step is a person's, so a waiting card is the would-do
	// and nothing is sent.
	if _, err := f.svc.Back(id, "the plan needs another look", "human:a"); err != nil {
		t.Fatal(err)
	}
	if k.count() != 0 {
		t.Fatalf("a turn was sent for the review step: %+v", k.got)
	}
	if !f.journalHas(id, jobstore.WorkJournalStageAction, "stage would: raise_wait_card review [rule: back_prev_review]") {
		t.Fatal("no waiting-card would-do for the review step")
	}
	// The person approves: the flow enters plan, the agent's step, and gets the turn.
	if res, err := f.svc.Approve(id, "human:a"); err != nil || !res.Advanced {
		t.Fatalf("approve = %+v %v", res, err)
	}
	if k.count() != 1 || k.got[0].Step != StepPlan {
		t.Fatalf("turns = %+v, want one for plan", k.got)
	}
}

func TestRemindJournalKeepsAJobIDWhole(t *testing.T) {
	f := newFix(t)
	f.withDelivery(&sink{})
	id := f.item(TemplateSmall)
	f.attach(id, "job-20261126-101010-ab12cd34")
	if _, err := f.svc.SetLevel(id, "remind", "human:a"); err != nil {
		t.Fatal(err)
	}
	if res, err := f.svc.Done(id, "session:job-20261126-101010-ab12cd34"); err != nil || !res.Advanced {
		t.Fatalf("done = %+v %v", res, err)
	}
	if !f.journalHas(id, jobstore.WorkJournalStage, "sent to session job-20261126-101010-ab12cd34") {
		t.Fatal("the job id is not whole in the journal")
	}
}

func TestRemindOutcomesAreJournaled(t *testing.T) {
	cases := []struct {
		name string
		out  string
		err  error
		want string
	}{
		{"queued", DeliveredQueued, nil, "queued for session s-1"},
		{"offline", NotDelivered, nil, "not delivered, session s-1 is offline"},
		{"failed", "", errors.New("relay down"), "not delivered to session s-1: relay down"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFix(t)
			k := &sink{out: c.out, err: c.err}
			f.withDelivery(k)
			id := f.item(TemplateSmall)
			f.attach(id, "s-1")
			if _, err := f.svc.SetLevel(id, "remind", "human:a"); err != nil {
				t.Fatal(err)
			}
			if _, err := f.svc.Done(id, "session:s-1"); err != nil {
				t.Fatal(err)
			}
			if !f.journalHas(id, jobstore.WorkJournalStage, c.want) {
				t.Fatalf("journal lacks %q", c.want)
			}
		})
	}
	t.Run("no current session", func(t *testing.T) {
		f := newFix(t)
		k := &sink{}
		f.withDelivery(k)
		id := f.item(TemplateSmall)
		if _, err := f.svc.SetLevel(id, "remind", "human:a"); err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.Done(id, "human:a"); err != nil {
			t.Fatal(err)
		}
		if k.count() != 0 || !f.journalHas(id, jobstore.WorkJournalStage, "no current session") {
			t.Fatalf("sent %d, journal lacks the no-session note", k.count())
		}
	})
}

// reportOf is the report of one item at the fixture clock.
func (f *fix) reportOf(id string) Report {
	f.t.Helper()
	rep, err := f.svc.Report(id, "")
	if err != nil {
		f.t.Fatal(err)
	}
	return rep
}

func rowOf(rep Report, action string) ReportRow {
	for _, r := range rep.ByAction {
		if r.Action == action {
			return r
		}
	}
	return ReportRow{}
}

func TestReportCountsMatchMismatchPendingAndNoFollowup(t *testing.T) {
	f := newFix(t)
	f.withDelivery(&sink{})
	id := f.item(TemplateSmall)

	// doing -> verify by the agent: would-do #1 (continue verify).
	f.tick()
	if _, err := f.svc.Done(id, "session:s-1"); err != nil {
		t.Fatal(err)
	}
	if rep := f.reportOf(id); rep.Total != 1 || rep.Pending != 1 {
		t.Fatalf("before any follow-up: %+v", rep)
	}
	// The agent sends it back instead of finishing: #1 is a mismatch, and the back's own
	// would-do (#2, continue doing) is recorded.
	f.tick()
	if _, err := f.svc.Back(id, "rework", "session:s-1"); err != nil {
		t.Fatal(err)
	}
	rep := f.reportOf(id)
	if rep.Total != 2 || rep.Pending != 1 || rowOf(rep, ActionSendContinue).Mismatch != 1 {
		t.Fatalf("after the back: %+v", rep)
	}
	// The agent carries doing through: #2 is a match; #3 (continue verify) is pending.
	f.tick()
	if _, err := f.svc.Done(id, "session:s-1"); err != nil {
		t.Fatal(err)
	}
	rep = f.reportOf(id)
	row := rowOf(rep, ActionSendContinue)
	if rep.Total != 3 || row.Match != 1 || row.Mismatch != 1 || row.Pending != 1 || row.Rate != 0.5 {
		t.Fatalf("after the second done: %+v row=%+v", rep, row)
	}
	if rep.RecentN != 2 || rep.RecentMatch != 1 || rep.RecentRate != 0.5 {
		t.Fatalf("recent figure = %d/%d %.2f", rep.RecentMatch, rep.RecentN, rep.RecentRate)
	}
	rules := map[string]ReportRow{}
	for _, r := range rep.ByRule {
		rules[r.Rule] = r
	}
	if rules["done_next_agent"].Total != 2 || rules["back_prev_agent"].Match != 1 {
		t.Fatalf("by rule = %+v", rep.ByRule)
	}
	if len(rep.Recent) != 3 || rep.Recent[0].Outcome != OutcomePending {
		t.Fatalf("recent list = %+v", rep.Recent)
	}

	// Nothing follows #3 within the window: it reads no_followup, which is not agreement.
	f.clock.Add(int64(ReportObserveWindow/time.Second) + 60)
	rep = f.reportOf(id)
	row = rowOf(rep, ActionSendContinue)
	if row.NoFollowup != 1 || row.Pending != 0 || row.Rate != 1.0/3.0 {
		t.Fatalf("after the window: %+v", row)
	}
}

func TestReportWaitCardMatchesOnAPersonAndMismatchesOnAnAgent(t *testing.T) {
	f := newFix(t)
	f.withDelivery(&sink{})
	id := f.item(TemplateFeature)
	f.stageAt(id, StepPlan)

	// Back to review: a card is the would-do; a person approving it is a match.
	if _, err := f.svc.Back(id, "again", "human:a"); err != nil {
		t.Fatal(err)
	}
	f.tick()
	if res, err := f.svc.Approve(id, "human:a"); err != nil || !res.Advanced {
		t.Fatalf("approve = %+v %v", res, err)
	}
	row := rowOf(f.reportOf(id), ActionWaitCard)
	if row.Total != 1 || row.Match != 1 {
		t.Fatalf("wait card after a person's approval: %+v", row)
	}

	// Back to review again; this time a passing review job lets the agent's own done move
	// it, so nobody acted on the card: a mismatch.
	f.tick()
	if _, err := f.svc.Back(id, "again", "human:a"); err != nil {
		t.Fatal(err)
	}
	f.tick()
	f.reviewJob("job-r1", "pass")
	f.link(id, jobstore.WorkLinkJob, "job-r1", jobstore.WorkJobRoleReview)
	if res, err := f.svc.Done(id, "session:s-1"); err != nil || !res.Advanced {
		t.Fatalf("done = %+v %v", res, err)
	}
	row = rowOf(f.reportOf(id), ActionWaitCard)
	if row.Total != 2 || row.Match != 1 || row.Mismatch != 1 {
		t.Fatalf("wait card after the agent's done: %+v", row)
	}
}

func TestReportProjectScopeAndEmpty(t *testing.T) {
	f := newFix(t)
	f.withDelivery(&sink{})
	if rep, err := f.svc.Report("", "p"); err != nil || rep.Total != 0 || len(rep.ByAction) != 0 {
		t.Fatalf("empty report = %+v %v", rep, err)
	}
	id := f.item(TemplateSmall)
	if _, err := f.svc.Done(id, "session:s-1"); err != nil {
		t.Fatal(err)
	}
	if rep, err := f.svc.Report("", "p"); err != nil || rep.Total != 1 || rep.ProjectKey != "p" {
		t.Fatalf("project report = %+v %v", rep, err)
	}
	if rep, err := f.svc.Report("", "other"); err != nil || rep.Total != 0 {
		t.Fatalf("other project report = %+v %v", rep, err)
	}
}
