package mcpserver

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/httpapi"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/work"
)

func callTool(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s transport: %v", name, err)
	}
	return res
}

// exerciseWorkTools drives the whole W1 tool surface through one MCP session; the same
// script runs against the local backend and the client backend, so they stay identical.
func exerciseWorkTools(t *testing.T, s *mcp.ClientSession, meta *jobstore.Store, otherProjectItem string) {
	t.Helper()

	var list workListOutput
	structured(t, callTool(t, s, "gofer_work_list", map[string]any{}), &list)
	if len(list.Items) < 2 || list.Summary.Open < 2 {
		t.Fatalf("list = %+v", list)
	}
	var mine *work.ItemView
	for i := range list.Items {
		if list.Items[i].Title == "mcp item" {
			mine = &list.Items[i]
		}
	}
	if mine == nil || len(mine.SessionIDs) != 1 {
		t.Fatalf("seeded item missing from list: %+v", list.Items)
	}

	var d work.DetailView
	structured(t, callTool(t, s, "gofer_work_get", map[string]any{"id": mine.ID}), &d)
	if d.Title != "mcp item" || len(d.Sessions) != 1 || len(d.Journal) == 0 {
		t.Fatalf("get = %+v", d)
	}

	// update: fields only, rev-checked.
	structured(t, callTool(t, s, "gofer_work_update", map[string]any{"id": mine.ID, "rev": d.Rev, "goal": "做完导出", "next_step": "写测试"}), &d)
	if d.Goal != "做完导出" || d.NextStep != "写测试" || d.Status != "active" {
		t.Fatalf("update = %+v", d)
	}
	stale := callTool(t, s, "gofer_work_update", map[string]any{"id": mine.ID, "rev": 1, "goal": "x"})
	if !stale.IsError {
		t.Fatalf("a stale rev must be an error result: %+v", stale)
	}

	// note + report (status under the human-priority rule is covered in the work package).
	structured(t, callTool(t, s, "gofer_work_note", map[string]any{"id": mine.ID, "text": "from mcp"}), &map[string]string{})
	// WORK-06: an optional level is kept; a bad one is refused.
	structured(t, callTool(t, s, "gofer_work_note", map[string]any{"id": mine.ID, "text": "mcp milestone", "level": "milestone"}), &map[string]string{})
	if bad := callTool(t, s, "gofer_work_note", map[string]any{"id": mine.ID, "text": "x", "level": "loud"}); !bad.IsError {
		t.Fatalf("an invalid level must be an error result: %+v", bad)
	}
	structured(t, callTool(t, s, "gofer_work_report", map[string]any{"id": mine.ID, "blocker": "缺账号", "status": "waiting_resource", "session_id": "sess-mcp-1"}), &d)
	if d.Status != "waiting_resource" || d.BlockerText != "缺账号" {
		t.Fatalf("report = %+v", d)
	}
	last := d.Journal[len(d.Journal)-1]
	if last.Kind != "report" || last.By != "session:sess-mcp-1" {
		t.Fatalf("report journal = %+v", last)
	}
	sawNote := false
	for _, e := range d.Journal {
		if e.Kind == "note" && e.Text == "from mcp" {
			sawNote = true
		}
		if e.Text == "mcp milestone" && e.Level != "milestone" {
			t.Fatalf("level not kept: %+v", e)
		}
	}
	if !sawNote {
		t.Fatalf("note missing: %+v", d.Journal)
	}

	// the update tool has no status knob
	res := callTool(t, s, "gofer_work_update", map[string]any{"id": mine.ID, "status": "done"})
	var after work.DetailView
	structured(t, callTool(t, s, "gofer_work_get", map[string]any{"id": mine.ID}), &after)
	if after.Status == "done" {
		t.Fatalf("gofer_work_update must not set the status (res=%+v)", res)
	}

	// read-only sessions
	var sl sessionListOutput
	structured(t, callTool(t, s, "gofer_session_list", map[string]any{"include_ended": true}), &sl)
	if len(sl.Sessions) != 1 || sl.Sessions[0].SessionID != "sess-mcp-0001" || sl.Sessions[0].Agent != "claude" {
		t.Fatalf("session list = %+v", sl)
	}
	var sv sessionToolView
	structured(t, callTool(t, s, "gofer_session_get", map[string]any{"id": "sess-mcp-0001"}), &sv)
	if sv.Cwd != "/ws/a" {
		t.Fatalf("session get = %+v", sv)
	}
	if res := callTool(t, s, "gofer_session_get", map[string]any{"id": "nope"}); !res.IsError {
		t.Fatal("unknown session must be an error")
	}
	// not-found work item
	if res := callTool(t, s, "gofer_work_get", map[string]any{"id": "w-missing"}); !res.IsError {
		t.Fatal("unknown work item must be an error")
	}
	_ = otherProjectItem
}

