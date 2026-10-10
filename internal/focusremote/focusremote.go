// Package focusremote gathers the server-side part of the tracker prime
// 「当前重点」 section (the server version, the workers serving this project and
// its open plans).
//
// It sits between the command layer and internal/tracker: tracker must not
// import client (client already imports tracker), so the orchestration over the
// server API lives here and the command layer only binds the client and calls.
package focusremote

import (
	"context"
	"slices"
	"sort"
	"sync"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/tracker"
)

// Client is the slice of the server API the focus section reads.
type Client interface {
	RunnersOverview() (client.RunnersOverview, error)
	ListProjects() ([]client.ProjectMeta, error)
	ListPlans(opts client.PlanListOpts) (client.PlanList, error)
	GetPlan(id string) (client.Plan, error)
}

// planFetch caps how many open plans get a full GetPlan fetch.
const planFetch = 2

// Remote fetches the server version with this project's workers (withServer) and
// this project's open plans in parallel. Each failing call only drops its own part;
// plans and workers need a project key. planLimit is the ListPlans page size.
func Remote(cli Client, projectKey string, planLimit int, withServer bool) func(context.Context) (tracker.FocusRemote, error) {
	return func(context.Context) (tracker.FocusRemote, error) {
		var (
			out tracker.FocusRemote
			wg  sync.WaitGroup
		)
		if withServer {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ov, err := cli.RunnersOverview()
				if err != nil {
					return
				}
				var projects []client.ProjectMeta
				if projectKey != "" {
					projects, _ = cli.ListProjects()
				}
				out.Server = server(ov, projectKey, projects)
			}()
		}
		if projectKey != "" {
			wg.Add(1)
			go func() {
				defer wg.Done()
				out.Plans = plans(cli, projectKey, planLimit)
			}()
		}
		wg.Wait()
		return out, nil
	}
}

// server keeps only the workers that serve projectKey: the prime goes to every
// project's sessions, so it must not list the other projects' workers.
func server(ov client.RunnersOverview, projectKey string, projects []client.ProjectMeta) *tracker.FocusServer {
	srv := &tracker.FocusServer{Version: ov.Server.Version, UTCOffsetSec: ov.Server.UTCOffsetSec}
	allowed, known := projectRunners(projects, projectKey)
	for _, r := range ov.Runners {
		if r.Type != "worker" || !servesProject(r, projectKey, allowed, known) {
			continue
		}
		w := tracker.FocusWorker{Name: r.Name, Online: r.Status == "connected"}
		if r.Worker != nil {
			w.Version = r.Worker.GoferVersion
		}
		srv.Workers = append(srv.Workers, w)
	}
	return srv
}

// projectRunners is the allowed_runners set of projectKey; known is false when the
// server does not describe the project (unknown key, or a worker-only project whose
// allowlists live on the worker).
func projectRunners(projects []client.ProjectMeta, projectKey string) (map[string]bool, bool) {
	for _, p := range projects {
		if p.Key != projectKey || p.WorkerOnly {
			continue
		}
		set := make(map[string]bool, len(p.AllowedRunners))
		for _, r := range p.AllowedRunners {
			set[r] = true
		}
		return set, true
	}
	return nil, false
}

// servesProject: a worker runner serves the project when the project's
// allowed_runners lists it (if the server describes the project) and the worker's
// reported projects, when it reports any, include the key.
func servesProject(r client.RunnerMeta, projectKey string, allowed map[string]bool, known bool) bool {
	if projectKey == "" || (known && !allowed[r.Name]) {
		return false
	}
	if c := r.Capabilities; c != nil && len(c.Projects) > 0 {
		return slices.Contains(c.Projects, projectKey)
	}
	return known
}

func plans(cli Client, projectKey string, planLimit int) []tracker.FocusPlan {
	list, err := cli.ListPlans(client.PlanListOpts{Status: "open", Project: projectKey, Limit: planLimit})
	if err != nil {
		return nil
	}
	plans := list.Plans
	sort.SliceStable(plans, func(i, j int) bool { return plans[i].UpdatedAt > plans[j].UpdatedAt })
	if len(plans) > planFetch {
		plans = plans[:planFetch]
	}
	out := make([]tracker.FocusPlan, len(plans))
	var wg sync.WaitGroup
	for i, p := range plans {
		out[i] = tracker.FocusPlan{ID: p.PlanID, Title: p.Title}
		if p.TodoCounts != nil {
			out[i].Done = p.TodoCounts.Done + p.TodoCounts.Skipped
			out[i].Total = p.TodoCounts.Total
		}
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			full, err := cli.GetPlan(id)
			if err != nil {
				return
			}
			out[i].NextTodo = firstOpenTodo(full.Todos)
			if out[i].Total == 0 && len(full.Todos) > 0 {
				out[i].Total = len(full.Todos)
				for _, t := range full.Todos {
					if todoFinished(t) {
						out[i].Done++
					}
				}
			}
		}(i, p.PlanID)
	}
	wg.Wait()
	return out
}

func todoFinished(t client.Todo) bool {
	return t.Done || t.Status == "done" || t.Status == "skipped"
}

// firstOpenTodo is the title of the first unfinished todo in plan order.
func firstOpenTodo(todos []client.Todo) string {
	sorted := append([]client.Todo(nil), todos...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Sort < sorted[j].Sort })
	for _, t := range sorted {
		if !todoFinished(t) {
			return t.Title
		}
	}
	return ""
}

// WorkLister is the slice of the server API HasOpenWork reads.
type WorkLister interface {
	ListWorkItems(o client.WorkListOpts) (client.WorkList, error)
}

// HasOpenWork reports whether the server has an open (not closed, not merged)
// work item of projectKey. Errors and an empty key count as none.
func HasOpenWork(cli WorkLister, projectKey string) bool {
	if projectKey == "" {
		return false
	}
	list, err := cli.ListWorkItems(client.WorkListOpts{Project: projectKey, Limit: 1})
	return err == nil && len(list.Items) > 0
}
