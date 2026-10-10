package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gookit/goutil/x/assert"
	"github.com/inhere/gofer/internal/brief"
	"github.com/inhere/gofer/internal/tracker"
)

// `gofer issue brief` works without a server: the server sections carry a note.
func TestIssueBriefCLIOffline(t *testing.T) {
	t.Setenv("GOFER_SERVER_ADDR", "http://127.0.0.1:1")
	root := t.TempDir()
	trackerRunOK(t, root, "repo", "init")
	out := trackerRunOK(t, root, "issue", "create", "brief target", "--json")
	var item tracker.Issue
	assert.Require(t, assert.NoErr(t, json.Unmarshal([]byte(out), &item)))

	// -s beats any server.addr a host config supplies (the env alone did not on Windows).
	text := trackerRunOK(t, root, "issue", "brief", item.ID, "-s", "http://127.0.0.1:1")
	assert.Contains(t, text, "# 接手包 issue "+item.ID)
	assert.Contains(t, text, "## 相关 job / plan\n（未连接 server，本节跳过")
	assert.Contains(t, text, "--claim")

	out = trackerRunOK(t, root, "issue", "brief", item.ID, "-s", "http://127.0.0.1:1", "--json", "--max-lines", "12")
	var b brief.Brief
	assert.Require(t, assert.NoErr(t, json.Unmarshal([]byte(out), &b)))
	assert.Eq(t, "issue", b.Kind)
	assert.Eq(t, 8, len(b.Sections))
	assert.True(t, b.Sections[0].Truncated > 0) // --max-lines reached the flag
	assert.Contains(t, b.Text(), "[本节截断 ")

	_, code := trackerCLI(t, root, "plan", "brief", "plan-x")
	assert.NotEq(t, 0, code)
}

// The prime 「进行中 plan」 section lists each plan with the brief hint and its doing /
// ready todos plus the issues they name, even without a handoff note.
func TestPrimePlanSectionListsTodos(t *testing.T) {
	root := t.TempDir()
	s, _, err := tracker.Init(root, "pp", true)
	assert.Require(t, assert.NoErr(t, err))
	assert.Require(t, assert.NoErr(t, s.SetProjectKey("proj")))
	item, err := s.CreateIssue(tracker.Issue{Title: "x", Type: "task", Priority: 2})
	assert.Require(t, assert.NoErr(t, err))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/plans":
			_, _ = w.Write([]byte(`{"plans":[{"plan_id":"p1","status":"open","updated_at":2}],"total":1}`))
		case "/v1/plans/p1":
			_, _ = w.Write([]byte(`{"plan_id":"p1","title":"work","status":"open","todos":[{"todo_id":"a","title":"T1 do ` + item.ID + `","status":"doing"},{"todo_id":"b","title":"T2 later","status":"pending"},{"todo_id":"c","title":"T3 next","status":"ready"}]}`))
		case "/v1/plans/p1/handoff":
			_, _ = w.Write([]byte(`{"plan_id":"p1","version":0}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("GOFER_SERVER_ADDR", server.URL)
	p3Write(t, root, "config.yaml", "server: {}\n")
	t.Chdir(root)
	body, err := primeWithServerHandoffs(s, filepath.Join(root, "config.yaml"))
	assert.Require(t, assert.NoErr(t, err))
	assert.Contains(t, body, "## 进行中 plan\n\n- p1 work（接手：`gofer plan brief p1`）\n  - [doing] T1 do "+item.ID+" · "+item.ID+"\n  - [ready] T3 next\n")
	assert.NotContains(t, body, "T2 later")
	assert.Contains(t, body, tracker.PrimeTakeoverHint)
}
