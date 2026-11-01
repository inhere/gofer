package inbox

import (
	"testing"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/today"
)

func TestClassifyFirstMatchWins(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		sig  Signals
		want Category
		ok   bool
	}{
		{"nothing", Signals{}, "", false},
		{"held", Signals{HeldForApproval: true}, CatApproval, true},
		{"approval interaction", Signals{ApprovalInteraction: true}, CatApproval, true},
		{"question", Signals{Question: true}, CatAnswer, true},
		{"review", Signals{NeedsReview: true}, CatReview, true},
		{"plan blocked", Signals{PlanBlocked: true}, CatPlanStuck, true},
		{"todo needs review alone", Signals{TodoNeedsReview: true}, CatPlanStuck, true},
		{"failed", Signals{FailedBySelf: true}, CatFailed, true},
		{"delivery", Signals{DeliveryHeld: true}, CatDelivery, true},
		// boundaries: the earlier class wins when a subject satisfies several
		{"approval beats review", Signals{HeldForApproval: true, NeedsReview: true}, CatApproval, true},
		{"question beats review", Signals{Question: true, NeedsReview: true}, CatAnswer, true},
		{"review beats todo review", Signals{NeedsReview: true, TodoNeedsReview: true}, CatReview, true},
		{"review beats plan blocked", Signals{NeedsReview: true, PlanBlocked: true}, CatReview, true},
		{"plan blocked beats failed", Signals{PlanBlocked: true, FailedBySelf: true}, CatPlanStuck, true},
		{"failed beats delivery", Signals{FailedBySelf: true, DeliveryHeld: true}, CatFailed, true},
		{"everything", Signals{true, true, true, true, true, true, true, true}, CatApproval, true},
	}
	for _, tc := range cases {
		got, ok := Classify(tc.sig)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: Classify = %q,%v want %q,%v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

// Every category is reachable and the order is the design's.
func TestCategoriesExhaustiveAndOrdered(t *testing.T) {
	t.Parallel()
	sigs := []Signals{{HeldForApproval: true}, {Question: true}, {NeedsReview: true}, {PlanBlocked: true}, {FailedBySelf: true}, {DeliveryHeld: true}}
	for i, c := range Categories {
		got, _ := Classify(sigs[i])
		if got != c || Rank(c) != i {
			t.Errorf("category %d = %q (rank %d), classify %q", i, c, Rank(c), got)
		}
		if lbl := Label(i18n.EN, c); lbl == "" || lbl == "inbox.waiting."+string(c) {
			t.Errorf("no English label for %s", c)
		}
		if lbl := Label(i18n.ZhCN, c); lbl == "" || lbl == Label(i18n.EN, c) {
			t.Errorf("no zh-CN label for %s", c)
		}
	}
}

func card(kind, key string, refs today.Refs, subtype string) today.Card {
	return today.Card{Key: key, Kind: kind, Refs: refs, Subtype: subtype}
}

func TestWaitingFiltersGroupsAndLabels(t *testing.T) {
	t.Parallel()
	cards := []today.Card{
		card(today.KindWork, "work:w1", today.Refs{WorkItemID: "w1"}, ""),
		card("suggestion", "suggestion:w1:goal", today.Refs{WorkItemID: "w1"}, ""), // a legacy kind no builder makes any more
		card(today.KindMerge, "merge:1", today.Refs{}, ""),
		card(today.KindMemory, "memory:1", today.Refs{}, ""),
		card(today.KindCapture, today.CaptureCardKey, today.Refs{}, ""),
		card("worker_enroll", "worker_enroll:box", today.Refs{}, ""),
		// the same job needs review and has a pending approval interaction: one entry, approval
		card(today.KindReview, "review:j1", today.Refs{JobID: "j1"}, ""),
		card(today.KindInteraction, "interaction:j1/i1", today.Refs{JobID: "j1", InteractionID: "i1"}, job.InteractionTypePermission),
		card(today.KindInteraction, "interaction:j2/i2", today.Refs{JobID: "j2"}, job.InteractionTypeQuestion),
		card(today.KindInteraction, "interaction:j3/i3", today.Refs{JobID: "j3"}, job.InteractionTypeConfirmation),
		card(today.KindApproval, "approval:j4", today.Refs{JobID: "j4"}, ""),
		card(today.KindPermission, "permission:d1", today.Refs{SessionID: "s1"}, ""),
		card(today.KindPermission, "permission:d2", today.Refs{SessionID: "s2"}, today.PermissionSubtypeQuestion),
		card(today.KindRelay, "relay:s3", today.Refs{SessionID: "s3"}, ""),
		card(today.KindDecision, "decision:d3", today.Refs{DecisionID: "d3"}, ""),
		card(today.KindPlanBlocked, "plan_blocked:p1", today.Refs{PlanID: "p1", JobID: "j1"}, ""),
		card(today.KindMailHeld, today.MailHeldCardKey, today.Refs{}, ""),
		card(KindJobFailed, "job.terminal:j9", today.Refs{JobID: "j9"}, ""),
	}
	got := Waiting(i18n.EN, cards)
	want := map[string]Category{
		"interaction:j1/i1":   CatApproval, // won over review:j1, which is dropped
		"interaction:j2/i2":   CatAnswer,
		"interaction:j3/i3":   CatApproval,
		"approval:j4":         CatApproval,
		"permission:d1":       CatApproval,
		"permission:d2":       CatAnswer,
		"relay:s3":            CatAnswer,
		"decision:d3":         CatAnswer,
		"plan_blocked:p1":     CatPlanStuck,
		today.MailHeldCardKey: CatDelivery,
		"job.terminal:j9":     CatFailed,
	}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d: %+v", len(got), len(want), keysOf(got))
	}
	for _, c := range got {
		w, ok := want[c.Key]
		if !ok || c.Waiting != string(w) || c.WaitingLabel != Label(i18n.EN, w) {
			t.Errorf("%s: waiting=%q label=%q, want %q", c.Key, c.Waiting, c.WaitingLabel, w)
		}
	}
	if got := Waiting(i18n.EN, nil); got == nil || len(got) != 0 {
		t.Fatalf("empty input must give an empty non-nil list, got %#v", got)
	}
}

