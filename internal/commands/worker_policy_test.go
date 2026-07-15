package commands

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/core"
	"github.com/inhere/gofer/internal/worker"
	"github.com/inhere/gofer/internal/wsproto"
)

// policyWC builds a POLICY-mode worker config: one root mapping /srv → <hostRoot>, with
// the given guards. Projects intentionally stays empty (POLICY ignores local projects).
func policyWC(hostRoot string, guards config.WorkerGuards) *config.WorkerConfig {
	return &config.WorkerConfig{
		WorkerID:      "w1",
		ServerLink:    config.WorkerServerLink{URLs: []string{"ws://hub"}},
		Roots:         []config.WorkerRoot{{From: "/srv", To: hostRoot}},
		Guards:        guards,
		MaxConcurrent: 4,
		Labels:        []string{"linux"},
	}
}

func boolPtr(b bool) *bool { return &b }

// TestWorkerModeOf (verification 2): the (roots, projects) matrix maps to the three
// modes. This is the wall against a well-meaning implementer flipping the switch back
// to a protocol-version gate.
func TestWorkerModeOf(t *testing.T) {
	roots := []config.WorkerRoot{{From: "/srv", To: "/host"}}
	projs := map[string]config.ProjectConfig{"a": {HostPath: "/host/a"}}
	cases := []struct {
		name string
		wc   config.WorkerConfig
		want workerMode
	}{
		{"legacy: projects, no roots", config.WorkerConfig{Projects: projs}, modeLegacy},
		{"policy: roots (projects ignored)", config.WorkerConfig{Roots: roots, Projects: projs}, modePolicy},
		{"policy: roots only", config.WorkerConfig{Roots: roots}, modePolicy},
		{"empty: neither", config.WorkerConfig{}, modeEmpty},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := workerModeOf(&c.wc); got != c.want {
				t.Fatalf("workerModeOf = %d, want %d", got, c.want)
			}
		})
	}
}

// TestInitialWorkerConfigSource (verification 2): the startup config's project source
// per mode — LEGACY from wc.Projects, EMPTY none, POLICY none-without-cache.
func TestInitialWorkerConfigSource(t *testing.T) {
	t.Setenv(config.EnvConfigDir, t.TempDir())

	legacy := &config.WorkerConfig{WorkerID: "w1", Projects: map[string]config.ProjectConfig{"a": {HostPath: "/x/a"}, "b": {HostPath: "/x/b"}}}
	cfg, projs, ip := initialWorkerConfig(modeLegacy, legacy, "")
	if len(cfg.Projects) != 2 || ip != nil {
		t.Fatalf("legacy: cfg.Projects=%d ip=%v, want 2 local projects and no seed policy", len(cfg.Projects), ip)
	}
	if len(projs) != 2 {
		t.Fatalf("legacy advertised projects = %v, want the 2 local ones", projs)
	}

	pol := policyWC("/host", config.WorkerGuards{})
	cfg, projs, ip = initialWorkerConfig(modePolicy, pol, filepath.Join(t.TempDir(), "missing.json"))
	if len(cfg.Projects) != 0 || projs != nil || ip != nil {
		t.Fatalf("policy cold start (no cache): cfg.Projects=%d projs=%v ip=%v, want all empty", len(cfg.Projects), projs, ip)
	}

	empty := &config.WorkerConfig{WorkerID: "w1"}
	cfg, projs, _ = initialWorkerConfig(modeEmpty, empty, "")
	if len(cfg.Projects) != 0 || projs != nil {
		t.Fatalf("empty: cfg.Projects=%d projs=%v, want none", len(cfg.Projects), projs)
	}
}

