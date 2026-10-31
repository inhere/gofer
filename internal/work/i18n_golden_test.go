package work

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
)

// The golden files under testdata/i18n_golden pin the text the work service writes into
// an item's journal or hands to a person (outcome lines, report notes, request lines,
// digest, health reasons ...). The *.zh-CN.txt file was recorded from the code BEFORE
// the text moved into the catalog, so a change to it changes what every zh-CN deployment
// already has in its journals. *.en.txt is the English rewrite. GOFER_GOLDEN_UPDATE=1
// rewrites either after a deliberate edit.

var goldenIDs = regexp.MustCompile(`\b(?:wr|w|wi|wrq|req)-[0-9a-z]+\b`)

func ptr[T any](v T) *T { return &v }

func journalLines(t *testing.T, st *jobstore.Store, id string) []string {
	t.Helper()
	list, err := st.ListWorkJournal(id, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for i, e := range list {
		if i == 0 || storeLine.MatchString(e.Text) {
			continue // the store's own lines are not the work service's text
		}
		out = append(out, e.Text)
	}
	return out
}

// storeLine matches the journal lines jobstore writes itself (links, field edits).
var storeLine = regexp.MustCompile(`^(关联 |目标已更新|状态：\S+ → |阻塞：.*；|搁置条件：.*；)|提醒时间：|搁置至：`)

func workTextOutputs(t *testing.T, lang i18n.Lang) string {
	t.Helper()
	var sb strings.Builder
	sect := func(name string, lines ...string) {
		sb.WriteString("## " + name + "\n")
		for _, l := range lines {
			sb.WriteString(l + "\n")
		}
	}
	svc, st, n := newSvc(t)
	svc.SetLanguageFor(func(string) i18n.Lang { return lang })
	now := time.Unix(1_800_000_000, 0)
	svc.SetNow(func() time.Time { return now })
	yes := true
	svc.SetConfigFn(func() config.WorkConfig { return config.WorkConfig{NeedsMeNotify: &yes} })
	mk := func(title string, in jobstore.WorkItemInput) jobstore.WorkItem {
		in.Title = title
		if in.By == "" {
			in.By = "human:alice"
		}
		w, err := st.CreateWorkItem(in)
		if err != nil {
			t.Fatal(err)
		}
		return w
	}

	// --- pure producers
	var lines []string
	for _, s := range []string{"active", "needs_me", "waiting_resource", "needs_onsite", "review", "parked", "done", "dropped", "custom"} {
		lines = append(lines, s+" => "+shimStatusLabel(lang, s))
	}
	sect("status labels", lines...)

	lines = nil
	for _, o := range []JobOutcome{
		{ID: "job-12345678abc", Agent: "codex", Title: "export csv", Status: "done", Commits: 3},
		{ID: "j1", Status: "done"},
		{ID: "j1", Status: "needs_review", Commits: 1},
		{ID: "j1", Status: "failed", Error: "exit  1\nboom"},
		{ID: "j1", Status: "timeout"},
		{ID: "j1", Status: "failed", FailureClass: "budget"},
		{ID: "j1", Status: "cancelled"},
		{ID: "j1", Status: "rejected", Reviewed: true},
		{ID: "j1", Status: "done", Reviewed: true},
		{ID: "j1", Status: "weird"},
	} {
		text, level := shimOutcomeLine(lang, o)
		lines = append(lines, level+" | "+text)
	}
	sect("outcome lines", lines...)

	lines = nil
	h := int64(3600)
	stall := 2 * time.Hour
	nowU := now.Unix()
	jf := &jobstore.JobRecord{ID: "job-abcdefgh1234", Status: "failed"}
	jb := &jobstore.JobRecord{ID: "job-abcdefgh1234", Status: "failed", FailureClass: "budget"}
	for _, in := range []HealthInput{
		{BlockedPlan: "p1"},
		{BlockedPlan: "「Quoted」"},
		{Blocker: "waiting   for the\n device", NeedsPerson: true},
		{InProgress: true, LastActivity: nowU - 3*24*h},
		{InProgress: true, LastActivity: nowU - 5*h},
		{InProgress: true, LastActivity: nowU - 3*h},
		{LatestJob: jf},
		{LatestJob: jb},
		{},
	} {
		hl, r := shimComputeHealth(lang, in, nowU, stall)
		lines = append(lines, hl+" | "+r)
	}
	sect("health reasons", lines...)

	lines = nil
	for _, m := range []jobstore.Mail{
		{ToKind: jobstore.MailKindSession, ToID: "sess-a"},
		{ToKind: jobstore.MailKindJob, ToID: "job-7"},
		{ToAddress: "topic:x"},
	} {
		lines = append(lines, shimMailTarget(lang, m))
	}
	sect("mail target", lines...)

	lines = nil
	sess := func(state string) jobstore.AgentSession { return jobstore.AgentSession{State: state} }
	for _, c := range []struct {
		s    []jobstore.AgentSession
		j    map[string]JobState
		wait string
	}{
		{s: []jobstore.AgentSession{sess(jobstore.SessionWaitingReply)}},
		{s: []jobstore.AgentSession{sess(jobstore.SessionNeedsAttention)}},
		{j: map[string]JobState{"a": {PendingInteraction: true}}},
		{wait: "WAIT"},
		{j: map[string]JobState{"a": {Status: statusNeedsReview}}},
		{s: []jobstore.AgentSession{sess(jobstore.SessionRunning)}},
	} {
		st1, r, _ := shimDerived(lang, c.s, c.j, c.wait)
		lines = append(lines, st1+" | "+r)
	}
	sect("derived status reasons", lines...)

	lines = nil
	for _, f := range []string{jobstore.SuggestGoal, jobstore.SuggestBlocker, jobstore.SuggestBlockerKind, jobstore.SuggestNext, jobstore.SuggestSummary, jobstore.SuggestStatusHint, "custom"} {
		lines = append(lines, f+" => "+shimSuggestionLabel(lang, f))
	}
	sect("suggestion labels", lines...)

	sect("todo note", shimTodoNote(lang, jobstore.WorkItem{ID: "w-1", Title: "T", CaptureText: "capture", Goal: "goal", NextStep: "step"}))

	// --- report notes
	cap := mustCaptured(t, st, "captured one")
	rep := func(name, id string, in ReportInput) {
		in.By = "session:s1"
		if _, err := svc.Report(id, in); err != nil {
			sect(name, "ERR "+err.Error())
			return
		}
		sect(name, journalLines(t, st, id)...)
	}
	rep("report captured", cap.ID, ReportInput{Goal: "G", Status: "active"})
	a := mk("report a", jobstore.WorkItemInput{})
	rep("report final", a.ID, ReportInput{Status: "done"})
	b := mk("report b", jobstore.WorkItemInput{})
	rep("report unblocked", b.ID, ReportInput{Status: "active", Summary: "S"})
	c := mk("report c", jobstore.WorkItemInput{})
	_, _, _ = st.UpdateWorkItem(c.ID, jobstore.WorkItemPatch{Status: ptr("parked"), StatusSource: ptr(jobstore.WorkSourceHuman)}, 0, "human:alice")
	rep("report human", c.ID, ReportInput{Status: "waiting_resource"})
	d := mk("report d", jobstore.WorkItemInput{ProjectKey: "p1"})
	_, _, _ = st.UpdateWorkItem(d.ID, jobstore.WorkItemPatch{StatusSource: ptr(jobstore.WorkSourceAuto)}, 0, "x")
	rep("report default", d.ID, ReportInput{Goal: "G", Blocker: "B", Next: "N", Summary: "S", Status: "needs_me"})
	var got []string
	for _, s := range n.got {
		got = append(got, s.event+" | "+s.title+" | "+strings.ReplaceAll(s.text, "\n", "\\n"))
	}
	sect("needs_me notifications", got...)
	n.got = nil

	// steward update notes
	cap2 := mustCaptured(t, st, "captured two")
	r1, _ := svc.StewardUpdate(cap2.ID, jobstore.WorkItemPatch{Status: ptr("active")}, 0, "steward")
	hum := mk("human item", jobstore.WorkItemInput{})
	_, _, _ = st.UpdateWorkItem(hum.ID, jobstore.WorkItemPatch{Status: ptr("parked"), StatusSource: ptr(jobstore.WorkSourceHuman)}, 0, "human:alice")
	r2, _ := svc.StewardUpdate(hum.ID, jobstore.WorkItemPatch{Status: ptr("waiting_resource")}, 0, "steward")
	other := mk("steward needs me", jobstore.WorkItemInput{})
	_, _ = svc.StewardUpdate(other.ID, jobstore.WorkItemPatch{Status: ptr("needs_me")}, 0, "steward")
	got = nil
	for _, s := range n.got {
		got = append(got, s.event+" | "+s.title+" | "+strings.ReplaceAll(s.text, "\n", "\\n"))
	}
	sect("steward notes", append(append(r1.Notes, r2.Notes...), got...)...)
	n.got = nil

	// park -> needs_me notification reason
	pk := mk("park me", jobstore.WorkItemInput{})
	_, _ = svc.Update(pk.ID, jobstore.WorkItemPatch{Status: ptr("needs_me")}, 0, "human:bob")
	got = nil
	for _, s := range n.got {
		got = append(got, s.event+" | "+s.title+" | "+strings.ReplaceAll(s.text, "\n", "\\n"))
	}
	sect("update needs_me notification", got...)
	n.got = nil

	// --- dispatch close
	c1 := mustCaptured(t, st, "to archive")
	_, _ = svc.DispatchClose(c1.ID, DispatchArchive, "human:alice")
	c2 := mustCaptured(t, st, "to drop")
	_, _ = svc.DispatchClose(c2.ID, DispatchDrop, "human:alice")
	sect("dispatch close", append(journalLines(t, st, c1.ID), journalLines(t, st, c2.ID)...)...)

	// --- ask log
	ak := mk("ask item", jobstore.WorkItemInput{})
	svc.logAsk(ak.ID, "sess-abcdefghijkl", "please look at this", "human:alice")
	sect("ask log", journalLines(t, st, ak.ID)...)

	// --- reminders
	rm := mk("remind me", jobstore.WorkItemInput{ProjectKey: "p1", Goal: "confirm", Status: "parked"})
	_, _, _ = st.UpdateWorkItem(rm.ID, jobstore.WorkItemPatch{RemindAt: ptr(nowU - 5), ParkNote: ptr("after arrival"), NextStep: ptr("call"), BlockerText: ptr("no stock")}, 0, "h")
	pk2 := mk("park due", jobstore.WorkItemInput{})
	_, _ = svc.Park(pk2.ID, nowU-1, "arrives", "human:a")
	var dueReasons []string
	svc.SetDueHook(func(w jobstore.WorkItem, reason string, at int64) { dueReasons = append(dueReasons, reason) })
	n.got = nil
	svc.Tick(now)
	got = nil
	for _, s := range n.got {
		got = append(got, s.event+" | "+s.title+" | "+strings.ReplaceAll(s.text, "\n", "\\n"))
	}
	sort.Strings(got)
	sort.Strings(dueReasons)
	sect("reminders", append(append(got, dueReasons...), append(journalLines(t, st, rm.ID), journalLines(t, st, pk2.ID)...)...)...)
	n.got = nil

	// --- requests
	sectReq := func(name string, mkItem func() jobstore.WorkItem, opts RequestOpts, setup func(*fakeMessenger)) {
		w := mkItem()
		m := &fakeMessenger{}
		svc.SetMessenger(m)
		if setup != nil {
			setup(m)
		}
		out, err := svc.RequestSessions(context.Background(), w.ID, opts)
		var res []string
		if err != nil {
			res = append(res, "ERR "+err.Error())
		}
		for _, o := range out {
			res = append(res, fmt.Sprintf("kind=%s sent=%v reason=%s", o.Kind, o.Sent, o.Reason))
		}
		svc.WaitIdle()
		res = append(res, journalLines(t, st, w.ID)...)
		sect(name, res...)
	}
	mkWith := func(sid, state string) func() jobstore.WorkItem {
		return func() jobstore.WorkItem {
			if state != "" {
				session(t, st, sid, state)
			}
			return mk("req "+sid, jobstore.WorkItemInput{SessionIDs: []string{sid}})
		}
	}
	_ = st.UpsertJob(jobstore.JobRecord{ID: "job-as-session", Status: "running", RequestJSON: "{}"})
	sectReq("request job session", mkWith("job-as-session", ""), RequestOpts{}, nil)
	sectReq("request gone", mkWith("sess-gone0001", ""), RequestOpts{}, nil)
	sectReq("request denied", mkWith("sess-deny0001", jobstore.SessionRunning), RequestOpts{Allow: func(string) bool { return false }}, nil)
	sectReq("request not running", mkWith("sess-ended001", jobstore.SessionEnded), RequestOpts{}, nil)
	sectReq("request delivery failed", mkWith("sess-fail0001", jobstore.SessionRunning), RequestOpts{}, func(m *fakeMessenger) { m.fail = errors.New("no address") })
	sectReq("request sent handoff", mkWith("sess-sent0001", jobstore.SessionRunning), RequestOpts{Kind: jobstore.WorkRequestHandoff}, nil)
	sectReq("request sent report", mkWith("sess-sent0002", jobstore.SessionRunning), RequestOpts{}, nil)

	// answered + expiry
	ex := mk("expiry", jobstore.WorkItemInput{SessionIDs: []string{"sess-sent0001"}})
	svc.SetMessenger(&fakeMessenger{})
	out, _ := svc.RequestSessions(context.Background(), ex.ID, RequestOpts{})
	svc.WaitIdle()
	if len(out) > 0 {
		_, _ = svc.Report(ex.ID, ReportInput{Summary: "done", RequestID: out[0].RequestID, By: "session:s"})
	}
	ex2 := mk("expiry2", jobstore.WorkItemInput{SessionIDs: []string{"sess-sent0002"}})
	_, _ = svc.RequestSessions(context.Background(), ex2.ID, RequestOpts{})
	svc.WaitIdle()
	svc.advanceRequests(now.Add(2 * time.Hour))
	svc.WaitIdle()
	reqs, _ := st.ListWorkRequests(ex2.ID, false, 10)
	var rl []string
	for _, r := range reqs {
		if r.Kind == jobstore.WorkRequestSummarize {
			continue
		}
		rl = append(rl, r.Kind+" "+r.State+" "+r.Error)
	}
	sect("request answered", journalLines(t, st, ex.ID)...)
	sect("request expired", append(rl, journalLines(t, st, ex2.ID)...)...)

	// --- completion write-back (todo + issue)
	cw := mk("completion", jobstore.WorkItemInput{})
	now0 := nowU
	_ = st.InsertPlan(jobstore.Plan{PlanID: "plan-g", Title: "g", Status: jobstore.PlanOpen, CreatedAt: now0, UpdatedAt: now0})
	_ = st.InsertTodo(jobstore.PlanTodo{TodoID: "todo-g", PlanID: "plan-g", Title: "a", Status: jobstore.TodoDone, CreatedAt: now0, UpdatedAt: now0})
	_, _ = st.AddWorkLink(cw.ID, jobstore.WorkLinkTodo, "todo-g", "h")
	svc.checkLinkedCompletion(cw.ID)
	cw2 := mk("completion issue", jobstore.WorkItemInput{})
	_, _ = st.AddWorkLink(cw2.ID, jobstore.WorkLinkTodo, "todo-g", "h")
	_, _ = st.AddWorkLink(cw2.ID, jobstore.WorkLinkIssue, "g-9", "h")
	_ = st.UpsertTrackerIssue(jobstore.TrackerRecord{TrackerID: "t1", ID: "g-9", Body: []byte(`{"status":"closed"}`), Rev: 1})
	svc.checkLinkedCompletion(cw2.ID)
	sect("completion", append(journalLines(t, st, cw.ID), journalLines(t, st, cw2.ID)...)...)

	// --- suggestions adopt / dismiss
	sg := mk("suggest", jobstore.WorkItemInput{})
	_, _ = st.UpsertWorkSuggestion(jobstore.WorkSuggestion{WorkItemID: sg.ID, Field: jobstore.SuggestGoal, Value: "new goal", By: "summarizer"})
	_, _ = st.UpsertWorkSuggestion(jobstore.WorkSuggestion{WorkItemID: sg.ID, Field: jobstore.SuggestNext, Value: "x", By: "summarizer"})
	_, aerr := svc.AcceptSuggestion(sg.ID, jobstore.SuggestGoal, "human:alice")
	derr := svc.DismissSuggestion(sg.ID, jobstore.SuggestNext, "human:alice")
	sect("suggestions", append([]string{fmt.Sprint(aerr, derr)}, journalLines(t, st, sg.ID)...)...)

	// --- job outcome journal
	jo := mk("job outcome", jobstore.WorkItemInput{})
	_, _ = st.AddWorkLink(jo.ID, jobstore.WorkLinkJob, "job-77", "h")
	svc.NoteJobOutcome(JobOutcome{ID: "job-77", Agent: "codex", Status: "done", Commits: 2})
	sect("job outcome journal", journalLines(t, st, jo.ID)...)

	// --- digest
	dg := svc
	old := mk("old parked", jobstore.WorkItemInput{Status: "parked"})
	_, _, _ = st.UpdateWorkItem(old.ID, jobstore.WorkItemPatch{Status: ptr("parked")}, 0, "h")
	dgst, err := shimDigest(lang, dg, now.Add(30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	sect("digest", dgst.Title, dgst.Text)

	// empty digest, and one with yesterday's progress and the steward's comment
	e1, _, _ := newSvc(t)
	e1.SetLanguageFor(func(string) i18n.Lang { return lang })
	ed, _ := shimDigest(lang, e1, now)
	sect("digest empty", ed.Text)
	y1, ys, _ := newSvc(t)
	y1.SetLanguageFor(func(string) i18n.Lang { return lang })
	yday := now.Add(-24 * time.Hour)
	ys.SetClock(func() time.Time { return yday })
	for range 12 {
		if _, err := ys.CreateWorkItem(jobstore.WorkItemInput{Title: "yday", Status: "active", By: "h"}); err != nil {
			t.Fatal(err)
		}
	}
	ys.SetClock(func() time.Time { return now })
	rid, err := ys.BeginStewardReview(now.Format("2006-01-02"), "manual", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ys.FinishStewardReview(rid, jobstore.StewardReviewDone, "all calm", "", ""); err != nil {
		t.Fatal(err)
	}
	yd, _ := shimDigest(lang, y1, now)
	sect("digest yesterday", yd.Text)

	body := sb.String()
	// Mask generated ids so the file is stable.
	body = goldenIDs.ReplaceAllString(body, "<ID>")
	return sortLinkRuns(body)
}

func goldenPath(lang i18n.Lang) string {
	return filepath.Join("testdata", "i18n_golden", "work."+string(lang)+".txt")
}

func TestWorkTextGolden(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.ZhCN, i18n.EN} {
		t.Run(string(lang), func(t *testing.T) {
			got := workTextOutputs(t, lang)
			path := goldenPath(lang)
			if os.Getenv("GOFER_GOLDEN_UPDATE") == "1" {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Errorf("work text for %s changed (GOFER_GOLDEN_UPDATE=1 to rewrite)\n--- got ---\n%s\n--- want ---\n%s", lang, got, want)
			}
			if lang == i18n.EN {
				for i, l := range strings.Split(got, "\n") {
					if han.MatchString(l) {
						t.Errorf("en golden line %d carries Han: %q", i+1, l)
					}
				}
			}
		})
	}
}

// sortLinkRuns sorts each run of "- [" digest lines (the digest orders them by random ids).
func sortLinkRuns(body string) string {
	lines := strings.Split(body, "\n")
	for i := 0; i < len(lines); {
		if !strings.HasPrefix(lines[i], "- [") {
			i++
			continue
		}
		j := i
		for j < len(lines) && strings.HasPrefix(lines[j], "- [") {
			j++
		}
		sort.Strings(lines[i:j])
		i = j
	}
	return strings.Join(lines, "\n")
}
