package httpapi

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/messenger"
	"github.com/inhere/gofer/internal/project"
	runnerpkg "github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
	"github.com/inhere/gofer/internal/wsproto"
)

// fakeResident is a residentMessenger that counts ListAgents calls.
type fakeResident struct {
	calls atomic.Int32
	err   error
	snap  messenger.Snapshot
}

func (f *fakeResident) Status(string) string               { return f.snap.Status }
func (f *fakeResident) Snapshot(string) messenger.Snapshot { return f.snap }
func (f *fakeResident) Command() string                    { return "claude-test" }
func (f *fakeResident) ListAgents(_ context.Context, runner, _ string, command []string) (messenger.AgentList, error) {
	f.calls.Add(1)
	if f.err != nil {
		return messenger.AgentList{}, f.err
	}
	if runner != "local" || len(command) != 1 || command[0] != "claude-test" {
		return messenger.AgentList{}, errors.New("unexpected args")
	}
	return messenger.ParseAgents("This session is m [aaaaaa] — x\n\nPeer sessions (1):\n  proj-a [dc779b]  ·  interactive  ·  idle  ·  started 3d ago"), nil
}

func getJSON(t *testing.T, s *Server, path string, into any) int {
	t.Helper()
	resp := do(t, s, http.MethodGet, path, testToken, nil)
	if into != nil && resp.StatusCode == http.StatusOK {
		decode(t, resp, into)
	}
	return resp.StatusCode
}

func TestRunnersExposeMessengerSnapshotAndDirs(t *testing.T) {
	ws := t.TempDir()
	t.Setenv("GOFER_WORKSPACE", ws)
	runnersCfg := map[string]config.RunnerConfig{"r1": {Type: "worker", WorkerID: "w1"}, "r2": {Type: "worker", WorkerID: "w2"}}
	workers := fakeWorkers{
		"w1": {Connected: true, MessengerStatus: "stopped",
			MessengerDetail: &messenger.Snapshot{Status: "busy", StderrTail: "oops", Deliveries: []messenger.Delivery{{At: 1, OK: false, Error: "x"}}},
			Dirs: &DirsView{Workspace: &DirEntry{Path: "/w", Exists: false},
				Roots:    []RootEntry{{From: "/srv", To: "/data", Exists: true}},
				Projects: []ProjectDirEntry{{Key: "p", Path: "/data/p", Exists: false}}},
			PolicyRejected: []PolicyRejection{{Key: "bad", Reason: "path_outside_roots"}}},
		"w2": {Connected: true, MessengerStatus: "running"}, // an older worker: register-time value only
	}
	s := newRunnersServer(t, runnersCfg, nil, workers)
	fr := &fakeResident{snap: messenger.Snapshot{Status: "idle", StartedAt: 5}}
	s.residentMessenger = fr
	rows := byName(listRunners(t, s))

	loc := rows["local"]
	if loc.Messenger != "idle" || loc.MessengerDetail == nil || loc.MessengerDetail.StartedAt != 5 {
		t.Fatalf("local messenger = %q / %+v", loc.Messenger, loc.MessengerDetail)
	}
	if loc.Dirs == nil || loc.Dirs.Workspace == nil || loc.Dirs.Workspace.Path != ws || !loc.Dirs.Workspace.Exists {
		t.Fatalf("local dirs = %+v", loc.Dirs)
	}
	if len(loc.Dirs.Projects) != 1 || loc.Dirs.Projects[0].Key != "self" || !loc.Dirs.Projects[0].Exists {
		t.Fatalf("local project dirs = %+v", loc.Dirs.Projects)
	}

	w1 := rows["r1"]
	if w1.Messenger != "busy" || w1.MessengerDetail == nil || w1.MessengerDetail.StderrTail != "oops" || w1.Worker.MessengerDetail == nil {
		t.Fatalf("worker messenger = %q / %+v", w1.Messenger, w1.MessengerDetail)
	}
	if w1.Dirs == nil || w1.Dirs.Roots[0].To != "/data" || w1.Dirs.Projects[0].Exists || w1.Worker.Dirs == nil {
		t.Fatalf("worker dirs = %+v", w1.Dirs)
	}
	w2 := rows["r2"]
	if w2.Messenger != "running" || w2.MessengerDetail != nil || w2.Dirs != nil {
		t.Fatalf("old worker row must show only the register-time status: %+v", w2)
	}
}