// TestInitialWorkerConfigRecoversCache (verification 9, cache cold start): a POLICY
// worker cold-starting with a valid cache projects the cached policy at boot (so its
// projects survive a restart with an unreachable server) and seeds it as the LKG.
func TestInitialWorkerConfigRecoversCache(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.EnvConfigDir, dir)
	hostRoot := t.TempDir()
	cachePath := workerPolicyCachePath("w1")
	// A cache holding one project whose host_path maps under the worker's root.
	cached := wsproto.Policy{Rev: 12, Projects: []wsproto.PolicyProject{
		{Key: "svc", HostPath: "/srv/svc", AllowedAgents: []string{"claude"}},
	}}
	if err := worker.WritePolicyCacheFile(cachePath, "w1", &cached, 1); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	cfg, projs, ip := initialWorkerConfig(modePolicy, policyWC(hostRoot, config.WorkerGuards{}), cachePath)
	if ip == nil || ip.Rev != 12 {
		t.Fatalf("cold start must seed the cached policy as LKG, got %+v", ip)
	}
	if _, ok := cfg.Projects["svc"]; !ok {
		t.Fatalf("cold start must project the cached project, got %v", cfg.Projects)
	}
	if !slices.Contains(projs, "svc") {
		t.Fatalf("cold start advertised projects = %v, want svc", projs)
	}
	if got := cfg.Projects["svc"].HostPath; got != filepath.Join(hostRoot, "svc") {
		t.Fatalf("recovered host_path = %q, want it mapped under the root", got)
	}
}

// TestProjectPolicyRejectsUnmappablePath (verification 8): a project whose host_path is
// outside every root is REJECTED, never admitted with an empty HostPath (which
// filepath.Abs would resolve to the process CWD).
//
// Falsification: make projectPolicy emit a ProjectConfig on a MapRoot miss. The rejected
// key then appears in cfg.Projects with HostPath=="" and the assertions below fail.
func TestProjectPolicyRejectsUnmappablePath(t *testing.T) {
	wc := policyWC("/host", config.WorkerGuards{})
	p := wsproto.Policy{Rev: 1, Projects: []wsproto.PolicyProject{
		{Key: "ok", HostPath: "/srv/ok"},
		{Key: "bad", HostPath: "/elsewhere/bad"}, // no root covers /elsewhere
	}}
	cfg, rejected := projectPolicy(wc, p)

	if _, ok := cfg.Projects["bad"]; ok {
		t.Fatalf("unmappable project must NOT be admitted: %v", cfg.Projects)
	}
	if _, ok := cfg.Projects["ok"]; !ok {
		t.Fatalf("mappable project must be admitted: %v", cfg.Projects)
	}
	foundReject := false
	for _, r := range rejected {
		if r.Key == "bad" && r.Reason == "path_outside_roots" {
			foundReject = true
		}
	}
	if !foundReject {
		t.Fatalf("rejected = %v, want {bad, path_outside_roots}", rejected)
	}
	for k, pc := range cfg.Projects {
		if pc.HostPath == "" {
			t.Fatalf("project %q has an empty HostPath (would resolve to the process CWD)", k)
		}
	}
}

// TestProjectPolicyWhitelistVerbatim (verification 13): allowed_agents is passed through
// EXACTLY — never intersected with the worker's installed agents (which would silently
// open all agents when the intersection is empty).
//
// Falsification: intersect AllowedAgents with cfg.Agents in projectPolicy. With codex not
// installed here, the list would drop tty-codex (or empty out), and the verbatim assertion
// fails.
func TestProjectPolicyWhitelistVerbatim(t *testing.T) {
	wc := policyWC("/host", config.WorkerGuards{})
	p := wsproto.Policy{Rev: 1, Projects: []wsproto.PolicyProject{{
		Key: "svc", HostPath: "/srv/svc",
		AllowedAgents:            []string{"claude", "tty-codex"},
		InteractiveAllowedAgents: []string{"tty-codex"},
	}}}
	cfg, _ := projectPolicy(wc, p)
	got := cfg.Projects["svc"].AllowedAgents
	if !reflect.DeepEqual(got, []string{"claude", "tty-codex"}) {
		t.Fatalf("allowed_agents = %v, want verbatim [claude tty-codex] (no intersection)", got)
	}
	if !reflect.DeepEqual(cfg.Projects["svc"].InteractiveAllowedAgents, []string{"tty-codex"}) {
		t.Fatalf("interactive_allowed_agents = %v, want verbatim [tty-codex]", cfg.Projects["svc"].InteractiveAllowedAgents)
	}
}

