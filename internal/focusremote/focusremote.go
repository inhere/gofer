// Package focusremote gathers the server-side part of the tracker prime
// 「当前重点」 section (server / worker versions and this project's open plans).
//
// It sits between the command layer and internal/tracker: tracker must not
// import client (client already imports tracker), so the orchestration over the
// server API lives here and the command layer only binds the client and calls.
package focusremote

import (
	"context"
	"sort"
	"sync"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/tracker"
)

// Client is the slice of the server API the focus section reads.
type Client interface {
	RunnersOverview() (client.RunnersOverview, error)
	ListPlans(opts client.PlanListOpts) (client.PlanList, error)
	GetPlan(id string) (client.Plan, error)
}

// planFetch caps how many open plans get a full GetPlan fetch.
const planFetch = 2

// Remote fetches the server / worker versions and this project's open plans
// in parallel. Each failing call only drops its own part; plans need a project key.
// planLimit is the ListPlans page size.
func Remote(cli Client, projectKey string, planLimit int) func(context.Context) (tracker.FocusRemote, error) {
	return func(context.Context) (tracker.FocusRemote, error) {
		var (
			out tracker.FocusRemote
			wg  sync.WaitGroup
		)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ov, err := cli.RunnersOverview(); err == nil {
				out.Server = server(ov)
			}
		}()
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

func server(ov client.RunnersOverview) *tracker.FocusServer {
	srv := &tracker.FocusServer{Version: ov.Server.Version, UTCOffsetSec: ov.Server.UTCOffsetSec}
	for _, r := range ov.Runners {
		if r.Type != "worker" {
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
