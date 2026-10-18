package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/config"
)

// Caller-scope ceilings at the HTTP layer (design 2026-10-18, plan S4b): the three-state
// decision per resource kind, the resolver table, the handler-side BodyProject checks, the
// reverse matrix over every route, and the read-only matrix.

// kindParam is the path parameter that carries each Resource kind's id.
var kindParam = map[string]string{
	"job": "id", "plan": "id", "todo": "todo_id", "schedule": "id", "workflow": "id",
	"session": "sid", "work_item": "id", "tracker_repo": "tracker_id", "worker": "id",
	"xfer": "id", "wakeup": "wid", "retry": "rid", "decision": "id", "nudge": "id",
	"workbench_thread": "id", "merge_suggestion": "n", "memory_candidate": "n",
	"memory_suggestion": "n", "issue": "id",
}

// kindID is the id of the world's record of `kind`.
func (w ceilWorld) kindID(kind string) string {
	switch kind {
	case "job":
		return w.jobID
	case "plan":
		return w.planID
	case "todo":
		return w.todoID
	case "schedule":
		return w.scheduleID
	case "workflow":
		return w.workflowID
	case "session":
		return w.sessionID
	case "work_item":
		return w.workItemID
	case "tracker_repo":
		return w.trackerID
	case "worker":
		if w.project == "p1" {
			return "w-ok"
		}
		return "w-other"
	case "xfer":
		return w.xferID
	case "wakeup":
		return w.wakeupID
	case "retry":
		return w.retryID
	case "decision":
		return w.decisionID
	case "nudge":
		return w.nudgeID
	case "workbench_thread":
		return "j:" + w.jobID
	case "merge_suggestion":
		return w.mergeSugID
	case "memory_candidate":
		return w.candidateID
	case "memory_suggestion":
		return w.suggestionID
	case "issue":
		return w.issueID
	}
	return "x"
}

// genericParam fills a path parameter that is not the route's addressed resource.
func (w ceilWorld) genericParam(name string) string {
	switch name {
	case "todo_id":
		return w.todoID
	case "sid":
		return w.sessionID
	case "job_id":
		return w.jobID
	case "wid":
		return w.wakeupID
	case "tracker_id":
		return w.trackerID
	case "scope":
		return "project"
	case "scope_key":
		return w.project
	case "key":
		return "mem-key"
	case "field":
		return "title"
	case "n":
		return "1"
	}
	return "x"
}

// requestFor builds the request of route `key` against this world's resources.
func (w ceilWorld) requestFor(key string, decl routeDecl) (method, path string, body any) {
	method, tmpl, _ := strings.Cut(key, " ")
	segs := strings.Split(tmpl, "/")
	for i, seg := range segs {
		if len(seg) < 3 || seg[0] != '{' {
			continue
		}
		name := seg[1 : len(seg)-1]
		switch {
		case decl.scope.kind == scopePathProject && name == decl.scope.arg:
			segs[i] = w.project
		case decl.scope.kind == scopeResource && name == kindParam[decl.scope.arg]:
			segs[i] = w.kindID(decl.scope.arg)
		default:
			segs[i] = w.genericParam(name)
		}
	}
	path = strings.Join(segs, "/")
	if decl.scope.kind == scopeResource && decl.scope.arg == "tracker_repo" && !strings.Contains(tmpl, "{tracker_id}") {
		path += "?tracker_id=" + w.trackerID
	}
	if methodIsRead(method) {
		return method, path, nil
	}
	return method, path, w.bodyFor(key)
}

