package today

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/steward"
)

// The golden files under testdata/i18n_golden pin the text the today page generates
// (card tags, titles, summaries, block reasons, action labels, lanes, status alerts ...).
// today.zh-CN.txt was recorded from the code BEFORE the text moved into the catalog, so a
// change to it changes what every zh-CN deployment shows. today.en.txt is the English
// rewrite. GOFER_GOLDEN_UPDATE=1 rewrites either after a deliberate edit.

var han = regexp.MustCompile(`\p{Han}`)

func dumpCard(sb *strings.Builder, c Card) {
	fmt.Fprintf(sb, "[%s] kind=%s tag=%q urgency=%s\n", c.Key, c.Kind, c.Tag, c.Urgency)
	fmt.Fprintf(sb, "  title=%q\n  summary=%q\n  blocks=%d/%d %q\n", c.Title, c.Summary, c.Blocks.Score, c.Blocks.Items, c.Blocks.Text)
	for _, a := range c.Actions {
		fmt.Fprintf(sb, "  action %s %q value=%q\n", a.ID, a.Label, a.Value)
	}
	for _, sg := range c.Suggestions {
		fmt.Fprintf(sb, "  suggestion %s %q\n", sg.Field, sg.Text)
	}
	if c.Woke {
		fmt.Fprintf(sb, "  woke=%q\n", c.WokeReason)
	}
}

