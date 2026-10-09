package commands

import (
	"context"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/tracker"
)

// primeFocusTimeout caps the whole 「当前重点」 collection (design §2.4: ≤ 1s).
const primeFocusTimeout = time.Second

// primeFocusGit is the git runner of the focus section (tests swap it).
var primeFocusGit tracker.GitRunner = tracker.ExecGit

// primeFocus renders the 「当前重点」 section; "" when nothing is available.
// Server data is optional: without a reachable server only local parts show.
func primeFocus(s *tracker.Store, configPath string, now time.Time) string {
	ctx, cancel := context.WithTimeout(context.Background(), primeFocusTimeout)
	defer cancel()
	src := tracker.FocusSources{Git: primeFocusGit}
	root, _ := os.Getwd()
	if addr, projectKey, err := primeServerTarget(s, configPath, root); err == nil && addr != "" {
		cli := client.NewWithTimeout(addr, os.Getenv("GOFER_SERVER_TOKEN"), primeClientTimeout)
		src.Remote = focusRemote(cli, projectKey)
	}
	return s.BuildFocus(ctx, now, src)
}

// focusClient is the slice of the server API the focus section reads.
type focusClient interface {
	RunnersOverview() (client.RunnersOverview, error)
	ListPlans(opts client.PlanListOpts) (client.PlanList, error)
	GetPlan(id string) (client.Plan, error)
}

// focusRemote fetches the server / worker versions and this project's open plans
// in parallel. Each failing call only drops its own part; plans need a project key.
func focusRemote(cli focusClient, projectKey string) func(context.Context) (tracker.FocusRemote, error) {
	return func(context.Context) (tracker.FocusRemote, error) {
		var (
			out tracker.FocusRemote
			wg  sync.WaitGroup
		)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ov, err := cli.RunnersOverview(); err == nil {
				out.Server = focusServer(ov)
			}
		}()
		if projectKey != "" {
			wg.Add(1)
			go func() {
				defer wg.Done()
				out.Plans = focusPlans(cli, projectKey)
			}()
		}
		wg.Wait()
		return out, nil
	}
}

func focusServer(ov client.RunnersOverview) *tracker.FocusServer {
	srv := &tracker.FocusServer{Version: ov.Server.Version}
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

const focusPlanFetch = 2

func focusPlans(cli focusClient, projectKey string) []tracker.FocusPlan {
	list, err := cli.ListPlans(client.PlanListOpts{Status: "open", Project: projectKey, Limit: clientPlanPrimeLimit})
	if err != nil {
		return nil
	}
	plans := list.Plans
	sort.SliceStable(plans, func(i, j int) bool { return plans[i].UpdatedAt > plans[j].UpdatedAt })
	if len(plans) > focusPlanFetch {
		plans = plans[:focusPlanFetch]
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