// TestProjectPolicyH2Fields (verification 14): max_concurrent_jobs and capture_diff are
// projected verbatim — dropping them would silently mean unlimited concurrency / diff-on.
//
// Falsification: stop projecting MaxConcurrentJobs (→ 0 = unlimited) or CaptureDiff (→ nil
// = default-on); the assertions below fail.
func TestProjectPolicyH2Fields(t *testing.T) {
	wc := policyWC("/host", config.WorkerGuards{})
	p := wsproto.Policy{Rev: 1, Projects: []wsproto.PolicyProject{{
		Key: "svc", HostPath: "/srv/svc",
		MaxConcurrentJobs: 1,
		CaptureDiff:       boolPtr(false),
	}}}
	cfg, _ := projectPolicy(wc, p)
	pc := cfg.Projects["svc"]
	if pc.MaxConcurrentJobs != 1 {
		t.Fatalf("max_concurrent_jobs = %d, want 1 (dropping it means unlimited)", pc.MaxConcurrentJobs)
	}
	if pc.CaptureDiff == nil || *pc.CaptureDiff != false {
		t.Fatalf("capture_diff = %v, want explicit false (dropping it defaults diff on)", pc.CaptureDiff)
	}
}

// TestProjectPolicyGuardsOnlyTighten (verification 11): a worker guard set to false
// overrides a policy's allow_exec: true, and the interactive guard clears the interactive
// allowlist. The exec-gated project is flagged in Degraded (diagnostic).
func TestProjectPolicyGuardsOnlyTighten(t *testing.T) {
	wc := policyWC("/host", config.WorkerGuards{AllowExec: boolPtr(false), AllowInteractive: boolPtr(false)})
	p := wsproto.Policy{Rev: 1, Projects: []wsproto.PolicyProject{{
		Key: "svc", HostPath: "/srv/svc",
		AllowExec:                true,
		InteractiveAllowedAgents: []string{"tty-claude"},
	}}}
	cfg, _ := projectPolicy(wc, p)
	pc := cfg.Projects["svc"]
	if pc.AllowExec {
		t.Fatal("worker allow_exec:false must override the policy's allow_exec:true")
	}
	if len(pc.InteractiveAllowedAgents) != 0 {
		t.Fatalf("allow_interactive:false must clear the interactive allowlist, got %v", pc.InteractiveAllowedAgents)
	}
	deg := diagnosePolicy(cfg, p, wc, nil)
	if !slices.ContainsFunc(deg, func(d wsproto.AppliedDegrade) bool { return d.Key == "svc" && d.Gate == "exec" }) {
		t.Fatalf("degraded = %v, want an exec gate for svc", deg)
	}
}

// TestProjectPolicyCompleteSnapshotReplace (verification 26): each projection is a
// COMPLETE snapshot — a shorter policy revokes the missing project, and an empty policy
// revokes everything. There is no merge that keeps a key the server dropped.
//
// Falsification: merge the projection into wc.Projects (or carry keys forward). The
// "B is gone" / "empty ⇒ zero" assertions then fail.
func TestProjectPolicyCompleteSnapshotReplace(t *testing.T) {
	wc := policyWC("/host", config.WorkerGuards{})
	// A leftover LOCAL project (POLICY mode must ignore it): the projection replaces the
	// whole set, so it must never survive into the projected cfg. If projectPolicy merged
	// the policy into wc.Projects (instead of replacing), this key would leak through.
	wc.Projects = map[string]config.ProjectConfig{"leftover": {HostPath: "/host/leftover"}}

	full, _ := projectPolicy(wc, wsproto.Policy{Rev: 1, Projects: []wsproto.PolicyProject{
		{Key: "A", HostPath: "/srv/A"}, {Key: "B", HostPath: "/srv/B"},
	}})
	if len(full.Projects) != 2 {
		t.Fatalf("first projection = %v, want {A,B} (local leftover excluded)", keysOf(full.Projects))
	}
	if _, ok := full.Projects["leftover"]; ok {
		t.Fatalf("POLICY projection must ignore the local leftover project: %v", keysOf(full.Projects))
	}

	dropB, _ := projectPolicy(wc, wsproto.Policy{Rev: 2, Projects: []wsproto.PolicyProject{
		{Key: "A", HostPath: "/srv/A"},
	}})
	if _, ok := dropB.Projects["B"]; ok {
		t.Fatalf("a shorter policy must REVOKE B, got %v", keysOf(dropB.Projects))
	}
	if _, ok := dropB.Projects["A"]; !ok {
		t.Fatalf("A must remain, got %v", keysOf(dropB.Projects))
	}

	empty, _ := projectPolicy(wc, wsproto.Policy{Rev: 3})
	if len(empty.Projects) != 0 {
		t.Fatalf("an empty policy must revoke everything, got %v", keysOf(empty.Projects))
	}
}