func todayTextOutputs(t *testing.T, lang i18n.Lang) string {
	t.Helper()
	svc, st := newTestService(t)
	svc.d.Language = func() i18n.Lang { return lang } // the server language: stored text follows it
	var sb strings.Builder
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	// --- interactions: a permission request, a question, a confirmation, a held job
	must(st.InsertPlan(jobstore.Plan{PlanID: "p-1", Title: "Export", Status: jobstore.PlanBlocked, BlockedTodo: "t1", ProjectKey: "p1"}))
	must(st.InsertTodo(jobstore.PlanTodo{TodoID: "t1", PlanID: "p-1", Title: "Backend", Status: jobstore.TodoDoing, DispatchError: ""}))
	must(st.InsertTodo(jobstore.PlanTodo{TodoID: "t2", PlanID: "p-1", Title: "Frontend", After: []string{"t1"}}))
	must(st.SetPlanBlocked("p-1", "t1"))
	putJob(t, st, jobstore.JobRecord{ID: "j-run", ProjectKey: "p1", Agent: "codex", Status: job.StatusRunning, WorktreePath: "/wt/1", PlanID: "p-1", TodoID: "t1", StartedAt: ago(60 * 30)})
	must(st.UpsertInteraction(jobstore.InteractionRecord{ID: "i1", JobID: "j-run", Type: job.InteractionTypePermission,
		Prompt: "go test ./...", Status: "pending", CreatedAt: ago(25),
		OptionsJSON:  `[{"value":"a1","label":"Allow","kind":"allow_once"}]`,
		ToolCallJSON: `{"id":"tc","title":"run tests"}`}))
	putJob(t, st, jobstore.JobRecord{ID: "j-dir", ProjectKey: "p1", Agent: "claude", Status: job.StatusRunning, DirExclusive: true})
	must(st.UpsertInteraction(jobstore.InteractionRecord{ID: "i2", JobID: "j-dir", Type: job.InteractionTypePermission,
		Prompt: "x", Status: "pending", CreatedAt: ago(60 * 3)}))
	putJob(t, st, jobstore.JobRecord{ID: "j-day", ProjectKey: "p1", Agent: "claude", Status: job.StatusRunning})
	must(st.UpsertInteraction(jobstore.InteractionRecord{ID: "i3", JobID: "j-day", Type: job.InteractionTypeConfirmation,
		Prompt: "Proceed?", Status: "pending", CreatedAt: ago(60 * 24 * 2)}))

	// --- decisions: plan question, global question without options, a permission, a form question, a relay
	must(st.InsertDecision(&jobstore.PlanDecision{ID: "dq", PlanID: "p-1", Title: "Plan question", Question: "Which?", AskedAt: ago(5), TimeoutSec: 86400}))
	must(st.InsertDecision(&jobstore.PlanDecision{ID: "dg", Title: "Global", Question: "Free text?", AskedAt: ago(4), TimeoutSec: 86400}))
	_, err := st.UpsertAgentSession(jobstore.AgentSession{SessionID: "sid-1", Agent: "claude", ProjectKey: "p1", Title: "repo: cleanup"})
	must(err)
	must(st.InsertDecision(&jobstore.PlanDecision{ID: "dp", Title: "perm", Question: "q", SessionID: "sid-1", Kind: jobstore.DecisionKindPermission, TimeoutSec: 600, AskedAt: ago(1),
		Detail: `{"tool_name":"Bash","summary":"Bash ls","suggestions":[{"label":"rule"},{"label":""}],"fp":"x"}`}))
	_, err = st.UpsertAgentSession(jobstore.AgentSession{SessionID: "sid-2", Agent: "claude", ProjectKey: "p1", Title: "form"})
	must(err)
	must(st.InsertDecision(&jobstore.PlanDecision{ID: "dform", Title: "form", Question: "q", SessionID: "sid-2", Kind: jobstore.DecisionKindPermission, TimeoutSec: 600, AskedAt: ago(1),
		Detail: `{"tool_name":"AskUserQuestion","summary":"s","questions":[{"question":"Pick","options":[{"label":"One"},{"label":"Two"}]}]}`}))
	_, err = st.UpsertAgentSession(jobstore.AgentSession{SessionID: "sid-3", Agent: "claude", ProjectKey: "p1", Title: "relay s", State: jobstore.SessionWaitingReply})
	must(err)
	must(st.InsertDecision(&jobstore.PlanDecision{ID: "dr", Title: "turn", Question: "q", Kind: jobstore.DecisionKindRelay, SessionID: "sid-3", AskedAt: ago(10), TimeoutSec: 86400}))

	// --- review, approval
	putJob(t, st, jobstore.JobRecord{ID: "j-rev", ProjectKey: "p1", Agent: "codex", Status: job.StatusNeedsReview, EndedAt: ago(300), WorktreePath: "/wt/2", PlanID: "p-1", TodoID: "t1"})
	putJob(t, st, jobstore.JobRecord{ID: "j-hold", ProjectKey: "p1", Agent: "codex", Status: job.StatusAwaitingApproval, HoldExpiresAt: testNow.Unix() + 86400,
		HoldJSON: `{"reason":"push","timeout_sec":86400,"expires_at":` + itoa(testNow.Unix()+86400) + `}`})

	// --- work cards: needs_me with an online session, needs_onsite, a due parked item
	_, err = st.UpsertAgentSession(jobstore.AgentSession{SessionID: "s-w", Agent: "claude", ProjectKey: "p1", State: jobstore.SessionIdle})
	must(err)
	w1, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "Needs me item", Status: jobstore.WorkNeedsMe, SessionIDs: []string{"s-w"}, By: "me"})
	must(err)
	w2, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "Onsite item", Status: jobstore.WorkNeedsOnsite, By: "me"})
	must(err)
	w3, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "Due parked", Status: jobstore.WorkParked, By: "me"})
	must(err)
	past := ago(5)
	_, _, err = st.UpdateWorkItem(w3.ID, jobstore.WorkItemPatch{RemindAt: &past}, 0, "me")
	must(err)
	w4, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "Status hint item", Status: jobstore.WorkParked, By: "me"})
	must(err)
	w5, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "Merge target", Status: jobstore.WorkActive, By: "me"})
	must(err)
	w6, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "Merge source", Status: jobstore.WorkActive, By: "me"})
	must(err)
	_ = w2
	for _, sg := range []jobstore.WorkSuggestion{
		{WorkItemID: w1.ID, Field: jobstore.SuggestGoal, Value: "iframe", By: "summarizer"},
		{WorkItemID: w4.ID, Field: jobstore.SuggestStatusHint, Value: jobstore.WorkReview, By: "summarizer"},
		{WorkItemID: w4.ID, Field: jobstore.SuggestGoal, Value: "new goal", By: "summarizer"},
	} {
		_, err := st.UpsertWorkSuggestion(sg)
		must(err)
	}
	_, _, err = st.AddWorkMergeSuggestion(w5.ID, w6.ID, "", "steward")
	must(err)

	// --- capture box, held mail
	for _, text := range []string{"idea", "idea"} {
		_, err := st.CaptureWorkItem(jobstore.CaptureInput{Text: text, Kind: jobstore.WorkKindIdea, By: "human:t"})
		must(err)
	}
	for _, in := range []jobstore.MailInput{
		{FromKind: jobstore.MailKindSession, FromID: "s-abcdef1234567", FromCaller: "c1", ToAddress: "topic:nobody", Body: "first"},
		{FromKind: jobstore.MailKindJob, FromID: "job-1", ToAddress: "path:nowhere", Body: "second"},
		{FromKind: jobstore.MailKindCaller, FromCaller: "c2", ToAddress: "", Body: "third"},
	} {
		heldMail(t, st, in)
	}

	// --- memory suggestions
	seedMemories(t, st)
	sugg := func(key, action string, p MemoryPayload) {
		_, _, err := svc.SuggestMemory(MemorySuggestInput{TrackerID: "trk", Key: key, Action: action, Reason: "reason", Payload: p}, "s", "")
		must(err)
	}
	sugg("old-note", MemoryActArchive, MemoryPayload{})
	sugg("release-b", MemoryActMerge, MemoryPayload{Into: "release-a"})
	sugg("fresh", MemoryActSummary, MemoryPayload{Summary: "A new fact"})
	sugg("release-a", MemoryActWhen, MemoryPayload{Keywords: []string{"ship", "release"}})
	sugg("fresh", MemoryActKind, MemoryPayload{Kind: "rule"})

	cards, err := shimDecisions(lang, svc)
	must(err)
	sortedByKey := append([]Card(nil), cards...)
	order := func(c Card) string { return c.Kind + "|" + c.Title + "|" + c.Summary }
	for i := range sortedByKey {
		for j := i + 1; j < len(sortedByKey); j++ {
			if order(sortedByKey[j]) < order(sortedByKey[i]) {
				sortedByKey[i], sortedByKey[j] = sortedByKey[j], sortedByKey[i]
			}
		}
	}
	sb.WriteString("## cards\n")
	for _, c := range sortedByKey {
		dumpCard(&sb, c)
	}

	sb.WriteString("## memory suggestions\n")
	list, err := shimMemorySuggestions(lang, svc)
	must(err)
	for _, m := range list {
		fmt.Fprintf(&sb, "%s %s => %q\n", m.Action, m.Key, m.Text)
	}

	// --- snooze labels (recorded in the handled audit) and the handled rows
	_, err = svc.Snooze(SnoozeInput{CardKey: "review:j-rev", UntilAt: testNow.Unix() + 3600}, "me")
	must(err)
	_, err = svc.Snooze(SnoozeInput{CardKey: "approval:j-hold", UntilJobID: "j-run"}, "me")
	must(err)
	rows, err := svc.HandledSince(7)
	must(err)
	sb.WriteString("## handled labels\n")
	var labels []string
	for _, r := range rows {
		labels = append(labels, r.Label)
	}
	for i := range labels {
		for j := i + 1; j < len(labels); j++ {
			if labels[j] < labels[i] {
				labels[i], labels[j] = labels[j], labels[i]
			}
		}
	}
	sb.WriteString(strings.Join(labels, "\n") + "\n")

	// --- today response: status alerts + digest
	resp, err := shimToday(lang, svc, Query{})
	must(err)
	sb.WriteString("## status alerts\n" + strings.Join(resp.Status.Alerts, "\n") + "\n")
	sb.WriteString("## digest\n" + resp.Digest.Title + "\n" + resp.Digest.Text + "\n")

	// steward alerts (a failed review and an unusable agent)
	svc.d.Steward = fakeSteward{}
	resp2, err := shimToday(lang, svc, Query{})
	must(err)
	sb.WriteString("## steward alerts\n" + strings.Join(resp2.Status.Alerts, "\n") + "\n")

	// --- worker enroll card
	ec, _ := shimEnrollCard(lang, []EnrollPending{
		{Hostname: "h1", RequestedID: "w-1", CreatedAt: 1, DecideUntil: 100}, {Hostname: "h2", CreatedAt: 2, DecideUntil: 90},
		{Hostname: "h3", CreatedAt: 3}, {Hostname: "h4", CreatedAt: 4}})
	sb.WriteString("## enroll card\n")
	dumpCard(&sb, ec)

	// --- lanes
	lv, err := shimLanes(lang, svc.d.Work)
	must(err)
	sb.WriteString("## lanes\n")
	var laneLines []string
	for _, l := range lv.Lanes {
		laneLines = append(laneLines, fmt.Sprintf("%s health=%s reason=%q", l.Kind+":"+l.Title, l.Health, l.HealthReason))
	}
	sort.Strings(laneLines)
	sb.WriteString(strings.Join(laneLines, "\n") + "\n")

	out := sb.String()
	// ids the store generates are masked
	out = regexp.MustCompile(`\b(?:w|wm)-[0-9a-f]{6,}\b`).ReplaceAllString(out, "<ID>")
	out = regexp.MustCompile(`"(?:work|suggestion|merge):[^"]*"`).ReplaceAllStringFunc(out, func(m string) string { return m })
	return out
}

func TestTodayTextGolden(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.ZhCN, i18n.EN} {
		t.Run(string(lang), func(t *testing.T) {
			got := todayTextOutputs(t, lang)
			path := filepath.Join("testdata", "i18n_golden", "today."+string(lang)+".txt")
			if os.Getenv("GOFER_GOLDEN_UPDATE") == "1" {
				must := func(err error) {
					if err != nil {
						t.Fatal(err)
					}
				}
				must(os.MkdirAll(filepath.Dir(path), 0o755))
				must(os.WriteFile(path, []byte(got), 0o644))
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Errorf("today text for %s changed (GOFER_GOLDEN_UPDATE=1 to rewrite)\n--- got ---\n%s\n--- want ---\n%s", lang, got, want)
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

type fakeSteward struct{}

func (fakeSteward) Status() steward.Status {
	return steward.Status{Enabled: true, AgentError: "x",
		LastReview: &jobstore.StewardReview{Day: testNow.Format("2006-01-02"), State: jobstore.StewardReviewFailed}}
}

var _ = json.Marshal
var _ = time.Second
