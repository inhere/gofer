package mcpserver

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/httpapi"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/work"
)

// stewardToolNames is the WHITELIST the steward's gofer MCP exposes (W2b). Deliberately
// absent: run_job, cancel, reject, every config / plan / presence tool, accept, merge,
// delete — and anything that could end a work item.
var stewardToolNames = []string{
	"gofer_get_job",
	"gofer_issue_get",
	"gofer_issue_list",
	"gofer_list_jobs",
	"gofer_session_ask",
	"gofer_session_get",
	"gofer_session_list",
	"gofer_session_tail",
	"gofer_steward_notes",
	"gofer_work_get",
	"gofer_work_list",
	"gofer_work_merge_suggest",
	"gofer_work_note",
	"gofer_work_remind",
	"gofer_work_request_report",
	"gofer_work_requests",
	"gofer_work_summarize",
	"gofer_work_update",
}

func newStewardSession(t *testing.T) (call func(name string, args map[string]any) (isErr bool, out map[string]any, text string), meta *jobstore.Store, cli *client.Client) {
	t.Helper()
	t.Setenv(envSteward, "1")
	t.Setenv(envJobID, "steward-job")
	jobs, projects, agents, _ := testCore(t)
	meta = jobs.Meta()
	const tok = "gjt_steward-job_00112233445566778899aabbccddeeff"
	sum := sha256.Sum256([]byte(tok))
	if err := meta.UpsertJobToken(jobstore.JobTokenRecord{JobID: "steward-job", TokenHash: hex.EncodeToString(sum[:]),
		Kind: jobstore.JobCredentialSteward, ExpiresAt: time.Now().Unix() + 3600, CreatedAt: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
	hs := httpapi.New(&config.ServerConfig{Token: "op-secret"}, "op-secret", false, jobs, nil, projects, agents, nil, nil, nil, nil)
	hs.SetTrackerStore(meta)
	srv := httptest.NewServer(hs.Handler())
	t.Cleanup(srv.Close)
	cli = client.New(srv.URL, tok)
	session := connectTo(t, newServer(NewClientBackend(cli), "", "", ""))
	call = func(name string, args map[string]any) (bool, map[string]any, string) {
		res := callTool(t, session, name, args)
		var text strings.Builder
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				text.WriteString(tc.Text)
			}
		}
		out, _ := res.StructuredContent.(map[string]any)
		return res.IsError, out, text.String()
	}
	return call, meta, cli
}

func TestStewardToolsWhitelist(t *testing.T) {
	t.Setenv(envSteward, "1")
	jobs, projects, agents, pres := testCore(t)
	session := connectTo(t, NewLocal(jobs, projects, agents, pres))
	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tl := range tools.Tools {
		got = append(got, tl.Name)
	}
	sort.Strings(got)
	want := append([]string(nil), stewardToolNames...)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("steward tools = %v\nwant exactly  %v", got, want)
	}
	for _, banned := range []string{"gofer_run_job", "gofer_cancel_job", "gofer_reject_job", "gofer_create_plan", "gofer_register", "gofer_job_say", "gofer_job_end",
		"gofer_work_report", "gofer_answer_interaction", "gofer_tail_log"} {
		for _, g := range got {
			if g == banned {
				t.Fatalf("the steward must not have %s", banned)
			}
		}
	}
}

