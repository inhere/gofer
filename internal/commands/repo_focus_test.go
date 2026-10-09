package commands

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/gookit/goutil/x/assert"
	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/tracker"
)

type fakeFocusClient struct {
	runnersErr, plansErr error
	plans                []client.Plan
	full                 map[string]client.Plan
	listOpts             client.PlanListOpts
}

func (f *fakeFocusClient) RunnersOverview() (client.RunnersOverview, error) {
	if f.runnersErr != nil {
		return client.RunnersOverview{}, f.runnersErr
	}
	return client.RunnersOverview{
		Server: client.RunnerServer{Version: "0.128.2 (6a52776)"},
		Runners: []client.RunnerMeta{
			{Name: "local", Type: "local", Status: "up"},
			{Name: "w-a", Type: "worker", Status: "connected", Worker: &client.RunnerWorkerBrief{GoferVersion: "0.128.2 (6a52776)"}},
			{Name: "w-b", Type: "worker", Status: "disconnected"},
			{Name: "peer", Type: "peer-http", Status: "up"},
		},
	}, nil
}

func (f *fakeFocusClient) ListPlans(opts client.PlanListOpts) (client.PlanList, error) {
	f.listOpts = opts
	return client.PlanList{Plans: f.plans}, f.plansErr
}

func (f *fakeFocusClient) GetPlan(id string) (client.Plan, error) {
	p, ok := f.full[id]
	if !ok {
		return client.Plan{}, errors.New("not found")
	}
	return p, nil
}

func TestFocusRemoteFromClient(t *testing.T) {
	f := &fakeFocusClient{
		plans: []client.Plan{
			{PlanID: "p-old", Title: "old", UpdatedAt: 1},
			{PlanID: "p-new", Title: "new", UpdatedAt: 3, TodoCounts: &jobstore.PlanTodoCounts{Total: 4, Done: 1, Skipped: 1}},
			{PlanID: "p-mid", Title: "mid", UpdatedAt: 2},
		},
		full: map[string]client.Plan{
			"p-new": {Todos: []client.Todo{{Title: "b", Sort: 2}, {Title: "a-done", Sort: 1, Status: "done"}, {Title: "c", Sort: 3}}},
			"p-mid": {Todos: []client.Todo{{Title: "x", Done: true}, {Title: "y", Status: "doing"}}},
		},
	}
	got, err := focusRemote(f, "proj")(context.Background())
	assert.NoErr(t, err)
	assert.Eq(t, "proj", f.listOpts.Project)
	assert.Eq(t, "open", f.listOpts.Status)
	assert.Eq(t, &tracker.FocusServer{Version: "0.128.2 (6a52776)", Workers: []tracker.FocusWorker{
		{Name: "w-a", Version: "0.128.2 (6a52776)", Online: true}, {Name: "w-b"},
	}}, got.Server)
	assert.Eq(t, []tracker.FocusPlan{
		{ID: "p-new", Title: "new", Done: 2, Total: 4, NextTodo: "b"},
		{ID: "p-mid", Title: "mid", Done: 1, Total: 2, NextTodo: "y"},
	}, got.Plans)

	// no project key: plans are not listed (other projects' plans are not 「在做」)
	f2 := &fakeFocusClient{plans: f.plans}
	got, _ = focusRemote(f2, "")(context.Background())
	assert.Nil(t, got.Plans)
	assert.Eq(t, "", f2.listOpts.Status)

	// failures drop their own part only
	got, err = focusRemote(&fakeFocusClient{runnersErr: errors.New("down"), plansErr: errors.New("down")}, "proj")(context.Background())
	assert.NoErr(t, err)
	assert.Nil(t, got.Server)
	assert.Nil(t, got.Plans)
}

func TestPrimeFocusSectionAndToggle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/runners" {
			_ = json.NewEncoder(w).Encode(map[string]any{"server": map[string]string{"version": "9.9.9"}, "runners": []any{}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	t.Setenv("GOFER_SERVER_ADDR", srv.URL)
	old := primeFocusGit
	primeFocusGit = func(context.Context, string, ...string) (string, error) { return "", errors.New("no git") }
	defer func() { primeFocusGit = old }()

	s, _, err := tracker.Init(t.TempDir(), "prime", true)
	assert.NoErr(t, err)
	body, err := primeWithServerContext(s, "", "")
	assert.NoErr(t, err)
	assert.StrContains(t, body, "## 当前重点（自动，")
	assert.StrContains(t, body, "- 服务：server 9.9.9\n")
	assert.NotContains(t, body, "仓库：")

	cfg, err := s.ReadConfig()
	assert.NoErr(t, err)
	off := false
	cfg.Prime.Focus = &off
	data, err := yaml.Marshal(cfg)
	assert.NoErr(t, err)
	assert.NoErr(t, os.WriteFile(filepath.Join(s.Dir, "config.yaml"), data, 0o644))
	body, err = primeWithServerContext(s, "", "")
	assert.NoErr(t, err)
	assert.NotContains(t, body, "当前重点")
}
