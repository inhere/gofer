package steward

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/work"
)

// han matches a Han character: text written for English must carry none.
var han = regexp.MustCompile(`\p{Han}`)

// migratedFiles are the steward files whose agent-facing text lives in internal/i18n
// (design 2026-10-27-i18n-design.md §7): the prime, the review prompt, its dynamic steps,
// the event summaries and the session's title / first turn / question wrapper. A string
// literal added to one of them must not be Chinese.
var migratedFiles = []string{
	"prime.go", "review.go", "today.go", "memory.go", "events.go", "steward.go",
}

// primeClock is the fixed clock every prime / review test renders at.
var primeClock = time.Date(2026, 11, 5, 9, 5, 0, 0, time.UTC)

// fixtureItems are hand-built item views: the prime renders what the store returns, so a
// hand-built view is what makes the rendered line independent of wall-clock time.
func fixtureItems() []work.ItemView {
	due := int64(1793000000)
	return []work.ItemView{
		{
			WorkItem: jobstore.WorkItem{
				ID: "W-1", Title: "修登录页", Status: jobstore.WorkNeedsMe,
				BlockerText: "等供应商报价", NextStep: "打样并验收",
			},
			LastActivityAt: 1792990000,
			Requests: []jobstore.WorkRequest{
				{ID: "r-7", Kind: jobstore.WorkRequestReport, State: jobstore.WorkRequestPending, By: "system"},
				{ID: "r-8", Kind: jobstore.WorkRequestHandoff, State: jobstore.WorkRequestAnswered},
			},
		},
		{
			WorkItem: jobstore.WorkItem{
				ID: "W-2", Title: "整理线路", Status: jobstore.WorkActive,
				Unsorted: true, RemindAt: due,
			},
			LastActivityAt: 1792980000,
			Due:            true,
			SessionOffline: true,
		},
	}
}

// zhGolden reads the pinned text of the zh-CN output captured from the code before the
// migration (testdata/*.txt).
func zhGolden(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	return strings.TrimRight(string(raw), "\n")
}

// TestZhStewardTextIsUnchanged pins the zh-CN text of every migrated helper CHARACTER FOR
// CHARACTER: the migration moved the text into internal/i18n and must not have changed a
// single one of them for an existing deployment.
func TestZhStewardTextIsUnchanged(t *testing.T) {
	zh := i18n.ZhCN
	items := fixtureItems()
	cases := []struct{ name, got, want string }{
		{"item lines", itemLine(zh, items[0], primeClock) + "\n" + itemLine(zh, items[1], primeClock),
			zhGolden(t, "item-lines.txt")},
		{"ago none", ago(zh, primeClock, 0), "无"},
		{"ago just now", ago(zh, primeClock, primeClock.Add(-30*time.Second).Unix()), "刚刚"},
		{"ago minutes", ago(zh, primeClock, primeClock.Add(-5*time.Minute).Unix()), "5分钟前"},
		{"ago hours", ago(zh, primeClock, primeClock.Add(-5*time.Hour).Unix()), "5小时前"},
		{"ago days", ago(zh, primeClock, primeClock.Add(-72*time.Hour).Unix()), "3天前"},
		{"weekday", weekday(zh, 0) + weekday(zh, 1) + weekday(zh, 2) + weekday(zh, 3) +
			weekday(zh, 4) + weekday(zh, 5) + weekday(zh, 6), "周日周一周二周三周四周五周六"},
		{"request kinds", requestKindLabel(zh, jobstore.WorkRequestReport) + "/" +
			requestKindLabel(zh, jobstore.WorkRequestHandoff) + "/" +
			requestKindLabel(zh, jobstore.WorkRequestSummarize), "汇报请求/交接请求/整理"},
		{"request states", requestStateLabel(zh, jobstore.WorkRequestPending) + "/" +
			requestStateLabel(zh, jobstore.WorkRequestSent) + "/" +
			requestStateLabel(zh, jobstore.WorkRequestAnswered) + "/" +
			requestStateLabel(zh, jobstore.WorkRequestFailed) + "/" +
			requestStateLabel(zh, jobstore.WorkRequestExpired),
			"待发送/已发送待回复/已回复/失败/未回应(已转整理)"},
		{"journal kinds", journalKindLabel(zh, jobstore.WorkJournalReport) + "/" +
			journalKindLabel(zh, jobstore.WorkJournalNote) + "/" +
			journalKindLabel(zh, jobstore.WorkJournalStatus) + "/" +
			journalKindLabel(zh, jobstore.WorkJournalSteward) + "/" +
			journalKindLabel(zh, jobstore.WorkJournalLink), "汇报/备注/状态/整理/关联"},
		{"unknown kind reads as itself", requestKindLabel(zh, "future_kind"), "future_kind"},
		{"today section", todaySection(zh, 2, 3), zhGolden(t, "today-pending.txt") + "\n"},
		{"today section, unknown count", todaySection(zh, 2, -1), zhGolden(t, "today-unknown.txt") + "\n"},
		{"memory section", memorySection(zh, 3, &MemoryHygiene{Findings: 10, Remaining: 2,
			Lines: []string{"proj · k1（rule）：a"}}), zhGolden(t, "memory.txt") + "\n"},
		{"question", wrapQuestion(zh, "还有什么没完成", "human:a"), zhGolden(t, "question.txt")},
		{"question, anonymous", wrapQuestion(zh, "还有什么没完成", ""), zhGolden(t, "question-anon.txt")},
		{"session title", stewardText(zh, "session.title", "agent", "claude-acp"), "管家 · claude-acp"},
		{"ready line", readyLine(zh), "读完以上内容后，只回复一行：管家已就绪（说明未结工作项数量）。不要调用任何工具。"},
		{"review labels", reviewTriggerLabel(zh, TriggerDaily) + "/" + reviewTriggerLabel(zh, TriggerManual) +
			"/" + reviewTriggerLabel(zh, TriggerEvent), "每日巡检/手动巡检/事件整理"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: zh text changed\n got: %q\nwant: %q", c.name, c.got, c.want)
		}
	}
}

