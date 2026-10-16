package commands

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/httpapi"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
)

// newTrackerTestServer is newPlanTestServerStore with the tracker store wired, so
// POST /v1/work-items/{id}/dispatch to=issue can create a server-side issue.
func newTrackerTestServer(t *testing.T) (string, *jobstore.Store) {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Server:  config.ServerConfig{AllowEmptyToken: true},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {HostPath: root, AllowedAgents: []string{"exec"}, AllowedRunners: []string{"local"}, AllowExec: true},
		},
	}
	st, err := jobstore.Open(filepath.Join(root, "gofer.db"))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	jobs := job.NewService(cfg, projects, agents, runners, st, nil)
	eng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(eng)
	srv := httpapi.New(&cfg.Server, "", true, jobs, eng, projects, agents, nil, nil, nil, nil)
	srv.SetTrackerStore(st)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts.URL, st
}

func TestWorkDispatchToIssueCLI(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	server, st := newTrackerTestServer(t)
	if err := st.UpsertTrackerRepo(jobstore.TrackerRepo{TrackerID: "r1", ProjectKey: "self", Prefix: "zz"}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertTrackerRepo(jobstore.TrackerRepo{TrackerID: "bare", ProjectKey: "self"}); err != nil {
		t.Fatal(err)
	}
	w, err := st.CaptureWorkItem(jobstore.CaptureInput{Text: "login page 500s", Kind: jobstore.WorkKindIssue, ProjectKey: "self", By: "human:t"})
	if err != nil {
		t.Fatal(err)
	}

	// --to issue needs --tracker before any request is made.
	if out, code := workCLI(t, server, "dispatch", w.ID, "--to", "issue"); code == 0 || !strings.Contains(out, "--tracker") {
		t.Fatalf("missing --tracker must fail with a hint (exit %d):\n%s", code, out)
	}
	// A repo without an id prefix is refused and the item stays captured.
	if out, code := workCLI(t, server, "dispatch", w.ID, "--to", "issue", "--tracker", "bare"); code == 0 {
		t.Fatalf("a repo without a prefix must be refused:\n%s", out)
	}
	if got, _, _ := st.GetWorkItem(w.ID); got.Status != jobstore.WorkCaptured {
		t.Fatalf("item left the box after a refused dispatch: %s", got.Status)
	}

	out := workCLIOK(t, server, "dispatch", w.ID, "--to", "issue", "--tracker", "r1", "--title", "Login 500")
	if !strings.Contains(out, "dispatched "+w.ID+" to issue") {
		t.Fatalf("output = %q", out)
	}
	links, _ := st.ListWorkLinks(w.ID)
	if len(links) != 1 || links[0].Kind != jobstore.WorkLinkIssue || !strings.HasPrefix(links[0].Ref, "zz-") || !links[0].PendingSync {
		t.Fatalf("links = %+v", links)
	}
	recs, _ := st.ListTrackerIssues("r1", 0)
	if len(recs) != 1 || !strings.Contains(string(recs[0].Body), "Login 500") {
		t.Fatalf("issue records = %+v", recs)
	}
}
