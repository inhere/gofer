package jobstore

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/testutil/golden"
)

// workTextDump drives every store operation that writes human-readable text (work-item
// journal lines, change summaries, notes of answered decisions / interactions, the delete
// event title, merge suggestions, issue-rename notes) and returns the text, with the
// random work-item ids replaced by W1, W2, ... so the output is stable.
func workTextDump(t *testing.T, s *Store) string {
	t.Helper()
	old := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = old })

	var sb strings.Builder
	ids := map[string]string{}
	name := func(id string) string {
		if n, ok := ids[id]; ok {
			return n
		}
		n := fmt.Sprintf("W%d", len(ids)+1)
		ids[id] = n
		return n
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	mkItem := func(title string) WorkItem {
		w, err := s.CreateWorkItem(WorkItemInput{Title: title, By: "human:a"})
		must(err)
		name(w.ID)
		return w
	}
	journal := func(label string, w WorkItem) {
		t.Helper()
		lines, err := s.ListWorkJournal(w.ID, 100, 0)
		must(err)
		sb.WriteString("## " + label + "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			l := lines[i]
			text := l.Text
			for id, n := range ids {
				text = strings.ReplaceAll(text, id, n)
			}
			sb.WriteString(fmt.Sprintf("%s/%s/%s: %s\n", l.Kind, l.By, l.Level, text))
		}
	}
	str := func(v string) *string { return &v }

	// capture, create, draft
	cap, err := s.CaptureWorkItem(CaptureInput{Text: "buy milk\nand eggs", Kind: WorkKindIdea, By: "human:a"})
	must(err)
	name(cap.ID)
	journal("captured", cap)
	draft, created, err := s.EnsureDraftWorkItemForSession(AgentSession{SessionID: "sess-abcdef123456", ProjectKey: "p"}, "first prompt")
	must(err)
	if !created {
		t.Fatal("draft not created")
	}
	name(draft.ID)
	journal("draft", draft)

	// update: every field kind
	a := mkItem("Alpha")
	prio, park, remind, unsorted := 2, int64(1_700_000_000), int64(1_700_003_600), true
	_, sum, err := s.UpdateWorkItem(a.ID, WorkItemPatch{
		Title: str("Alpha 2"), Goal: str("the goal"), BlockerKind: str("person"), BlockerText: str("waiting"),
		NextStep: str("go"), Summary: str("sum"), ProjectKey: str("proj"), Workspace: str("/w"), ParkNote: str("later"),
		Priority: &prio, ParkUntil: &park, RemindAt: &remind, Unsorted: &unsorted,
	}, 0, "human:a")
	must(err)
	sb.WriteString("## summary 1\n" + sum + "\n")
	zero := int64(0)
	unsortedOff := false
	st, src := WorkParked, WorkSourceAuto
	_, sum, err = s.UpdateWorkItem(a.ID, WorkItemPatch{
		Title: str("Alpha 3"), Goal: str(""), BlockerText: str(""), NextStep: str("next"), ParkUntil: &zero, Unsorted: &unsortedOff,
		Status: &st, StatusSource: &src,
	}, 0, "human:a")
	must(err)
	sb.WriteString("## summary 2\n" + sum + "\n")
	srcOnly := WorkSourceHuman
	_, sum, err = s.UpdateWorkItem(a.ID, WorkItemPatch{StatusSource: &srcOnly}, 0, "human:a")
	must(err)
	sb.WriteString("## summary 3\n" + sum + "\n")
	journal("updated", a)

	// links and sessions
	must(s.AttachWorkSession(a.ID, "sess-aaaabbbbcccc", "human:a"))
	must(s.DetachWorkSession(a.ID, "sess-aaaabbbbcccc", "human:a"))
	_, err = s.AddWorkLink(a.ID, WorkLinkIssue, "gofer-1", "human:a")
	must(err)
	_, err = s.RemoveWorkLink(a.ID, WorkLinkIssue, "gofer-1", "human:a")
	must(err)
	journal("links", a)

	// merge and split
	b, c := mkItem("Beta"), mkItem("Gamma")
	must(s.AttachWorkSession(c.ID, "sess-ccccddddeeee", "human:a"))
	_, err = s.MergeWorkItems(b.ID, []string{c.ID}, "human:a")
	must(err)
	journal("merge target", b)
	journal("merge source", c)
	d := mkItem("Delta")
	must(s.AttachWorkSession(d.ID, "sess-ddddeeeeffff", "human:a"))
	_, nw, err := s.SplitWorkItem(d.ID, SplitWorkItemInput{Title: "Delta part", SessionIDs: []string{"sess-ddddeeeeffff"}, By: "human:a"})
	must(err)
	name(nw.ID)
	journal("split source", d)
	journal("split new", nw)
	e := mkItem("Epsilon")
	must(s.AttachWorkSession(e.ID, "sess-eeeeffff0000", "human:a"))
	_, nw2, err := s.SplitWorkItem(e.ID, SplitWorkItemInput{Title: "Epsilon part", SessionIDs: []string{"sess-eeeeffff0000"}, KeepSessions: true, By: "human:a"})
	must(err)
	name(nw2.ID)
	journal("split keep source", e)
	_, nw3, err := s.SplitWorkItem(e.ID, SplitWorkItemInput{Title: "Epsilon bare", By: "human:a"})
	must(err)
	name(nw3.ID)
	journal("split bare source", e)

	// auto status, merge suggestion
	f := mkItem("Zeta")
	changed, err := s.SetWorkItemAutoStatus(f.ID, WorkNeedsMe, "a session waits")
	must(err)
	sb.WriteString(fmt.Sprintf("## auto status %v\n", changed))
	g := mkItem("Eta")
	_, _, err = s.AddWorkMergeSuggestion(f.ID, g.ID, "same topic", "steward")
	must(err)
	h, k := mkItem("Theta"), mkItem("Iota")
	_, _, err = s.AddWorkMergeSuggestion(h.ID, k.ID, "", "steward")
	must(err)
	journal("auto status", f)
	journal("suggest with reason", g)
	journal("suggest bare", k)

	// answered decision / interaction
	w := mkItem("Kappa")
	_, err = s.AddWorkLink(w.ID, WorkLinkPlan, "plan-abc12345", "human:a")
	must(err)
	dec := &PlanDecision{PlanID: "plan-abc12345", Title: "pick", Question: "A or B?"}
	must(s.InsertDecision(dec))
	_, err = s.AnswerDecision(dec.ID, "A", "alice")
	must(err)
	job := sampleJob("job-i1", "p", 1)
	job.PlanID = "plan-abc12345"
	must(s.UpsertJob(job))
	rec := InteractionRecord{ID: "it-1", JobID: "job-i1", Type: "permission", Prompt: "run tests?", Status: "pending", CreatedAt: 1}
	must(s.UpsertInteraction(rec))
	rec.Status, rec.Answer, rec.AnsweredBy, rec.AnsweredAt = "answered", "allow", "human", 2
	must(s.UpsertInteraction(rec))
	journal("answered", w)

	// issue-id rename note
	r := mkItem("Lambda")
	_, err = s.AddWorkLink(r.ID, WorkLinkIssue, "old-1", "human:a")
	must(err)
	tx, err := s.db.Begin()
	must(err)
	_, err = s.renameWorkLinksTx(tx, "old-1", "new-1", "", "2026-10-31T00:00:00Z")
	must(err)
	must(tx.Commit())
	journal("issue renamed", r)

	// delete event title
	job2 := sampleJob("job-del", "p", 1)
	job2.Status = "done"
	must(s.UpsertJob(job2))
	must(s.DeleteJob("job-del", "alice"))
	evs, err := s.ListJobEvents("job-del", 0)
	must(err)
	sb.WriteString("## delete events\n")
	for _, ev := range evs {
		sb.WriteString(ev.Type + " " + ev.Detail + "\n")
	}
	return sb.String()
}