// bodyFor is the request body of a route; routes whose handler judges something in the body
// get one that names this world's resources, the rest a bare object.
func (w ceilWorld) bodyFor(key string) any {
	p := w.project
	exec := map[string]any{"project_key": p, "agent": "exec", "runner": "local", "cmd": []string{"go", "version"}, "cwd": "."}
	switch key {
	case "POST /v1/jobs":
		return exec
	case "POST /v1/work-items/capture":
		return map[string]any{"text": "x", "project": p}
	case "POST /v1/memory-suggestions":
		return map[string]any{"tracker_id": w.trackerID, "key": "mem-key", "action": "archive"}
	case "POST /v1/workflows":
		return map[string]any{"steps": []any{map[string]any{"name": "a", "project_key": p, "agent": "exec", "runner": "local", "cmd": []string{"go", "version"}, "cwd": "."}}}
	case "POST /v1/workflow-templates/{name}/render":
		return map[string]any{"vars": map[string]string{"project": p}}
	case "POST /v1/jobs/secret-scan":
		return map[string]any{"project": p, "literals": []string{"zzz"}}
	case "POST /v1/sessions":
		return map[string]any{"session_id": "s-new-" + p, "agent": "claude", "project_key": p}
	case "POST /v1/schedules":
		return map[string]any{"name": "n", "cron": "*/5 * * * *", "request": exec}
	case "POST /v1/plans":
		return map[string]any{"title": "t", "project": p}
	case "POST /v1/work-items":
		return map[string]any{"title": "t", "project_key": p}
	case "POST /v1/xfer", "POST /v1/xfer/precheck":
		return map[string]any{"op": "get", "runner": "local", "project": p, "path": "a"}
	case "POST /v1/projects":
		return map[string]any{"key": p + "-new"}
	case "POST /v1/memories":
		return map[string]any{"scope": "project", "scope_key": p, "key": "k", "content": "c"}
	case "POST /v1/decisions/{id}/answer":
		return map[string]any{"answer": "yes"}
	case "POST /v1/decisions":
		return map[string]any{"plan_id": w.planID, "title": "t", "question": "q"}
	case "POST /v1/session-ask":
		return map[string]any{"session_id": w.sessionID, "text": "hi"}
	case "POST /v1/work-items/delete":
		return map[string]any{"ids": []string{w.workItemID}}
	case "POST /v1/tracker/sync":
		return map[string]any{"tracker_id": w.trackerID, "project_key": p}
	case "POST /v1/tracker/issues/batch":
		return map[string]any{"tracker_id": w.trackerID, "ids": []string{w.issueID}, "set": map[string]any{}}
	}
	return map[string]any{}
}

// unfilteredAggregates are the Filter routes that stay unfiltered in phase 1 (design 「第二期」:
// aggregates and counts by scope); the reverse matrix does not assert their bodies.
var unfilteredAggregates = map[string]string{
	"GET /v1/today":              "cross-project aggregate (phase 2)",
	"GET /v1/today/handled":      "cross-project aggregate (phase 2)",
	"GET /v1/today/lanes":        "cross-project aggregate (phase 2)",
	"GET /v1/stats":              "cross-project counts (phase 2)",
	"GET /v1/stats/overview":     "cross-project counts (phase 2)",
	"GET /v1/work-items/digest":  "cross-project digest (phase 2)",
	"POST /v1/work-items/digest": "cross-project digest (phase 2)",
}

// TestEveryResourceKindHasResolver: every Resource(kind) of the route declarations has a
// resolver in resourceKinds, and the table carries no kind no route uses.
func TestEveryResourceKindHasResolver(t *testing.T) {
	t.Parallel()
	used := map[string]bool{}
	for key, decl := range routeDecls {
		if decl.scope.kind != scopeResource {
			continue
		}
		used[decl.scope.arg] = true
		rk, ok := resourceKinds[decl.scope.arg]
		if !ok {
			t.Errorf("%s: Resource(%q) has no resolver in resourceKinds", key, decl.scope.arg)
			continue
		}
		if rk.id == nil || rk.scope == nil {
			t.Errorf("%s: resolver of %q is incomplete", key, decl.scope.arg)
		}
	}
	for kind := range resourceKinds {
		if !used[kind] {
			t.Errorf("resourceKinds carries %q, which no route declares", kind)
		}
	}
}