func TestLocalDirsReportMissingProjectDir(t *testing.T) {
	t.Setenv("GOFER_WORKSPACE", filepath.Join(t.TempDir(), "nope"))
	s := newRunnersServer(t, nil, nil, nil)
	cfg := s.projects.Config()
	p := cfg.Projects["self"]
	p.HostPath = filepath.Join(t.TempDir(), "gone")
	cfg.Projects["self"] = p
	d := s.localDirs()
	if d.Workspace.Exists || d.Projects[0].Exists {
		t.Fatalf("missing dirs must be reported as not existing: %+v", d)
	}
	if err := os.MkdirAll(d.Workspace.Path, 0o755); err != nil {
		t.Fatal(err)
	}
	if !s.localDirs().Workspace.Exists {
		t.Fatal("workspace created later must show as existing")
	}
}

func TestRunnerMessengerAgentsCachesAndRefreshes(t *testing.T) {
	s := newRunnersServer(t, nil, nil, nil)
	fr := &fakeResident{}
	s.residentMessenger = fr
	var v messengerAgentsView
	if code := getJSON(t, s, "/v1/runners/local/messenger/agents", &v); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if v.Runner != "local" || v.Cached || v.Self != "m" || len(v.Agents) != 1 || v.Agents[0].Name != "proj-a" || !strings.Contains(v.RawOutput, "Peer sessions") || v.FetchedAt == 0 {
		t.Fatalf("view = %+v", v)
	}
	var v2 messengerAgentsView
	getJSON(t, s, "/v1/runners/server/messenger/agents", &v2) // the CLI spelling shares the entry
	if !v2.Cached || fr.calls.Load() != 1 {
		t.Fatalf("second read must hit the cache: cached=%v calls=%d", v2.Cached, fr.calls.Load())
	}
	var v3 messengerAgentsView
	getJSON(t, s, "/v1/runners/local/messenger/agents?refresh=1", &v3)
	if v3.Cached || fr.calls.Load() != 2 {
		t.Fatalf("refresh must bypass the cache: cached=%v calls=%d", v3.Cached, fr.calls.Load())
	}
	old := messengerAgentsTTL
	messengerAgentsTTL = time.Millisecond
	defer func() { messengerAgentsTTL = old }()
	time.Sleep(5 * time.Millisecond)
	getJSON(t, s, "/v1/runners/local/messenger/agents", nil)
	if fr.calls.Load() != 3 {
		t.Fatalf("an expired entry must be refetched, calls=%d", fr.calls.Load())
	}
}

func TestRunnerMessengerAgentsErrors(t *testing.T) {
	runnersCfg := map[string]config.RunnerConfig{
		"peer": {Type: "peer-http", BaseURL: "http://x"},
		"old":  {Type: "worker", WorkerID: "w-old"},
		"off":  {Type: "worker", WorkerID: "w-off"},
	}
	workers := fakeWorkers{"w-old": {Connected: true, ProtocolVersion: wsproto.MessengerListMinProtocolVersion - 1, Projects: []string{"p"}}}
	s := newRunnersServer(t, runnersCfg, nil, workers)
	fr := &fakeResident{}
	s.residentMessenger = fr
	cases := []struct {
		path string
		want int
		text string
	}{
		{"/v1/runners/nope/messenger/agents", http.StatusNotFound, ""},
		{"/v1/runners/peer/messenger/agents", http.StatusConflict, ""},
		{"/v1/runners/old/messenger/agents", http.StatusConflict, "升级"},
		{"/v1/runners/off/messenger/agents", http.StatusConflict, "没有连接"},
	}
	for _, tc := range cases {
		resp := do(t, s, http.MethodGet, tc.path, testToken, nil)
		if resp.StatusCode != tc.want {
			t.Fatalf("%s status = %d, want %d", tc.path, resp.StatusCode, tc.want)
		}
		if tc.text != "" {
			var body errorBody
			decode(t, resp, &body)
			if !strings.Contains(body.Detail, tc.text) {
				t.Fatalf("%s detail = %q, want it to mention %q", tc.path, body.Detail, tc.text)
			}
		}
	}
	// A failing listing is a 502 with the cause; a failure is not cached.
	fr.err = errors.New("boom")
	if code := getJSON(t, s, "/v1/runners/local/messenger/agents", nil); code != http.StatusBadGateway {
		t.Fatalf("failed listing status = %d, want 502", code)
	}
	fr.err = nil
	if code := getJSON(t, s, "/v1/runners/local/messenger/agents", nil); code != http.StatusOK || fr.calls.Load() != 2 {
		t.Fatalf("after a failure the next read must retry: calls=%d", fr.calls.Load())
	}
	if resp := do(t, s, http.MethodGet, "/v1/runners/local/messenger/agents", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", resp.StatusCode)
	}
}