func TestStewardToolsDriveTheWorkSurfaceThroughItsCredential(t *testing.T) {
	call, meta, cli := newStewardSession(t)
	a, _ := meta.CreateWorkItem(jobstore.WorkItemInput{Title: "登录修复", ProjectKey: "self"})
	b, _ := meta.CreateWorkItem(jobstore.WorkItemInput{Title: "登录问题", ProjectKey: "self"})
	if _, err := meta.UpsertAgentSession(jobstore.AgentSession{SessionID: "sess-stw-0001", Agent: "claude", ProjectKey: "self", Cwd: "/ws"}); err != nil {
		t.Fatal(err)
	}

	// Reads.
	if isErr, out, text := call("gofer_work_list", map[string]any{}); isErr {
		t.Fatalf("work_list: %s", text)
	} else if items, _ := out["items"].([]any); len(items) != 2 {
		t.Fatalf("work_list = %v", out)
	}
	if isErr, _, text := call("gofer_work_get", map[string]any{"id": a.ID}); isErr {
		t.Fatalf("work_get: %s", text)
	}
	if isErr, _, text := call("gofer_session_list", map[string]any{}); isErr {
		t.Fatalf("session_list: %s", text)
	}
	if isErr, _, text := call("gofer_session_get", map[string]any{"id": "sess-stw-0001"}); isErr {
		t.Fatalf("session_get: %s", text)
	}
	if isErr, _, text := call("gofer_list_jobs", map[string]any{"limit": 5}); isErr {
		t.Fatalf("list_jobs: %s", text)
	}
	if isErr, _, text := call("gofer_work_requests", map[string]any{"id": a.ID}); isErr {
		t.Fatalf("work_requests: %s", text)
	}

	// Writes the steward may do.
	if isErr, _, text := call("gofer_work_update", map[string]any{"id": a.ID, "goal": "登录要能用", "status": "waiting_resource"}); isErr {
		t.Fatalf("work_update: %s", text)
	}
	cur, _, _ := meta.GetWorkItem(a.ID)
	if cur.Goal != "登录要能用" || cur.Status != "waiting_resource" || cur.StatusSource != jobstore.WorkSourceReport {
		t.Fatalf("update not applied as the steward's report: %+v", cur)
	}
	if isErr, _, text := call("gofer_work_note", map[string]any{"id": a.ID, "text": "管家备注"}); isErr {
		t.Fatalf("work_note: %s", text)
	}
	j, _ := meta.ListWorkJournal(a.ID, 20, 0)
	sawNote := false
	for _, e := range j {
		if e.Kind == jobstore.WorkJournalSteward && e.Text == "管家备注" {
			sawNote = true
		}
	}
	if !sawNote {
		t.Fatalf("the steward's note must be a steward journal line: %+v", j)
	}

	// remind: set then clear.
	if isErr, _, text := call("gofer_work_remind", map[string]any{"id": a.ID, "after_sec": 3600}); isErr {
		t.Fatalf("work_remind: %s", text)
	}
	cur, _, _ = meta.GetWorkItem(a.ID)
	if cur.RemindAt < time.Now().Unix()+3000 {
		t.Fatalf("reminder not set: %+v", cur)
	}
	if isErr, _, text := call("gofer_work_remind", map[string]any{"id": a.ID, "clear": true}); isErr {
		t.Fatalf("work_remind clear: %s", text)
	}
	cur, _, _ = meta.GetWorkItem(a.ID)
	if cur.RemindAt != 0 {
		t.Fatalf("reminder not cleared: %+v", cur)
	}
	for _, bad := range []map[string]any{{"id": a.ID}, {"id": a.ID, "clear": true, "at": 5}, {"id": a.ID, "at": 5, "after_sec": 5}} {
		if isErr, _, _ := call("gofer_work_remind", bad); !isErr {
			t.Fatalf("remind %v must be refused", bad)
		}
	}

	// merge suggestion: recorded, nothing merged.
	if isErr, _, text := call("gofer_work_merge_suggest", map[string]any{"target_id": a.ID, "source_id": b.ID, "reason": "同一件事"}); isErr {
		t.Fatalf("merge_suggest: %s", text)
	}
	if bb, _, _ := meta.GetWorkItem(b.ID); bb.MergedInto != "" {
		t.Fatal("a suggestion merged")
	}
	sugs, _ := meta.ListWorkMergeSuggestions("")
	if len(sugs) != 1 || sugs[0].By != "steward" && !strings.HasPrefix(sugs[0].By, "steward") {
		t.Fatalf("suggestions = %+v", sugs)
	}

	// Refused by the tool before reaching the server.
	if isErr, _, text := call("gofer_work_update", map[string]any{"id": a.ID, "status": "done"}); !isErr || !strings.Contains(text, "person's call") {
		t.Fatalf("done must be refused with the reason, got err=%v %q", isErr, text)
	}
	if isErr, _, _ := call("gofer_work_update", map[string]any{"id": a.ID, "status": "dropped"}); !isErr {
		t.Fatal("dropped must be refused")
	}
	if cur, _, _ := meta.GetWorkItem(a.ID); cur.Status == "done" || cur.Status == "dropped" {
		t.Fatalf("the steward ended an item: %+v", cur)
	}

	// Refused by the SERVER's credential check even when the tool layer is bypassed: the
	// same credential through the plain client cannot do what the whitelist forbids.
	if _, err := cli.CreateWorkItem(map[string]any{"title": "x"}); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("creating a work item with the steward credential = %v, want a 403", err)
	}
	if _, err := cli.PatchWorkItem(a.ID, map[string]any{"status": "done"}); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("done through the raw client = %v, want a 403", err)
	}
	if _, err := cli.StewardAsk("hi"); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("steward ask with the steward credential = %v, want a 403", err)
	}
}

