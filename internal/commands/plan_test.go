package commands

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
)

// TestPlanSetTodoAppendNote: `plan set-todo <id> --append-note "<line>"` PATCHes
// the todo with the appended line and NO status (appending must not flip a todo's
// state — it is how a job's outcome is recorded on the checklist, SUP-01 C), and
// --note with --append-note is refused before any request is sent.
func TestPlanSetTodoAppendNote(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""

	var gotPath, gotMethod string
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode todo body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(client.Todo{TodoID: "todo-1", Status: "doing"})
	}))
	defer ts.Close()

	app := NewApp("test")
	if code := app.Run([]string{"plan", "set-todo", "--server", ts.URL, "todo-1", "--append-note", "job-1 ✓ no commits"}); code != 0 {
		t.Fatalf("app.Run exit code=%d", code)
	}
	if gotMethod != http.MethodPatch || gotPath != "/v1/todos/todo-1" {
		t.Fatalf("unexpected request: %s %s", gotMethod, gotPath)
	}
	if gotBody["append_note"] != "job-1 ✓ no commits" {
		t.Fatalf("append_note = %v, want the line", gotBody["append_note"])
	}
	if status, ok := gotBody["status"]; ok && status != "" {
		t.Fatalf("append must not carry a status, got %v", status)
	}

	// The two note forms are mutually exclusive: refuse without a request.
	gotPath, gotBody = "", nil
	if code := app.Run([]string{"plan", "set-todo", "--server", ts.URL, "todo-1", "--note", "x", "--append-note", "y"}); code == 0 {
		t.Fatal("--note with --append-note must fail")
	}
	if gotPath != "" {
		t.Fatalf("a rejected update must not reach the server: %s", gotPath)
	}
}

// TestPlanShowTodoDispatchFields (PLAN-02 P2): `plan show` is where a human reads a
// plan item's state, so the two facts the dispatch added — WHO runs it and WHY the last
// attempt started nothing — must be on that screen, along with the ready box.
func TestPlanShowTodoDispatchFields(t *testing.T) {
	out := captureOutput(t, func() {
		printPlanTodos(gcli.NewCommand("show", "", nil), []client.Todo{
			{
				TodoID: "todo-ready", Title: "queued for an agent", Status: "ready",
				Assignee: "omp", Note: "waiting",
			},
			{
				TodoID: "todo-failed", Title: "refused", Status: "ready", Assignee: "ghost",
				DispatchError: "invalid request: unknown agent \"ghost\"",
			},
		})
	})
	for _, want := range []string{
		"[>]", "todo-ready", "(assignee=omp)",
		"todo-failed", "dispatch_error: invalid request: unknown agent",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("plan show output missing %q:\n%s", want, out)
		}
	}
}

// TestPlanShowUsageLine (PLAN-02 P2): the plan header keeps the usage roll-up on one
// line — totals plus who did the work. An older server (or a plan with no jobs) sends
// nothing, and the line must then simply not appear.
func TestPlanShowUsageLine(t *testing.T) {
	if got := formatPlanUsage(nil); got != "" {
		t.Fatalf("no usage should render nothing, got %q", got)
	}
	if got := formatPlanUsage(&client.PlanUsage{}); got != "" {
		t.Fatalf("a job-less plan should render nothing, got %q", got)
	}
	u := &client.PlanUsage{
		Jobs: 7, TotalTokens: 1_234_567, CostUSD: 3.45,
		ByAgent: map[string]client.UsageAgent{
			"omp":   {Jobs: 5, TotalTokens: 1_200_000, CostUSD: 3.4},
			"codex": {Jobs: 2, TotalTokens: 34_567, CostUSD: 0.05},
		},
	}
	got := formatPlanUsage(u)
	for _, want := range []string{"total 1.2M tokens", "$3.45", "omp 5 jobs", "codex 2 jobs"} {
		if !strings.Contains(got, want) {
			t.Fatalf("usage line %q missing %q", got, want)
		}
	}
	// Most jobs first: the reader wants to see who is carrying the plan.
	if strings.Index(got, "omp 5 jobs") > strings.Index(got, "codex 2 jobs") {
		t.Fatalf("usage line %q should list the busier agent first", got)
	}
	// A plan whose jobs reported no numbers still states that it ran them.
	if got := formatPlanUsage(&client.PlanUsage{Jobs: 2, ByAgent: map[string]client.UsageAgent{"omp": {Jobs: 2}}}); !strings.Contains(got, "omp 2 jobs") {
		t.Fatalf("usage line %q must still name the agents", got)
	}
}