// TestCeilingAppliesToUserCallersOnly: a job credential and a worker token are never
// governed by a ceiling here (S5 owns job credentials), whatever their caller id says.
func TestCeilingAppliesToUserCallersOnly(t *testing.T) {
	t.Parallel()
	s := newCeilingServer(t)
	for _, kind := range []string{callerKindJob, callerKindWorker} {
		c := &rux.Context{}
		c.Set(ctxCallerID, "lim")
		c.Set(ctxCallerKind, kind)
		if got := s.ceilingOf(c); got != nil {
			t.Errorf("%s caller of a ceilinged id got ceiling %+v, want none", kind, got)
		}
	}
	c := &rux.Context{}
	c.Set(ctxCallerID, "lim")
	if s.ceilingOf(c) == nil {
		t.Error("user caller lim lost its ceiling")
	}
	c = &rux.Context{}
	c.Set(ctxCallerID, "plain")
	if s.ceilingOf(c) != nil {
		t.Error("user caller plain has a ceiling")
	}
}

// TestCeilingAllowsProjectRules pins the no-project rule of the HTTP layer.
func TestCeilingAllowsProjectRules(t *testing.T) {
	t.Parallel()
	limited := &config.CallerCeiling{Projects: []string{"p1", "app-*"}}
	open := &config.CallerCeiling{ReadOnly: boolptr(true)}
	cases := []struct {
		name    string
		ceil    *config.CallerCeiling
		project string
		want    bool
	}{
		{"nil ceiling admits all", nil, "p2", true},
		{"unrestricted projects admit all", open, "p2", true},
		{"unrestricted projects admit no project", open, "", true},
		{"listed project", limited, "p1", true},
		{"glob project", limited, "app-web", true},
		{"unlisted project", limited, "p2", false},
		{"no project is outside", limited, "", false},
	}
	for _, tc := range cases {
		if got := ceilingAllowsProject(tc.ceil, tc.project); got != tc.want {
			t.Errorf("%s: ceilingAllowsProject(%q) = %v, want %v", tc.name, tc.project, got, tc.want)
		}
	}
	if ceilingAllowsAll(limited, nil) {
		t.Error("a record naming no project at all must be outside a project-restricting ceiling")
	}
	if ceilingAllowsAll(limited, []string{"p1", "p2"}) {
		t.Error("a record with one project outside is outside")
	}
}

// ceilingStateCases: one route per Resource kind — a request that addresses the world's
// record of that kind, and whether a caller holding nothing is refused with a 403 in scope.
var ceilingStateCases = []struct {
	kind string
	key  string
	// perm403: the route needs a permission, so an in-scope caller with no grants gets 403.
	perm403 bool
}{
	{"job", "POST /v1/jobs/{id}/cancel", true},
	{"plan", "POST /v1/plans/{id}/pause", true},
	{"todo", "POST /v1/todos/{todo_id}/dispatch", true},
	{"schedule", "POST /v1/schedules/{id}/disable", true},
	{"workflow", "POST /v1/workflows/{id}/cancel", true},
	{"session", "POST /v1/sessions/{sid}/say", true},
	// Public: a work item has no owner or permission of its own to lack.
	{"work_item", "POST /v1/work-items/{id}/journal", false},
	{"tracker_repo", "POST /v1/tracker/repos/{tracker_id}/sync", true},
	{"memory", "PUT /v1/memories/{scope}/{scope_key}/{key}", true},
	{"xfer", "DELETE /v1/xfer/{id}", true},
	{"wakeup", "DELETE /v1/wakeups/{wid}", true},
	{"retry", "DELETE /v1/retries/{rid}", true},
	{"decision", "POST /v1/decisions/{id}/answer", true},
	{"nudge", "DELETE /v1/nudges/{id}", true},
	{"workbench_thread", "POST /v1/workbench/threads/{id}/review", true},
	{"memory_candidate", "POST /v1/memory-candidates/{n}/accept", true},
	{"memory_suggestion", "POST /v1/memory-suggestions/{n}/adopt", true},
	{"merge_suggestion", "POST /v1/work-items/merge-suggestions/{n}/accept", false},
	{"issue", "GET /v1/issues/{id}", false},
	{"worker", "GET /v1/workers/{id}", false},
}

