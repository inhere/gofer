package httpapi

import (
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
)

// fakeProber is a static runnerProber for the /v1/runners handler tests.
type fakeProber struct{ results []ProbeResult }

func (f fakeProber) Snapshot() []ProbeResult { return f.results }

// fakeWorkers is a static workerRegistry keyed by worker_id.
type fakeWorkers map[string]WorkerStatus

func (f fakeWorkers) WorkerStatus(id string) (WorkerStatus, bool) { ws, ok := f[id]; return ws, ok }

// newRunnersServer builds a Server with a configured runner set + injected
// prober / workers, reusing the standard "self" project so auth fixtures match
// the rest of the suite. Either prober or workers may be nil to exercise the
// `unknown` fallbacks.
func newRunnersServer(t *testing.T, runnersCfg map[string]config.RunnerConfig, prober runnerProber, workers workerRegistry) *Server {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Server:  config.ServerConfig{Token: testToken},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {HostPath: root, AllowedAgents: []string{"exec"}, AllowedRunners: []string{"local"}, AllowExec: true},
		},
		Runners: runnersCfg,
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	jobs := job.NewService(cfg, projects, agents, runners, openTestStore(t, root))
	return New(&cfg.Server, testToken, false, jobs, projects, agents, nil, runnersCfg, prober, workers)
}

// listRunners GETs /v1/runners and returns the decoded rows.
func listRunners(t *testing.T, s *Server) []runnerView {
	t.Helper()
	resp := do(t, s, http.MethodGet, "/v1/runners", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list runners status=%d, want 200", resp.StatusCode)
	}
	var body struct {
		Runners []runnerView `json:"runners"`
	}
	decode(t, resp, &body)
	return body.Runners
}

// byName indexes runner rows by name for assertions.
func byName(rows []runnerView) map[string]runnerView {
	m := make(map[string]runnerView, len(rows))
	for _, r := range rows {
		m[r.Name] = r
	}
	return m
}

func TestListRunnersRequiresAuth(t *testing.T) {
	s := newRunnersServer(t, nil, nil, nil)
	resp := do(t, s, http.MethodGet, "/v1/runners", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-bearer status=%d, want 401", resp.StatusCode)
	}
	resp = do(t, s, http.MethodGet, "/v1/runners", "wrong", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong-token status=%d, want 401", resp.StatusCode)
	}
}

// TestListRunnersOnlyLocal: with no configured runners the endpoint reports just
// the implicit local row (status up), as a non-nil array.
func TestListRunnersOnlyLocal(t *testing.T) {
	s := newRunnersServer(t, nil, nil, nil)
	rows := listRunners(t, s)
	if len(rows) != 1 {
		t.Fatalf("want exactly the local row, got %d: %+v", len(rows), rows)
	}
	if rows[0].Name != "local" || rows[0].Type != "local" || rows[0].Status != "up" {
		t.Fatalf("local row wrong: %+v", rows[0])
	}
}

// TestListRunnersPeerHTTP: a peer-http runner with an up probe shows `up` + probe
// detail; one with a down probe shows `down` + error; one with no probe result
// (prober has no entry) shows `unknown`.
func TestListRunnersPeerHTTP(t *testing.T) {
	runnersCfg := map[string]config.RunnerConfig{
		"peer-up":      {Type: "peer-http", BaseURL: "https://up.internal:8765"},
		"peer-down":    {Type: "peer-http", BaseURL: "https://down.internal:8765"},
		"peer-unknown": {Type: "peer-http", BaseURL: "https://unk.internal:8765"},
	}
	prober := fakeProber{results: []ProbeResult{
		{Name: "peer-up", Up: true, CheckedAt: 1750300000000, LatencyMS: 12},
		{Name: "peer-down", Up: false, CheckedAt: 1750300000000, Err: "connection refused"},
	}}
	rows := byName(listRunners(t, newRunnersServer(t, runnersCfg, prober, nil)))

	up := rows["peer-up"]
	if up.Type != "peer-http" || up.Status != "up" || up.BaseURL == "" || up.Probe == nil || up.Probe.LatencyMS != 12 {
		t.Fatalf("peer-up row wrong: %+v / probe %+v", up, up.Probe)
	}
	down := rows["peer-down"]
	if down.Status != "down" || down.Probe == nil || down.Probe.Error == "" {
		t.Fatalf("peer-down row wrong: %+v / probe %+v", down, down.Probe)
	}
	unk := rows["peer-unknown"]
	if unk.Status != "unknown" || unk.Probe != nil {
		t.Fatalf("peer-unknown row wrong: %+v", unk)
	}
}