// listingRunner stands in for a v16 worker runner: it records the forwarded
// dispatch and answers with the raw ListAgents text on stdout, like the real
// worker does for Op "list_agents".
type listingRunner struct {
	got chan *runnerpkg.MessengerDispatch
	out string
}

func (r *listingRunner) Name() string { return "r1" }
func (r *listingRunner) Run(_ context.Context, req runnerpkg.Request) runnerpkg.Result {
	if req.Forward != nil {
		r.got <- req.Forward.Messenger
	}
	_, _ = req.Stdout.Write([]byte(r.out))
	return runnerpkg.Result{}
}

func TestRunnerMessengerAgentsWorkerDispatchesListOp(t *testing.T) {
	root := t.TempDir()
	runnersCfg := map[string]config.RunnerConfig{"r1": {Type: "worker", WorkerID: "w1"}}
	cfg := &config.Config{
		Server:  config.ServerConfig{Token: testToken, Workers: map[string]config.WorkerAuthConfig{"w1": {Token: "w-token"}}},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {HostPath: root, AllowedAgents: []string{"exec"}, AllowedRunners: []string{"r1"}, AllowExec: true},
		},
		Runners: runnersCfg,
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	lr := &listingRunner{got: make(chan *runnerpkg.MessengerDispatch, 1),
		out: "This session is wm [bbbbbb] — x\n\nPeer sessions (1):\n  w-proj [111111]  ·  interactive  ·  busy  ·  started 1h ago"}
	runners := map[string]runnerpkg.Runner{localrunner.Name: localrunner.New(), "r1": lr}
	jobs := drainOnCleanup(t, job.NewService(cfg, projects, agents, runners, openTestStore(t, root), nil))
	eng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(eng)
	workers := fakeWorkers{"w1": {Connected: true, ProtocolVersion: wsproto.MessengerListMinProtocolVersion, Projects: []string{"self"}}}
	s := New(&cfg.Server, testToken, false, jobs, eng, projects, agents, nil, runnersCfg, nil, workers)

	var v messengerAgentsView
	if code := getJSON(t, s, "/v1/runners/r1/messenger/agents", &v); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if v.Self != "wm" || len(v.Agents) != 1 || v.Agents[0].Status != "busy" {
		t.Fatalf("view = %+v", v)
	}
	select {
	case d := <-lr.got:
		if d == nil || d.Op != "list_agents" || len(d.Command) != 3 || d.Command[2] != messenger.ListAgentsPrompt {
			t.Fatalf("forwarded messenger dispatch = %+v", d)
		}
	default:
		t.Fatal("the worker runner never received the dispatch")
	}
	// The delivery job is an internal messenger job: hidden from the default list.
	var list struct {
		Jobs []map[string]any `json:"jobs"`
	}
	getJSON(t, s, "/v1/jobs", &list)
	if len(list.Jobs) != 0 {
		t.Fatalf("list-agents job leaked into the default job list: %v", list.Jobs)
	}
	// A worker with no served project cannot take the job.
	workers["w1"] = WorkerStatus{Connected: true, ProtocolVersion: wsproto.MessengerListMinProtocolVersion}
	if code := getJSON(t, s, "/v1/runners/r1/messenger/agents?refresh=1", nil); code != http.StatusBadGateway {
		t.Fatalf("no-project worker status = %d, want 502", code)
	}
}