// TestCeilingThreeStatePerResourceKind: for each Resource kind, a ceilinged caller
//   - gets 404 for a record outside its projects — whatever it holds, writes included — in the
//     same body shape a missing record gets;
//   - passes the ceiling for a record inside (the handler then answers whatever it answers);
//   - gets the existing 403 for an in-scope record it lacks the permission for.
func TestCeilingThreeStatePerResourceKind(t *testing.T) {
	s := newCeilingServer(t)
	in := seedCeilingWorld(t, s, "p1")
	out := seedCeilingWorld(t, s, "p2")
	kindsCovered := map[string]bool{}

	for _, tc := range ceilingStateCases {
		kindsCovered[tc.kind] = true
		t.Run(tc.kind, func(t *testing.T) {
			decl := routeDecls[tc.key]
			if decl.scope.kind != scopeResource || decl.scope.arg != tc.kind {
				t.Fatalf("%s is not declared Resource(%q): %v", tc.key, tc.kind, decl.scope)
			}
			// Out of scope: 404, with every permission held.
			grantAll(s)
			m, p, body := out.requestFor(tc.key, decl)
			status, resp := ceilReq(t, s, ceilTokLim, m, p, body)
			if status != http.StatusNotFound {
				t.Fatalf("out of scope %s %s = %d (%s), want 404", m, p, status, resp)
			}
			if tc.kind != "issue" {
				notFoundShape(t, resp)
			}
			// A record that does not exist answers like the out-of-scope one (the ceiling must
			// not be tellable from absence).
			if tc.kind != "worker" && tc.kind != "issue" {
				missing := out
				missing.project = "p1"
				mm, mp, mbody := missing.requestFor(tc.key, decl)
				absent := "no-such-record"
				if kindParam[tc.kind] == "n" {
					absent = "99999" // numeric ids
				}
				mp = strings.ReplaceAll(mp, out.kindID(tc.kind), absent)
				if tc.kind == "memory" {
					mp = strings.ReplaceAll(mp, "/project/p1/", "/project/no-such-project/")
				}
				mstatus, mresp := ceilReq(t, s, ceilTokLim, mm, mp, mbody)
				if mstatus != http.StatusNotFound && tc.kind != "memory" {
					t.Errorf("a missing record answers %d %q, want 404", mstatus, mresp)
				}
			}
			if tc.kind == "issue" {
				_, mresp := ceilReq(t, s, ceilTokLim, http.MethodGet, "/v1/issues/ISS-nope", nil)
				if mresp != resp {
					t.Errorf("an out-of-scope issue answers %q, a missing one %q: the ceiling is tellable from absence", resp, mresp)
				}
			}

			if tc.perm403 {
				// In scope, no grants: the existing permission 403. Out of scope, no grants:
				// still 404. (Before the destructive in-scope request below.)
				s.setDBGrants(nil)
				m, p, body = in.requestFor(tc.key, decl)
				status, resp = ceilReq(t, s, ceilTokLim, m, p, body)
				if status != http.StatusForbidden || !strings.Contains(resp, "permission required") {
					t.Fatalf("in scope without permission %s %s = %d (%s), want a 403 naming the permission", m, p, status, resp)
				}
				m, p, body = out.requestFor(tc.key, decl)
				status, resp = ceilReq(t, s, ceilTokLim, m, p, body)
				if status != http.StatusNotFound {
					t.Fatalf("out of scope without permission %s %s = %d (%s), want 404", m, p, status, resp)
				}
			}

			// In scope with every permission: not refused by the ceiling.
			grantAll(s)
			m, p, body = in.requestFor(tc.key, decl)
			status, resp = ceilReq(t, s, ceilTokLim, m, p, body)
			if strings.Contains(resp, `"caller ceiling"`) {
				t.Fatalf("in scope %s %s refused by the ceiling: %d %s", m, p, status, resp)
			}
			if status == http.StatusNotFound && strings.Contains(resp, `"error":"not found"`) {
				t.Fatalf("in scope %s %s answered as out of scope: %s", m, p, resp)
			}
		})
	}

	// today_card has no project at all: a restricted caller never reaches it.
	grantAll(s)
	for _, key := range []string{"POST /v1/today/advice", "POST /v1/today/actions"} {
		status, resp := ceilReq(t, s, ceilTokLim, http.MethodPost, strings.TrimPrefix(key, "POST "), map[string]any{})
		if status != http.StatusNotFound {
			t.Errorf("%s = %d (%s), want 404 for a restricted caller", key, status, resp)
		}
	}
	kindsCovered["today_card"] = true

	for kind := range resourceKinds {
		if !kindsCovered[kind] {
			t.Errorf("resource kind %q has no three-state case", kind)
		}
	}
}