// TestListRunnersPeerHTTPNoProber: with a nil prober every peer-http row is
// `unknown` (no probing wired).
func TestListRunnersPeerHTTPNoProber(t *testing.T) {
	runnersCfg := map[string]config.RunnerConfig{
		"peer-x": {Type: "peer-http", BaseURL: "https://x.internal:8765"},
	}
	rows := byName(listRunners(t, newRunnersServer(t, runnersCfg, nil, nil)))
	if rows["peer-x"].Status != "unknown" {
		t.Fatalf("nil prober should yield unknown, got %+v", rows["peer-x"])
	}
}

// TestListRunnersWorker: a connected worker shows `connected` + heartbeat
// age / in-flight / labels; an unknown/offline worker shows `disconnected`.
func TestListRunnersWorker(t *testing.T) {
	// Pin the clock so heartbeat_age_ms is deterministic.
	const now = int64(1750300005000)
	orig := nowMillis
	nowMillis = func() int64 { return now }
	defer func() { nowMillis = orig }()

	runnersCfg := map[string]config.RunnerConfig{
		"r-online":  {Type: "worker", WorkerID: "w-online"},
		"r-offline": {Type: "worker", WorkerID: "w-offline"},
	}
	workers := fakeWorkers{
		"w-online": {Connected: true, LastHeartbeat: now - 1200, InFlight: 2, Labels: []string{"gpu", "linux"}},
	}
	rows := byName(listRunners(t, newRunnersServer(t, runnersCfg, nil, workers)))

	on := rows["r-online"]
	if on.Type != "worker" || on.Status != "connected" || on.WorkerID != "w-online" || on.Worker == nil {
		t.Fatalf("online worker row wrong: %+v / worker %+v", on, on.Worker)
	}
	if on.Worker.HeartbeatAgeMS != 1200 || on.Worker.InFlight != 2 || len(on.Worker.Labels) != 2 {
		t.Fatalf("online worker detail wrong: %+v", on.Worker)
	}
	off := rows["r-offline"]
	if off.Status != "disconnected" || off.Worker != nil {
		t.Fatalf("offline worker row wrong: %+v", off)
	}
}

// TestListRunnersWorkerNoRegistry: with a nil workers registry every worker row
// is `unknown` (P3/registry not wired).
func TestListRunnersWorkerNoRegistry(t *testing.T) {
	runnersCfg := map[string]config.RunnerConfig{
		"r-w": {Type: "worker", WorkerID: "w1"},
	}
	rows := byName(listRunners(t, newRunnersServer(t, runnersCfg, nil, nil)))
	if rows["r-w"].Status != "unknown" {
		t.Fatalf("nil registry should yield unknown, got %+v", rows["r-w"])
	}
}

// TestListRunnersLocalFirstAndStable: local is always first; the rest are sorted
// by name for a deterministic response regardless of config map order.
func TestListRunnersLocalFirstAndStable(t *testing.T) {
	runnersCfg := map[string]config.RunnerConfig{
		"zeta":  {Type: "peer-http", BaseURL: "https://z:8765"},
		"alpha": {Type: "worker", WorkerID: "a"},
	}
	rows := listRunners(t, newRunnersServer(t, runnersCfg, nil, nil))
	if len(rows) != 3 || rows[0].Name != "local" {
		t.Fatalf("local must be first row: %+v", rows)
	}
	if rows[1].Name != "alpha" || rows[2].Name != "zeta" {
		t.Fatalf("non-local rows not name-sorted: %+v", rows)
	}
}