func TestWaitingKeepsEarlierPositionOfGroup(t *testing.T) {
	t.Parallel()
	got := Waiting(i18n.EN, []today.Card{
		card(today.KindReview, "review:j1", today.Refs{JobID: "j1"}, ""),
		card(today.KindRelay, "relay:s1", today.Refs{SessionID: "s1"}, ""),
		card(today.KindApproval, "approval:j1", today.Refs{JobID: "j1"}, ""),
	})
	if len(got) != 2 || got[0].Key != "approval:j1" || got[1].Key != "relay:s1" {
		t.Fatalf("got %v", keysOf(got))
	}
}

func keysOf(cs []today.Card) []string {
	out := []string{}
	for _, c := range cs {
		out = append(out, c.Key)
	}
	return out
}

type fakeJobs struct{ rows []jobstore.JobRecord }

func (f fakeJobs) ListJobs(q jobstore.ListQuery) ([]jobstore.JobRecord, error) {
	var out []jobstore.JobRecord
	for _, r := range f.rows {
		if q.Status != "" && r.Status != q.Status {
			continue
		}
		if q.SourceJob != "" && r.SourceJobID != q.SourceJob {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func TestTerminalCardsOnlyThePersonsOwn(t *testing.T) {
	t.Parallel()
	mk := func(id, status string, mut func(*jobstore.JobRecord)) jobstore.JobRecord {
		r := jobstore.JobRecord{ID: id, Status: status, Channel: job.ChannelCLI, ProjectKey: "p", EndedAt: 100, Error: "boom"}
		if mut != nil {
			mut(&r)
		}
		return r
	}
	rows := []jobstore.JobRecord{
		mk("own-failed", job.StatusFailed, nil),
		mk("own-timeout", job.StatusTimeout, nil),
		mk("own-done", job.StatusDone, nil),
		mk("own-cancelled", job.StatusCancelled, nil),
		mk("own-continued", job.StatusFailed, nil),
		mk("own-followup", job.StatusRunning, func(r *jobstore.JobRecord) { r.SourceJobID = "own-continued"; r.EndedAt = 0 }),
		mk("own-autoresumed", job.StatusFailed, func(r *jobstore.JobRecord) { r.AutoResumedBy = "x" }),
		mk("cred-failed", job.StatusFailed, func(r *jobstore.JobRecord) { r.TagsJSON = `["submitted_by_job:j"]` }),
		mk("cred-done", job.StatusDone, func(r *jobstore.JobRecord) { r.TagsJSON = `["submitted_by_job:j"]` }),
		mk("agent-failed", job.StatusFailed, func(r *jobstore.JobRecord) { r.TagsJSON = `["submitted_by_session:s"]` }),
		mk("agent-done", job.StatusDone, func(r *jobstore.JobRecord) { r.SourceSessionID = "s" }),
		mk("cron-failed", job.StatusFailed, func(r *jobstore.JobRecord) { r.Channel = "cron" }),
		mk("cron-done", job.StatusDone, func(r *jobstore.JobRecord) { r.Channel = "cron" }),
		mk("other-project", job.StatusFailed, func(r *jobstore.JobRecord) { r.ProjectKey = "hidden" }),
	}
	failed, fyi, err := TerminalCards(fakeJobs{rows}, i18n.EN, func(p string) bool { return p != "hidden" })
	if err != nil {
		t.Fatal(err)
	}
	gotF, gotY := map[string]bool{}, map[string]bool{}
	for _, c := range failed {
		gotF[c.Refs.JobID] = true
		if c.Kind != KindJobFailed {
			t.Errorf("failed card kind = %s", c.Kind)
		}
	}
	for _, c := range fyi {
		gotY[c.Refs.JobID] = true
	}
	if len(gotF) != 2 || !gotF["own-failed"] || !gotF["own-timeout"] {
		t.Errorf("failed = %v", gotF)
	}
	if len(gotY) != 1 || !gotY["own-done"] {
		t.Errorf("fyi = %v", gotY)
	}
}