// TestCeilingWorkerKind: the workers list decides what a restricted caller sees of a worker,
// and managing workers is refused outright (403).
func TestCeilingWorkerKind(t *testing.T) {
	s := newCeilingServer(t)
	grantAll(s)
	if status, body := ceilReq(t, s, ceilTokLim, http.MethodGet, "/v1/workers/w-other", nil); status != http.StatusNotFound {
		t.Errorf("worker outside the workers list = %d (%s), want 404", status, body)
	}
	if status, body := ceilReq(t, s, ceilTokLim, http.MethodGet, "/v1/workers/w-ok", nil); status == http.StatusNotFound && strings.Contains(body, `"error":"not found"`) {
		t.Errorf("worker inside the workers list answered as out of scope: %s", body)
	}
	for _, path := range []string{"/v1/workers/w-ok", "/v1/workers/w-other"} {
		status, body := ceilReq(t, s, ceilTokLim, http.MethodDelete, path, nil)
		if status != http.StatusForbidden || !strings.Contains(body, "caller ceiling") {
			t.Errorf("DELETE %s = %d (%s), want the ceiling 403", path, status, body)
		}
	}
}

// bodyProjectCases: every BodyProject route, with a body naming a project OUTSIDE the
// ceiling. The handler must call requireProjectScope (or requireResourceScope for the one
// that names a tracker) after binding, which is what makes these 404.
var bodyProjectKeys = []string{
	"POST /v1/jobs", "POST /v1/work-items/capture", "POST /v1/memory-suggestions", "POST /v1/workflows",
	"POST /v1/workflow-templates/{name}/render", "POST /v1/jobs/secret-scan", "POST /v1/sessions",
	"POST /v1/schedules", "POST /v1/plans", "POST /v1/work-items", "POST /v1/xfer", "POST /v1/xfer/precheck",
	"POST /v1/projects", "POST /v1/memories",
}

// TestBodyProjectHandlersCheckScope: each BodyProject handler really calls the scope check —
// an out-of-scope project in the body is a 404 for the restricted caller even though it holds
// every permission, while the unrestricted caller is not stopped by it.
func TestBodyProjectHandlersCheckScope(t *testing.T) {
	s := newCeilingServer(t)
	out := seedCeilingWorld(t, s, "p2")
	grantAll(s)

	declared := map[string]bool{}
	for key, decl := range routeDecls {
		if decl.scope.kind == scopeBodyProject {
			declared[key] = true
		}
	}
	listed := map[string]bool{}
	for _, key := range bodyProjectKeys {
		listed[key] = true
		if !declared[key] {
			t.Errorf("%s is listed but not declared BodyProject", key)
		}
	}
	for key := range declared {
		if !listed[key] {
			t.Errorf("BodyProject route %s has no handler-check case: add it to bodyProjectKeys", key)
		}
	}

	for _, key := range bodyProjectKeys {
		t.Run(key, func(t *testing.T) {
			decl := routeDecls[key]
			m, p, body := out.requestFor(key, decl)
			status, resp := ceilReq(t, s, ceilTokLim, m, p, body)
			if status != http.StatusNotFound {
				t.Fatalf("restricted %s %s with an out-of-scope body = %d (%s), want 404", m, p, status, resp)
			}
			notFoundShape(t, resp)
			// The unrestricted caller is not stopped by the scope check (the handler may refuse
			// for its own reasons, but never with the out-of-scope shape).
			status, resp = ceilReq(t, s, ceilTokPlain, m, p, body)
			if status == http.StatusNotFound && strings.Contains(resp, `"error":"not found"`) {
				t.Fatalf("unrestricted %s %s answered as out of scope: %s", m, p, resp)
			}
		})
	}
}