// workTextExtras is the text of the helpers that are not store operations: the mail party
// labels, the short-session reason and the truncation marker of a stored session message.
func workTextExtras(t *testing.T, s *Store, lang i18n.Lang) string {
	t.Helper()
	var sb strings.Builder
	sb.WriteString("## extras\n")
	sb.WriteString(fmt.Sprintln(MailPartyLabelIn(lang, MailKindSession, "sess-a", "alice"), "|", MailPartyLabelIn(lang, MailKindJob, "job-7", ""), "|",
		MailPartyLabelIn(lang, MailKindCaller, "", ""), "|", MailPartyLabelIn(lang, MailKindCaller, "", "alice"), "|", MailPartyLabelIn(lang, "x", "", "")))
	r, _ := ShortSessionIn(lang, AgentSession{State: SessionEnded, StartedAt: 100, EndedAt: 220, HumanPrompts: 1}, 10, 3, 1000)
	sb.WriteString(r + "\n")
	m := s.capSessionMessage("sess-none", strings.Repeat("a", maxSessionLastMessage+10))
	sb.WriteString(fmt.Sprintf("%q\n", m[len(m)-20:]))
	return sb.String()
}

func TestWorkTextGolden(t *testing.T) {
	for _, lang := range i18n.Supported() {
		t.Run(string(lang), func(t *testing.T) {
			s := openTest(t)
			s.SetLanguageFor(func(string) i18n.Lang { return lang })
			golden.Check(t, lang, "work_text", workTextDump(t, s)+workTextExtras(t, s, lang))
		})
	}
}

// TestWorkTextFollowsTheItemsProject: the journal lines of a work item are written in its
// project's language (stored text: written once, history is not rewritten when the
// language later changes); a store with no resolver writes the default language.
func TestWorkTextFollowsTheItemsProject(t *testing.T) {
	s := openTest(t)
	s.SetLanguageFor(func(project string) i18n.Lang {
		if project == "p-en" {
			return i18n.EN
		}
		return i18n.ZhCN
	})
	en, err := s.CreateWorkItem(WorkItemInput{Title: "t", ProjectKey: "p-en", By: "human:a"})
	if err != nil {
		t.Fatal(err)
	}
	zh, err := s.CreateWorkItem(WorkItemInput{Title: "t", ProjectKey: "p-zh", By: "human:a"})
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{en.ID: "Work item created", zh.ID: "创建工作项"} {
		lines, err := s.ListWorkJournal(id, 5, 0)
		if err != nil || len(lines) != 1 || lines[0].Text != want {
			t.Errorf("journal of %s = %+v (%v), want %q", id, lines, err, want)
		}
	}
	prio := 3
	if _, sum, err := s.UpdateWorkItem(en.ID, WorkItemPatch{Priority: &prio, Goal: nil}, 0, "human:a"); err != nil || sum != "Priority: 3" {
		t.Errorf("en summary = %q (%v)", sum, err)
	}
	s.SetLanguageFor(nil)
	if _, sum, err := s.UpdateWorkItem(zh.ID, WorkItemPatch{Priority: &prio}, 0, "human:a"); err != nil || sum != "优先级：3" {
		t.Errorf("default summary = %q (%v)", sum, err)
	}
}