// TestZhStewardPrimeIsUnchanged pins the whole first-turn prime of a steward with nothing
// to report: its section headings, the "now" line and the empty-section hints.
func TestZhStewardPrimeIsUnchanged(t *testing.T) {
	e := newEnv(t)
	e.svc.SetLanguageFn(func() i18n.Lang { return i18n.ZhCN })
	p, err := e.svc.BuildPrime(primeClock)
	if err != nil {
		t.Fatalf("BuildPrime: %v", err)
	}
	if want := zhGolden(t, "prime-empty.txt"); p.Text != want {
		t.Errorf("prime changed\n--- got ---\n%s\n--- want ---\n%s", p.Text, want)
	}
}

// TestZhStewardReviewPromptIsUnchanged pins a whole review prompt: the trigger heading, the
// item lines, the event lines and the instruction with the two code-owned steps.
func TestZhStewardReviewPromptIsUnchanged(t *testing.T) {
	e := newEnv(t)
	e.svc.SetLanguageFn(func() i18n.Lang { return i18n.ZhCN })
	events := []jobstore.StewardEvent{
		{ID: 1, Kind: jobstore.StewardEventDue, Detail: "工作项 W-1「修登录页」3 天后到期"},
		{ID: 2, Kind: jobstore.StewardEventSession, Detail: "工作项 W-2「整理线路」的会话 abcdef 已离线"},
	}
	mem := &MemoryHygiene{Findings: 4, Remaining: 2, Lines: []string{"proj · k1（rule）：a, b"}}
	got := e.svc.reviewPrompt(TriggerDaily, "2026-11-05", fixtureItems(), 2, events, true, primeClock, 3, mem)
	if want := zhGolden(t, "review-daily.txt"); got != want {
		t.Errorf("review prompt changed\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	empty := e.svc.reviewPrompt(TriggerManual, "2026-11-05", nil, 0, nil, false, primeClock, 0, nil)
	if want := zhGolden(t, "review-empty.txt"); empty != want {
		t.Errorf("review prompt without material changed\n--- got ---\n%s\n--- want ---\n%s", empty, want)
	}
	// The other triggers differ in the heading only.
	for trigger, heading := range map[string]string{
		TriggerManual: "## 手动巡检（2026-11-05）\n\n",
		TriggerEvent:  "## 事件整理（2026-11-05）\n\n",
	} {
		if p := e.svc.reviewPrompt(trigger, "2026-11-05", nil, 0, nil, false, primeClock, 0, nil); !strings.HasPrefix(p, heading) {
			t.Errorf("%s review heading: got %q, want prefix %q", trigger, firstLineOf(p), heading)
		}
	}
}

func firstLineOf(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// TestZhStewardEventTextsAreUnchanged pins the lines the steward's event inbox carries.
func TestZhStewardEventTextsAreUnchanged(t *testing.T) {
	e := newEnv(t)
	e.svc.SetLanguageFn(func() i18n.Lang { return i18n.ZhCN })
	w := e.item(t, "整理线路")
	e.svc.NoteDue(w, "3 天后到期", 0)
	events, err := e.st.PendingStewardEvents(10)
	if err != nil || len(events) != 1 {
		t.Fatalf("events: %v (%d)", err, len(events))
	}
	if want := "工作项 " + w.ID + "「整理线路」3 天后到期"; events[0].Detail != want {
		t.Errorf("event text changed\n got: %q\nwant: %q", events[0].Detail, want)
	}
	if got, want := stewardText(i18n.ZhCN, "event.drafts", "n", 3), "有 3 个未整理的草稿工作项"; got != want {
		t.Errorf("drafts event: got %q, want %q", got, want)
	}
	if got, want := stewardText(i18n.ZhCN, "event.session", "id", "W-1", "title", "t",
		"session", "abcdef", "state", stewardText(i18n.ZhCN, "event.state_offline")),
		"工作项 W-1「t」的会话 abcdef 已离线"; got != want {
		t.Errorf("session event: got %q, want %q", got, want)
	}
}

// enItems is the same shape as fixtureItems with English content: the scan below checks the
// text gofer writes, so the user's own words must not be the thing that fails it.
func enItems() []work.ItemView {
	items := fixtureItems()
	items[0].Title, items[0].BlockerText, items[0].NextStep = "fix the login page", "waiting for a quote", "prototype and accept"
	items[1].Title = "tidy the line"
	items[0].Requests = append(items[0].Requests,
		jobstore.WorkRequest{ID: "r-9", Kind: jobstore.WorkRequestSummarize, State: jobstore.WorkRequestSent})
	return items
}

// TestStewardTextFollowsTheLanguage: the ordinary English path — a session whose project
// reads as English renders every text of the prime, the review prompt and the event lines
// without Chinese.
func TestEnglishStewardTextHasNoCJK(t *testing.T) {
	e := newEnv(t)
	e.svc.SetLanguageFn(func() i18n.Lang { return i18n.EN })
	items := enItems()
	mem := &MemoryHygiene{Findings: 4, Remaining: 2, Lines: []string{"proj · k1 (rule): a, b"}}
	texts := map[string]string{
		"item line":     itemLine(i18n.EN, items[0], primeClock),
		"item line 2":   itemLine(i18n.EN, items[1], primeClock),
		"ago":           ago(i18n.EN, primeClock, primeClock.Add(-72*time.Hour).Unix()),
		"weekday":       weekday(i18n.EN, 3),
		"today":         todaySection(i18n.EN, 2, 3),
		"today (none)":  todaySection(i18n.EN, 2, -1),
		"memory":        memorySection(i18n.EN, 3, mem),
		"question":      wrapQuestion(i18n.EN, "what is left?", "human:ann"),
		"ready":         readyLine(i18n.EN),
		"session title": stewardText(i18n.EN, "session.title", "agent", "claude-acp"),
		"review": e.svc.reviewPrompt(TriggerDaily, "2026-11-05", items, 2,
			[]jobstore.StewardEvent{{ID: 1, Kind: jobstore.StewardEventDue, Detail: "work item W-1 is due"}},
			true, primeClock, 3, mem),
	}
	for name, text := range texts {
		if m := han.FindString(text); m != "" {
			t.Errorf("%s: English text carries CJK (%q): %q", name, m, text)
		}
	}
	// English keeps gofer's own layout: the id / status / title head, the " | " separators,
	// the ", " list separator and the reminder / last-activity fields (the title and the
	// blocker are the user's own words and stay as written).
	line := itemLine(i18n.EN, items[0], primeClock)
	for _, want := range []string{"- W-1 [needs me] ", " | blocked: ",
		" | in flight: report request(pending), tidy-up(sent, awaiting reply)"} {
		if !strings.Contains(line, want) {
			t.Errorf("English item line lacks %q:\n%s", want, line)
		}
	}
	if p, err := e.svc.BuildPrime(primeClock); err != nil {
		t.Errorf("BuildPrime (en): %v", err)
	} else if m := han.FindString(p.Text); m != "" {
		t.Errorf("English prime carries CJK (%q): %q", m, p.Text)
	}
}

// TestMigratedStewardFilesHaveNoCJK is the scan guard of design §7: a string literal added
// to a migrated file must not be written in Chinese. Comments are not scanned (they are
// not part of the text an agent reads) and neither are test files.
func TestMigratedStewardFilesHaveNoCJK(t *testing.T) {
	for _, file := range migratedFiles {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		fset := token.NewFileSet()
		af, err := parser.ParseFile(fset, file, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		ast.Inspect(af, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if m := han.FindString(lit.Value); m != "" {
				t.Errorf("%s:%d: string literal carries CJK (%q) — move the text into internal/i18n: %s",
					file, fset.Position(lit.Pos()).Line, m, lit.Value)
			}
			return true
		})
	}
}