// TestCeilingSecondaryBodyReferences: ids a body names besides the route's own resource are
// judged too (a restricted caller cannot pull a foreign record in through a body).
func TestCeilingSecondaryBodyReferences(t *testing.T) {
	s := newCeilingServer(t)
	in := seedCeilingWorld(t, s, "p1")
	out := seedCeilingWorld(t, s, "p2")
	grantAll(s)
	cases := []struct {
		name, method, path string
		body               any
	}{
		{"work item merge pulls a foreign item", "POST", "/v1/work-items/" + in.workItemID + "/merge", map[string]any{"sources": []string{out.workItemID}}},
		{"work item merge suggestion names a foreign source", "POST", "/v1/work-items/" + in.workItemID + "/merge-suggestions", map[string]any{"source_id": out.workItemID}},
		{"work item patch moves it to a foreign project", "PATCH", "/v1/work-items/" + in.workItemID, map[string]any{"project_key": "p2"}},
		{"work item attaches a foreign session", "POST", "/v1/work-items/" + in.workItemID + "/sessions", map[string]any{"session_id": out.sessionID}},
		{"work item splits foreign sessions", "POST", "/v1/work-items/" + in.workItemID + "/split", map[string]any{"title": "t", "session_ids": []string{out.sessionID}}},
		{"work item to-todo into a foreign plan", "POST", "/v1/work-items/" + in.workItemID + "/to-todo", map[string]any{"plan_id": out.planID}},
		{"work item create with a foreign session", "POST", "/v1/work-items", map[string]any{"title": "t", "project_key": "p1", "session_ids": []string{out.sessionID}}},
		{"session ask names a foreign work item", "POST", "/v1/session-ask", map[string]any{"session_id": in.sessionID, "text": "hi", "work_id": out.workItemID}},
		{"plan attaches a foreign job", "POST", "/v1/plans/" + in.planID + "/jobs", map[string]any{"job_id": out.jobID}},
		{"plan todo in a foreign project", "POST", "/v1/plans/" + in.planID + "/todos", map[string]any{"title": "t", "project": "p2"}},
		{"plan todo with a foreign job", "POST", "/v1/plans/" + in.planID + "/todos", map[string]any{"title": "t", "job_id": out.jobID}},
		{"tracker sync points a repo at a foreign project", "POST", "/v1/tracker/sync", map[string]any{"tracker_id": in.trackerID, "project_key": "p2"}},
		{"tracker sync registers a repo under a foreign project", "POST", "/v1/tracker/sync", map[string]any{"tracker_id": "trk-new", "project_key": "p2"}},
		{"tracker sync of an unknown repo without a project", "POST", "/v1/tracker/sync", map[string]any{"tracker_id": "trk-new"}},
		{"decision without a plan belongs to no project", "POST", "/v1/decisions", map[string]any{"title": "t", "question": "q"}},
		{"bulk work item delete by status only", "POST", "/v1/work-items/delete", map[string]any{"status": "dropped"}},
		{"global memory without global_memory", "POST", "/v1/memories", map[string]any{"scope": "global", "key": "k", "content": "c"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, resp := ceilReq(t, s, ceilTokLim, tc.method, tc.path, tc.body)
			if status != http.StatusNotFound {
				t.Fatalf("%s %s = %d (%s), want 404", tc.method, tc.path, status, resp)
			}
			notFoundShape(t, resp)
		})
	}
}

