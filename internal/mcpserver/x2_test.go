package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
)

func TestStewardIssueReadToolsFilterTheMirror(t *testing.T) {
	call, meta, _ := newStewardSession(t)
	if err := meta.UpsertTrackerRepo(jobstore.TrackerRepo{TrackerID: "tr-a", ProjectKey: "proj-a", RelPath: "svc/a", Prefix: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := meta.UpsertTrackerRepo(jobstore.TrackerRepo{TrackerID: "tr-b", ProjectKey: "proj-b", RelPath: "svc/b", Prefix: "b"}); err != nil {
		t.Fatal(err)
	}
	put := func(tr, id, title, status string, tags ...string) {
		b, _ := json.Marshal(map[string]any{"id": id, "title": title, "status": status, "type": "task", "tags": tags, "description": "details of " + title})
		if err := meta.UpsertTrackerIssue(jobstore.TrackerRecord{TrackerID: tr, ID: id, Body: b, Rev: 1}); err != nil {
			t.Fatal(err)
		}
	}
	put("tr-a", "a-1", "导出按钮", "open", "ui")
	put("tr-a", "a-2", "登录失败", "closed", "bug")
	put("tr-b", "b-1", "导出接口", "open", "api")
	put("tr-a", "dup-1", "同号 A", "open")
	put("tr-b", "dup-1", "同号 B", "open")

	rows := func(args map[string]any) []any {
		isErr, out, text := call("gofer_issue_list", args)
		if isErr {
			t.Fatalf("issue_list %v: %s", args, text)
		}
		l, _ := out["issues"].([]any)
		return l
	}
	if n := len(rows(map[string]any{})); n != 5 {
		t.Fatalf("all issues = %d", n)
	}
	if n := len(rows(map[string]any{"project": "proj-a"})); n != 3 {
		t.Fatalf("proj-a = %d", n)
	}
	if n := len(rows(map[string]any{"status": "open", "query": "导出"})); n != 2 {
		t.Fatalf("open + 导出 = %d", n)
	}
	if n := len(rows(map[string]any{"tags": []string{"bug"}})); n != 1 {
		t.Fatalf("tag bug = %d", n)
	}
	if n := len(rows(map[string]any{"repo": "svc/b"})); n != 2 {
		t.Fatalf("repo svc/b = %d", n)
	}
	if n := len(rows(map[string]any{"limit": 2})); n != 2 {
		t.Fatalf("limit 2 = %d", n)
	}

	isErr, out, text := call("gofer_issue_get", map[string]any{"id": "a-2"})
	if isErr || out["tracker_id"] != "tr-a" {
		t.Fatalf("issue_get = %v %s", out, text)
	}
	if isErr, _, text := call("gofer_issue_get", map[string]any{"id": "dup-1"}); !isErr || !strings.Contains(text, "tracker_id") {
		t.Fatalf("an ambiguous id must ask for tracker_id: %v %s", isErr, text)
	}
	if isErr, _, _ := call("gofer_issue_get", map[string]any{"id": "dup-1", "tracker_id": "tr-b"}); isErr {
		t.Fatal("dup-1 with tracker_id must resolve")
	}
	if isErr, _, _ := call("gofer_issue_get", map[string]any{"id": "nope"}); !isErr {
		t.Fatal("unknown issue must be an error")
	}
}

func TestStewardSessionAskRefusesOfflineAndUnknownLoudly(t *testing.T) {
	call, meta, _ := newStewardSession(t)
	if _, err := meta.UpsertAgentSession(jobstore.AgentSession{SessionID: "sess-ask-off1", Agent: "claude", ProjectKey: "self", Cwd: "/ws", State: jobstore.SessionOffline}); err != nil {
		t.Fatal(err)
	}
	w, _ := meta.CreateWorkItem(jobstore.WorkItemInput{Title: "等设备", SessionIDs: []string{"sess-ask-off1"}})

	isErr, _, text := call("gofer_session_ask", map[string]any{"id": "sess-ask-off1", "text": "资源到了"})
	if !isErr || !strings.Contains(text, "not online") {
		t.Fatalf("offline session: isErr=%v %q", isErr, text)
	}
	if isErr, _, text := call("gofer_session_ask", map[string]any{"id": "sess-nope", "text": "hi"}); !isErr || !strings.Contains(text, "not found") {
		t.Fatalf("unknown session: isErr=%v %q", isErr, text)
	}
	if isErr, _, _ := call("gofer_session_ask", map[string]any{"id": "sess-ask-off1", "text": "  "}); !isErr {
		t.Fatal("empty text must be refused")
	}
	// Nothing was delivered, so nothing is logged on the work item.
	js, _ := meta.ListWorkJournal(w.ID, 50, 0)
	for _, j := range js {
		if strings.Contains(j.Text, "带话") {
			t.Fatalf("a refused ask was journaled: %q", j.Text)
		}
	}
}