// TestPlanSubcommandsRegistered: the plan group keeps its subcommands (plus the PLAN-02
// `dispatch`), and the legacy aliases still resolve.
func TestPlanSubcommandsRegistered(t *testing.T) {
	app := NewApp("test")

	planCmd := app.GetCommand("plan")
	if planCmd == nil {
		t.Fatal("plan command not registered")
	}
	for _, sub := range []string{"create", "list", "show", "attach", "set-status", "archive", "add-todo", "set-todo", "dispatch", "ask", "decisions", "answer"} {
		if planCmd.GetCommand(sub) == nil {
			t.Fatalf("plan subcommand %q not registered", sub)
		}
	}
	if !planCmd.IsAlias("ls") || planCmd.ResolveAlias("ls") != "list" {
		t.Fatalf("`ls` should be an alias for plan list, got %q", planCmd.ResolveAlias("ls"))
	}
	if !planCmd.IsAlias("todo-add") || planCmd.ResolveAlias("todo-add") != "add-todo" {
		t.Fatalf("`todo-add` should be an alias for plan add-todo, got %q", planCmd.ResolveAlias("todo-add"))
	}
	if !planCmd.IsAlias("todo-done") || planCmd.ResolveAlias("todo-done") != "set-todo" {
		t.Fatalf("`todo-done` should be an alias for plan set-todo, got %q", planCmd.ResolveAlias("todo-done"))
	}
}

func TestPrintPlanTodos(t *testing.T) {
	out := captureOutput(t, func() {
		printPlanTodos(gcli.NewCommand("show", "", nil), []client.Todo{
			{TodoID: "todo-1", Title: "done todo", Done: true, JobID: "job-1"},
			{TodoID: "todo-2", Title: "plain todo"},
		})
	})
	for _, want := range []string{"todos:", "[x]", "todo-1", "done todo", "(job=job-1)", "[ ]", "todo-2", "plain todo"} {
		if !strings.Contains(out, want) {
			t.Fatalf("printPlanTodos output missing %q:\n%s", want, out)
		}
	}
}

// TestPlanListPagingFlags pins `plan list`'s paging surface (F-d): --limit/--project/--q
// travel as query params (limit defaulting to 20), the footer names how many plans
// matched and which slice is shown, and --all pages through the server's page cap
// instead of silently truncating the list.
func TestPlanListPagingFlags(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""
	resetPlanListOpts := func() {
		planListOpts.status, planListOpts.project, planListOpts.q = "", "", ""
		planListOpts.limit, planListOpts.all = 0, false
	}
	resetPlanListOpts()
	t.Cleanup(resetPlanListOpts)

	// The stub holds 3 plans but serves at most 2 rows per response — a server whose cap
	// is lower than the client's page request, which is what --all has to walk.
	var queries []url.Values
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		queries = append(queries, q)
		offset, _ := strconv.Atoi(q.Get("offset"))
		limit, _ := strconv.Atoi(q.Get("limit"))
		if limit <= 0 {
			limit = 20
		}
		if limit > 2 {
			limit = 2
		}
		plans := []map[string]any{}
		for i := offset; i < 3 && len(plans) < limit; i++ {
			plans = append(plans, map[string]any{
				"plan_id": fmt.Sprintf("plan-%d", i+1), "status": "open", "title": "t",
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"plans": plans, "total": 3, "limit": limit, "offset": offset,
		})
	}))
	defer ts.Close()

	run := func(args ...string) string {
		t.Helper()
		resetPlanListOpts()
		var code int
		out := captureOutput(t, func() {
			code = NewApp("test").Run(append([]string{"plan", "list", "--server", ts.URL}, args...))
		})
		if code != 0 {
			t.Fatalf("plan list %v exit code=%d, out=%s", args, code, out)
		}
		return out
	}

	out := run("--limit", "1", "--project", "self", "--q", "plan-1")
	if len(queries) != 1 {
		t.Fatalf("plan list --limit 1 sent %d requests, want 1", len(queries))
	}
	got := queries[0]
	for _, want := range [][2]string{{"limit", "1"}, {"project", "self"}, {"q", "plan-1"}} {
		if got.Get(want[0]) != want[1] {
			t.Fatalf("query %s = %q, want %q (query=%v)", want[0], got.Get(want[0]), want[1], got)
		}
	}
	// An unset offset is simply absent (the server reads it as 0).
	if off := got.Get("offset"); off != "" && off != "0" {
		t.Fatalf("query offset = %q, want 0 or absent (query=%v)", off, got)
	}
	if !strings.Contains(out, "共 3 条，显示 1–1") {
		t.Fatalf("footer must state the total and the shown slice, got:\n%s", out)
	}

	// No --limit: the CLI's own default page size (20), not the server's.
	queries = nil
	run()
	if len(queries) != 1 || queries[0].Get("limit") != "20" {
		t.Fatalf("default page size = %v, want limit=20", queries)
	}

	// --all walks every page of the same filter.
	queries = nil
	out = run("--all")
	if len(queries) != 2 {
		t.Fatalf("--all sent %d requests, want 2 (3 plans, 2-row pages): %v", len(queries), queries)
	}
	first, second := queries[0].Get("offset"), queries[1].Get("offset")
	if first != "" && first != "0" {
		t.Fatalf("--all first offset = %q, want 0", first)
	}
	if second != "2" {
		t.Fatalf("--all second offset = %q, want 2 (page 1 held 2 rows)", second)
	}
	for _, p := range []string{"plan-1", "plan-2", "plan-3"} {
		if !strings.Contains(out, p) {
			t.Fatalf("--all must print every plan, %s missing:\n%s", p, out)
		}
	}
	if !strings.Contains(out, "共 3 条，显示 1–3") {
		t.Fatalf("--all footer = %s", out)
	}
}