func seedWork(t *testing.T, meta *jobstore.Store) (mine, other jobstore.WorkItem) {
	t.Helper()
	if _, err := meta.UpsertAgentSession(jobstore.AgentSession{SessionID: "sess-mcp-0001", Agent: "claude", ProjectKey: "self", Cwd: "/ws/a"}); err != nil {
		t.Fatal(err)
	}
	var err error
	mine, err = meta.CreateWorkItem(jobstore.WorkItemInput{Title: "mcp item", ProjectKey: "self", SessionIDs: []string{"sess-mcp-0001"}})
	if err != nil {
		t.Fatal(err)
	}
	other, err = meta.CreateWorkItem(jobstore.WorkItemInput{Title: "foreign item", ProjectKey: "elsewhere"})
	if err != nil {
		t.Fatal(err)
	}
	return mine, other
}

func TestWorkToolsLocalBackend(t *testing.T) {
	jobs, projects, agents, pres := testCore(t)
	seedWork(t, jobs.Meta())
	s := connectTo(t, newServer(newLocalBackend(jobs, projects, agents, pres), "", "", ""))
	exerciseWorkTools(t, s, jobs.Meta(), "")
}

func TestWorkToolsClientBackend(t *testing.T) {
	jobs, projects, agents, _ := testCore(t)
	seedWork(t, jobs.Meta())
	srv := httptest.NewServer(httpapi.New(&config.ServerConfig{AllowEmptyToken: true}, "", true, jobs, nil, projects, agents, nil, nil, nil, nil).Handler())
	t.Cleanup(srv.Close)
	s := connectTo(t, newServer(NewClientBackend(client.New(srv.URL, "")), "", "", ""))
	exerciseWorkTools(t, s, jobs.Meta(), "")
}

func TestWorkToolsRegisteredButNotForLeaders(t *testing.T) {
	names := func(leader bool) map[string]bool {
		jobs, projects, agents, pres := testCore(t)
		if leader {
			t.Setenv(envLeaderPlan, "plan-x")
		}
		s := connectTo(t, newServer(newLocalBackend(jobs, projects, agents, pres), "", "", ""))
		res, err := s.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]bool{}
		for _, tl := range res.Tools {
			m[tl.Name] = true
		}
		return m
	}
	want := []string{"gofer_work_list", "gofer_work_get", "gofer_work_update", "gofer_work_note", "gofer_work_report", "gofer_session_list", "gofer_session_get"}
	op := names(false)
	for _, n := range want {
		if !op[n] {
			t.Fatalf("operator MCP lacks %s", n)
		}
	}
	for n := range names(true) {
		if strings.HasPrefix(n, "gofer_work_") || strings.HasPrefix(n, "gofer_session_") {
			t.Fatalf("leader whitelist must not include %s", n)
		}
	}
}

func TestWorkToolsProjectScope(t *testing.T) {
	jobs, projects, agents, pres := testCore(t)
	mine, other := seedWork(t, jobs.Meta())
	s := connectTo(t, newServer(newLocalBackend(jobs, projects, agents, pres), "", "", "self"))

	var list workListOutput
	structured(t, callTool(t, s, "gofer_work_list", map[string]any{}), &list)
	if len(list.Items) != 1 || list.Items[0].ID != mine.ID {
		t.Fatalf("scoped list must only show the scoped project: %+v", list.Items)
	}
	if res := callTool(t, s, "gofer_work_list", map[string]any{"project": "elsewhere"}); !res.IsError {
		t.Fatal("listing another project must fail under a scope")
	}
	for _, call := range []struct {
		tool string
		args map[string]any
	}{
		{"gofer_work_get", map[string]any{"id": other.ID}},
		{"gofer_work_update", map[string]any{"id": other.ID, "goal": "x"}},
		{"gofer_work_note", map[string]any{"id": other.ID, "text": "x"}},
		{"gofer_work_report", map[string]any{"id": other.ID, "goal": "x"}},
	} {
		if res := callTool(t, s, call.tool, call.args); !res.IsError {
			t.Fatalf("%s on another project's item must fail under a scope", call.tool)
		}
	}
	if res := callTool(t, s, "gofer_work_update", map[string]any{"id": mine.ID, "project_key": "elsewhere"}); !res.IsError {
		t.Fatal("moving an item out of the scoped project must fail")
	}
	// Reads and writes on its own item work.
	if res := callTool(t, s, "gofer_work_note", map[string]any{"id": mine.ID, "text": "ok"}); res.IsError {
		t.Fatalf("scoped note on own item failed: %+v", res.Content)
	}
}