func TestStewardNotesToolVersionsAndConflicts(t *testing.T) {
	call, _, cli := newStewardSession(t)
	if isErr, _, text := call("gofer_steward_notes", map[string]any{"action": "set", "text": "# 笔记 v1", "version": 0}); isErr {
		t.Fatalf("first set: %s", text)
	}
	// A stale writer is told to get + merge.
	isErr, _, text := call("gofer_steward_notes", map[string]any{"action": "set", "text": "过期的写入", "version": 0})
	if !isErr || !strings.Contains(text, "call get") {
		t.Fatalf("stale set = err %v %q", isErr, text)
	}
	if isErr, _, text := call("gofer_steward_notes", map[string]any{"action": "set", "text": "# 笔记 v2", "version": 1}); isErr {
		t.Fatalf("second set: %s", text)
	}
	_, out, _ := call("gofer_steward_notes", map[string]any{})
	if v, _ := out["version"].(float64); v != 2 || out["body"] != "# 笔记 v2" {
		t.Fatalf("latest = %v", out)
	}
	_, out, _ = call("gofer_steward_notes", map[string]any{"action": "get", "version": 1})
	if out["body"] != "# 笔记 v1" {
		t.Fatalf("v1 = %v", out)
	}
	_, out, _ = call("gofer_steward_notes", map[string]any{"action": "history"})
	hist, _ := out["history"].([]any)
	if len(hist) != 2 {
		t.Fatalf("history = %v", out)
	}
	if isErr, _, _ := call("gofer_steward_notes", map[string]any{"action": "set", "text": "", "version": 2}); !isErr {
		t.Fatal("an empty set must be refused")
	}
	if isErr, _, _ := call("gofer_steward_notes", map[string]any{"action": "nope"}); !isErr {
		t.Fatal("unknown action must be refused")
	}
	// review_summary needs a review to attach to: without one it is a clean error, not a 403.
	isErr, _, text = call("gofer_steward_notes", map[string]any{"action": "review_summary", "text": "点评"})
	if !isErr || strings.Contains(text, "403") {
		t.Fatalf("review_summary without a review = err %v %q", isErr, text)
	}
	_ = cli
}

func TestStewardSessionTailAndUnavailableLocalBackend(t *testing.T) {
	call, meta, _ := newStewardSession(t)
	if _, err := meta.UpsertAgentSession(jobstore.AgentSession{SessionID: "sess-tail-9", Agent: "claude", ProjectKey: "self", Cwd: "/ws"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := meta.TouchAgentSession("sess-tail-9", jobstore.SessionHeartbeat{State: jobstore.SessionIdle, LastMessage: "做完了导出"}); err != nil {
		t.Fatal(err)
	}
	isErr, out, text := call("gofer_session_tail", map[string]any{"id": "sess-tail-9"})
	if isErr {
		t.Fatalf("session_tail: %s", text)
	}
	if !strings.Contains(out["text"].(string), "做完了导出") || out["source"] != "fallback" {
		t.Fatalf("tail = %v", out)
	}
	if isErr, _, _ := call("gofer_session_tail", map[string]any{"id": "missing"}); !isErr {
		t.Fatal("unknown session must be an error")
	}
	_ = work.DefaultSessionTailBytes
}