// TestCeilingReverseMatrix: for a caller restricted to p1 and holding EVERY permission, no
// route yields a 2xx for a resource of p2 — every PathProject / Resource / BodyProject route is
// a 404 (the worker kind's writes a 403), every NoProject write a 403, and no Filter list
// reveals a p2 record.
func TestCeilingReverseMatrix(t *testing.T) {
	s := newCeilingServer(t)
	in := seedCeilingWorld(t, s, "p1")
	out := seedCeilingWorld(t, s, "p2")
	grantAll(s)

	// Routes that authenticate themselves (a ticket, a token) never carry the restricted
	// caller's identity, so the ceiling has nothing to judge there.
	skipped := map[string]string{}
	var asserted int
	for _, key := range sortedRouteKeys() {
		decl := routeDecls[key]
		if decl.static {
			continue
		}
		if decl.user.kind == userNone {
			skipped[key] = "no user surface: it authenticates itself (" + decl.external + ") or with a worker credential"
			continue
		}
		method, path, body := out.requestFor(key, decl)
		status, resp := ceilReq(t, s, ceilTokLim, method, path, body)
		write := decl.effectiveRW(key) == rwWrite

		switch decl.scope.kind {
		case scopePathProject, scopeResource, scopeBodyProject:
			want := http.StatusNotFound
			if decl.scope.kind == scopeResource && decl.scope.arg == "worker" && write {
				want = http.StatusForbidden
			}
			if status != want {
				t.Errorf("%s: out-of-scope request %s %s = %d (%s), want %d", key, method, path, status, resp, want)
			}
			asserted++
		case scopeFilter:
			if _, ok := unfilteredAggregates[key]; ok {
				continue
			}
			for _, id := range out.ids() {
				if strings.Contains(resp, id) {
					t.Errorf("%s: the list answers %d and reveals %q of p2: %s", key, status, id, resp)
				}
			}
			asserted++
		case scopeNoProject:
			if write && decl.rw != rwRead {
				if status != http.StatusForbidden || !strings.Contains(resp, "caller ceiling") {
					t.Errorf("%s: NoProject write = %d (%s), want the ceiling 403", key, status, resp)
				}
				asserted++
			}
		}
		_ = in
	}
	if asserted < 200 {
		t.Errorf("the reverse matrix asserted only %d routes; the declarations or the request builder lost routes", asserted)
	}
	t.Logf("reverse matrix: %d routes asserted, %d skipped (self-authenticating)", asserted, len(skipped))
}

// TestCeilingFilterListsKeepInScopeRows: the lists answer for the caller's own projects and
// only those (the reverse matrix proves the absence, this the presence).
func TestCeilingFilterListsKeepInScopeRows(t *testing.T) {
	s := newCeilingServer(t)
	in := seedCeilingWorld(t, s, "p1")
	out := seedCeilingWorld(t, s, "p2")
	grantAll(s)
	lists := []struct {
		path string
		in   string
	}{
		{"/v1/jobs?all=1", in.jobID},
		{"/v1/plans", in.planID},
		{"/v1/sessions?all=1", in.sessionID},
		{"/v1/work-items", in.workItemID},
		{"/v1/schedules", in.scheduleID},
		{"/v1/workflows", in.workflowID},
		{"/v1/xfer", in.xferID},
		{"/v1/tracker/repos", in.trackerID},
		{"/v1/issues", in.issueID},
		{"/v1/memories?scope=project&scope_key=p1", "content p1"},
		{"/v1/memory-candidates", "candidate p1"},
		{"/v1/projects", "p1"},
	}
	for _, tc := range lists {
		t.Run(tc.path, func(t *testing.T) {
			status, body := ceilReq(t, s, ceilTokLim, http.MethodGet, tc.path, nil)
			if status != http.StatusOK {
				t.Fatalf("GET %s = %d (%s)", tc.path, status, body)
			}
			if !strings.Contains(body, tc.in) {
				t.Errorf("GET %s lost the in-scope row %q: %s", tc.path, tc.in, body)
			}
			for _, id := range out.ids() {
				if strings.Contains(body, id) {
					t.Errorf("GET %s reveals %q of p2: %s", tc.path, id, body)
				}
			}
			// The unrestricted caller still sees both.
			_, all := ceilReq(t, s, ceilTokPlain, http.MethodGet, tc.path, nil)
			if tc.path != "/v1/projects" && tc.path != "/v1/memories?scope=project&scope_key=p1" && !strings.Contains(all, out.project) {
				t.Errorf("GET %s lost the p2 rows for the unrestricted caller: %s", tc.path, all)
			}
		})
	}
	// The project list of the restricted caller names no other project.
	_, body := ceilReq(t, s, ceilTokLim, http.MethodGet, "/v1/projects", nil)
	if strings.Contains(body, "p2") || strings.Contains(body, "self") {
		t.Errorf("project list reveals a project outside the ceiling: %s", body)
	}
}