func TestWorkRequestToolsLocalBackendReadsButNeedsServerToAct(t *testing.T) {
	jobs, projects, agents, pres := testCore(t)
	mine, _ := seedWork(t, jobs.Meta())
	s := connectTo(t, newServer(newLocalBackend(jobs, projects, agents, pres), "", "", ""))

	var out workRequestsOutput
	structured(t, callTool(t, s, "gofer_work_requests", map[string]any{"id": mine.ID}), &out)
	if len(out.Requests) != 0 {
		t.Fatalf("requests = %+v", out)
	}
	if res := callTool(t, s, "gofer_work_request_report", map[string]any{"id": mine.ID}); !res.IsError {
		t.Fatal("request_report needs a running server")
	}
	if res := callTool(t, s, "gofer_work_summarize", map[string]any{"id": mine.ID}); !res.IsError {
		t.Fatal("summarize needs a running server")
	}
	if res := callTool(t, s, "gofer_work_requests", map[string]any{"id": "w-missing"}); !res.IsError {
		t.Fatal("unknown item must be an error")
	}
}

func TestWorkRequestToolsClientBackendUseTheLedger(t *testing.T) {
	jobs, projects, agents, _ := testCore(t)
	mine, _ := seedWork(t, jobs.Meta())
	srv := httptest.NewServer(httpapi.New(&config.ServerConfig{AllowEmptyToken: true}, "", true, jobs, nil, projects, agents, nil, nil, nil, nil).Handler())
	t.Cleanup(srv.Close)
	s := connectTo(t, newServer(NewClientBackend(client.New(srv.URL, "")), "", "", ""))

	// The session cannot be messaged (no peer address) and the test server has no
	// summarizer agent: the ask is recorded, turned into a tidy-up and honestly failed.
	var rr workRequestReportOutput
	structured(t, callTool(t, s, "gofer_work_request_report", map[string]any{"id": mine.ID, "kind": "handoff"}), &rr)
	if len(rr.Results) != 1 || rr.Results[0].Kind != jobstore.WorkRequestSummarize || rr.Results[0].Sent {
		t.Fatalf("request_report = %+v", rr)
	}
	var out workRequestsOutput
	structured(t, callTool(t, s, "gofer_work_requests", map[string]any{"id": mine.ID}), &out)
	if len(out.Requests) < 1 {
		t.Fatalf("ledger after the ask = %+v", out)
	}
	if res := callTool(t, s, "gofer_work_summarize", map[string]any{"id": mine.ID}); !res.IsError {
		t.Fatal("no summarizer agent in the test server: summarize must report why")
	}
}

func TestWorkRequestToolsProjectScope(t *testing.T) {
	jobs, projects, agents, pres := testCore(t)
	mine, other := seedWork(t, jobs.Meta())
	s := connectTo(t, newServer(newLocalBackend(jobs, projects, agents, pres), "", "", "self"))
	if res := callTool(t, s, "gofer_work_requests", map[string]any{}); !res.IsError {
		t.Fatal("a scoped MCP must name an item")
	}
	if res := callTool(t, s, "gofer_work_requests", map[string]any{"id": other.ID}); !res.IsError {
		t.Fatal("another project's ledger must be refused")
	}
	if res := callTool(t, s, "gofer_work_requests", map[string]any{"id": mine.ID}); res.IsError {
		t.Fatalf("own ledger failed: %+v", res.Content)
	}
}