// TestReloadSeamNoopKeepsRunningProjects (verification 9, LEGACY→roots→SIGHUP no-op):
// a POLICY-mode SIGHUP with no last-known-good (p == nil) must KEEP the running project
// set — never rebuild an empty config and ReloadWith it.
//
// Falsification: replace the no-op branch with `cfg,_ := projectPolicy(wc, empty);
// cr.ReloadWith(cfg)`. The running projects are then wiped to zero and the assertion fails.
func TestReloadSeamNoopKeepsRunningProjects(t *testing.T) {
	t.Setenv(config.EnvConfigDir, t.TempDir())
	// A worker.yaml now in POLICY mode (roots added).
	path := filepath.Join(t.TempDir(), "worker.yaml")
	if err := os.WriteFile(path, []byte("worker_id: w1\nserver_link:\n  urls: [ws://hub]\nroots:\n  - {from: /srv, to: /host}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The RUNNING config still has the projects it had while LEGACY.
	active := &config.Config{Projects: map[string]config.ProjectConfig{
		"a": {HostPath: "/host/a", AllowedRunners: []string{"local"}},
		"b": {HostPath: "/host/b", AllowedRunners: []string{"local"}},
	}}
	config.ApplyDefaults(active)
	active.Storage.DBPath = filepath.Join(t.TempDir(), "w.db")
	det := &availabilityRecorder{inner: agent.NoopDetector{}}
	cr, err := core.Build(active, core.WithAgentDetector(det))
	if err != nil {
		t.Fatalf("core.Build: %v", err)
	}
	defer func() { _ = cr.Close() }()

	before := cr.Snapshot().Rev
	out, err := newWorkerReloadFn(cr, det, path, "w1")(nil) // SIGHUP, no LKG
	if err != nil {
		t.Fatalf("seam(nil): %v", err)
	}
	if got := cr.Snapshot().Rev; got != before {
		t.Fatalf("no-op SIGHUP must NOT ReloadWith (Rev changed %d→%d)", before, got)
	}
	if len(cr.Config().Projects) != 2 {
		t.Fatalf("no-op SIGHUP wiped the running projects: %v", keysOf(cr.Config().Projects))
	}
	if len(out.Caps.Projects) != 2 {
		t.Fatalf("no-op SIGHUP caps.Projects = %v, want the 2 running projects kept", out.Caps.Projects)
	}
}

// TestWorkerCapsPolicyProjects (verification 1/T5-F): POLICY caps report the PROJECTED
// cfg.Projects, LEGACY caps report the local wc.Projects.
func TestWorkerCapsPolicyProjects(t *testing.T) {
	wc := policyWC("/host", config.WorkerGuards{})
	cfg, _ := projectPolicy(wc, wsproto.Policy{Rev: 1, Projects: []wsproto.PolicyProject{
		{Key: "svc", HostPath: "/srv/svc"},
	}})
	caps := workerCaps(wc, cfg, nil, mapKeys(cfg.Projects))
	if !slices.Contains(caps.Projects, "svc") || len(caps.Projects) != 1 {
		t.Fatalf("policy caps projects = %v, want [svc] from the projection", caps.Projects)
	}
}

func keysOf(m map[string]config.ProjectConfig) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