// TestCeilingReadOnlyMatrix: a read-only ceilinged caller gets 403 on every write route (the
// ReadSafe ones excepted) before any resource is looked up, and the reads and the ReadSafe
// writes are not stopped by the ceiling.
func TestCeilingReadOnlyMatrix(t *testing.T) {
	s := newCeilingServer(t)
	in := seedCeilingWorld(t, s, "p1")
	grantAll(s)

	var writes, reads int
	for _, key := range sortedRouteKeys() {
		decl := routeDecls[key]
		if decl.static {
			continue
		}
		if decl.user.kind == userNone {
			continue
		}
		method, path, body := in.requestFor(key, decl)
		status, resp := ceilReq(t, s, ceilTokRO, method, path, body)
		if decl.effectiveRW(key) == rwWrite {
			writes++
			if status != http.StatusForbidden || !strings.Contains(resp, "read-only") {
				t.Errorf("%s: write of a read-only caller = %d (%s), want the read-only 403", key, status, resp)
			}
			continue
		}
		reads++
		if strings.Contains(resp, `"caller ceiling"`) {
			t.Errorf("%s: read / ReadSafe request refused by the ceiling: %d %s", key, status, resp)
		}
		if decl.scope.kind == scopeResource && decl.scope.arg != "today_card" && decl.scope.arg != "worker" && decl.scope.arg != "issue" &&
			status == http.StatusNotFound && strings.Contains(resp, `"error":"not found"`) {
			t.Errorf("%s: in-scope read answered as out of scope: %s", key, resp)
		}
	}
	if writes < 100 || reads < 100 {
		t.Errorf("read-only matrix covered %d writes / %d reads; the builder lost routes", writes, reads)
	}
	// The explicit ReadSafe overrides pass for the read-only caller.
	for key := range reviewedReadSafe {
		decl := routeDecls[key]
		method, path, body := in.requestFor(key, decl)
		status, resp := ceilReq(t, s, ceilTokRO, method, path, body)
		if strings.Contains(resp, "read-only") || strings.Contains(resp, `"caller ceiling"`) {
			t.Errorf("ReadSafe route %s refused for the read-only caller: %d %s", key, status, resp)
		}
	}
}

// TestCeilingLeavesUnrestrictedCallersAlone: the same requests that are 404 / 403 for the
// restricted caller are not stopped by any ceiling for a caller without one.
func TestCeilingLeavesUnrestrictedCallersAlone(t *testing.T) {
	s := newCeilingServer(t)
	out := seedCeilingWorld(t, s, "p2")
	grantAll(s)
	for _, path := range []string{"/v1/jobs/" + out.jobID, "/v1/plans/" + out.planID, "/v1/sessions/" + out.sessionID, "/v1/work-items/" + out.workItemID} {
		status, body := ceilReq(t, s, ceilTokPlain, http.MethodGet, path, nil)
		if status != http.StatusOK {
			t.Errorf("unrestricted GET %s = %d (%s), want 200", path, status, body)
		}
	}
	status, body := ceilReq(t, s, ceilTokPlain, http.MethodPost, "/v1/jobs/"+out.jobID+"/cancel", nil)
	if strings.Contains(body, "caller ceiling") || (status == http.StatusNotFound && strings.Contains(body, `"error":"not found"`)) {
		t.Errorf("unrestricted write stopped by a ceiling: %d %s", status, body)
	}
}
