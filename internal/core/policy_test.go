package core

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/wsproto"
)

// matrixCfg is the reachability fixture: three worker runners (two pinned, one
// pool), one local + one peer-http runner, and projects covering every branch of
// projectReachesWorker.
func matrixCfg() *config.Config {
	captureOff := false
	return &config.Config{
		Runners: map[string]config.RunnerConfig{
			"run-a": {Type: "worker", WorkerID: "w-a"}, // pinned to w-a
			"run-b": {Type: "worker", WorkerID: "w-b"}, // pinned to w-b
			"pool":  {Type: "worker"},                  // WorkerID unset → pool (all-push)
			"local": {Type: "local"},                   // not a worker path
			"peer":  {Type: "peer-http"},               // not a worker path
		},
		Projects: map[string]config.ProjectConfig{
			"pin-a":  {HostPath: "/srv/pin-a", AllowedRunners: []string{"run-a"}},
			"pin-b":  {HostPath: "/srv/pin-b", AllowedRunners: []string{"run-b"}},
			"pooled": {HostPath: "/srv/pooled", AllowedRunners: []string{"pool"}, MaxConcurrentJobs: 3, CaptureDiff: &captureOff},
			"multi":  {HostPath: "/srv/multi", AllowedRunners: []string{"run-a", "pool"}}, // reachable via pool from any worker
			// —— Q8 / ignore branches: NONE of these reach any worker ——
			"empty":     {HostPath: "/srv/empty", AllowedRunners: []string{}}, // 🔴 Q8: empty ≠ wildcard
			"nilrun":    {HostPath: "/srv/nilrun"},                            // AllowedRunners nil → same as empty
			"localonly": {HostPath: "/srv/localonly", AllowedRunners: []string{"local"}},
			"peeronly":  {HostPath: "/srv/peeronly", AllowedRunners: []string{"peer"}},
			"ghost":     {HostPath: "/srv/ghost", AllowedRunners: []string{"nosuch"}}, // undefined runner key
			// —— whitelist passthrough (non-intersection, non-nil) ——
			"ag-nil": {HostPath: "/srv/ag-nil", AllowedRunners: []string{"pool"}}, // AllowedAgents nil → []
			"ag-set": {HostPath: "/srv/ag-set", AllowedRunners: []string{"pool"},
				AllowedAgents: []string{"claude", "tty-codex"}, InteractiveAllowedAgents: []string{"claude"}},
		},
	}
}

// policyKeys returns the sorted project keys of a Policy.
func policyKeys(pol wsproto.Policy) []string {
	ks := make([]string, 0, len(pol.Projects))
	for _, p := range pol.Projects {
		ks = append(ks, p.Key)
	}
	sort.Strings(ks)
	return ks
}

// policyByKey indexes a Policy's projects by key.
func policyByKey(pol wsproto.Policy) map[string]wsproto.PolicyProject {
	m := make(map[string]wsproto.PolicyProject, len(pol.Projects))
	for _, p := range pol.Projects {
		m[p.Key] = p
	}
	return m
}

// TestComputePolicyReachabilityMatrix: a project pinned to w-a must NOT appear in
// w-b's Policy (and vice-versa); a pool runner's project reaches BOTH workers; and
// the non-worker / undefined-runner / empty-allowed_runners projects reach NEITHER.
func TestComputePolicyReachabilityMatrix(t *testing.T) {
	cfg := matrixCfg()

	wantA := []string{"ag-nil", "ag-set", "multi", "pin-a", "pooled"}
	wantB := []string{"ag-nil", "ag-set", "multi", "pin-b", "pooled"}

	if got := policyKeys(computePolicy(cfg, "w-a", 7)); !reflect.DeepEqual(got, wantA) {
		t.Errorf("w-a policy keys = %v, want %v", got, wantA)
	}
	if got := policyKeys(computePolicy(cfg, "w-b", 7)); !reflect.DeepEqual(got, wantB) {
		t.Errorf("w-b policy keys = %v, want %v", got, wantB)
	}
	// pin-a on w-b would be the classic cross-leak — assert it explicitly.
	if _, leaked := policyByKey(computePolicy(cfg, "w-b", 7))["pin-a"]; leaked {
		t.Error("pin-a (pinned to w-a) leaked into w-b's Policy")
	}
	// A pool project reaches an ARBITRARY third worker id too (server can't enumerate
	// pool candidates → conservative all-push).
	if _, ok := policyByKey(computePolicy(cfg, "w-zzz", 7))["pooled"]; !ok {
		t.Error("pool project must reach an arbitrary worker (conservative all-push)")
	}
	// Rev rides through verbatim.
	if rev := computePolicy(cfg, "w-a", 7).Rev; rev != 7 {
		t.Errorf("Policy.Rev = %d, want 7", rev)
	}
}

// TestComputePolicyEmptyAllowedRunnersNeverPushed is the 🔴 Q8 reverse test (验收
// 12): a project with empty (or nil) allowed_runners must be pushed to NO worker,
// on EVERY worker id — empty is not a wildcard. The non-worker / undefined-runner
// projects (allowed_runners:[local] / [peer] / [nosuch]) likewise reach nobody.
//
// 先证伪: make projectReachesWorker return true when AllowedRunners is empty
// ("空=全推") and "empty"/"nilrun" appear in every worker's Policy → this test reddens.
func TestComputePolicyEmptyAllowedRunnersNeverPushed(t *testing.T) {
	cfg := matrixCfg()
	neverPushed := []string{"empty", "nilrun", "localonly", "peeronly", "ghost"}

	for _, wid := range []string{"w-a", "w-b", "w-c-unknown"} {
		got := policyByKey(computePolicy(cfg, wid, 1))
		for _, k := range neverPushed {
			if _, present := got[k]; present {
				t.Errorf("worker %q: project %q must never be pushed (allowed_runners reaches no worker), but it was", wid, k)
			}
		}
	}

	// allowed_runners:[local] specifically → not pushed to any worker (spec line 623).
	if _, present := policyByKey(computePolicy(cfg, "w-a", 1))["localonly"]; present {
		t.Error("allowed_runners:[local] must not be pushed to any worker")
	}
}

// TestComputePolicyWhitelistNonNilNoIntersection: AllowedAgents /
// InteractiveAllowedAgents are always NON-nil (wire `[]`, never null) and are passed
// through VERBATIM — no intersection with anything (D6). An unset whitelist becomes an
// empty non-nil slice; a set one is byte-for-byte preserved.
func TestComputePolicyWhitelistNonNilNoIntersection(t *testing.T) {
	byKey := policyByKey(computePolicy(matrixCfg(), "w-a", 1))

	nilAg := byKey["ag-nil"]
	if nilAg.AllowedAgents == nil || len(nilAg.AllowedAgents) != 0 {
		t.Errorf("ag-nil AllowedAgents = %#v, want non-nil empty slice", nilAg.AllowedAgents)
	}
	if nilAg.InteractiveAllowedAgents == nil || len(nilAg.InteractiveAllowedAgents) != 0 {
		t.Errorf("ag-nil InteractiveAllowedAgents = %#v, want non-nil empty slice", nilAg.InteractiveAllowedAgents)
	}

	setAg := byKey["ag-set"]
	if !reflect.DeepEqual(setAg.AllowedAgents, []string{"claude", "tty-codex"}) {
		t.Errorf("ag-set AllowedAgents = %v, want verbatim [claude tty-codex] (no intersection)", setAg.AllowedAgents)
	}
	if !reflect.DeepEqual(setAg.InteractiveAllowedAgents, []string{"claude"}) {
		t.Errorf("ag-set InteractiveAllowedAgents = %v, want [claude]", setAg.InteractiveAllowedAgents)
	}

	// Wire assertion: an empty whitelist marshals to [] (not null) so a downstream that
	// parses JSON never sees null for a project computePolicy produced.
	raw, err := json.Marshal(nilAg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	js := string(raw)
	if !strings.Contains(js, `"allowed_agents":[]`) {
		t.Errorf(`wire form must contain "allowed_agents":[]; got %s`, js)
	}
	if strings.Contains(js, `"allowed_agents":null`) {
		t.Errorf(`wire form must NOT contain "allowed_agents":null; got %s`, js)
	}
}

// TestComputePolicyH2Fields: MaxConcurrentJobs / CaptureDiff ride along verbatim; a
// project that sets neither leaves them at their "unset" wire values (0 / nil).
func TestComputePolicyH2Fields(t *testing.T) {
	byKey := policyByKey(computePolicy(matrixCfg(), "w-a", 1))

	pooled := byKey["pooled"]
	if pooled.MaxConcurrentJobs != 3 {
		t.Errorf("pooled MaxConcurrentJobs = %d, want 3", pooled.MaxConcurrentJobs)
	}
	if pooled.CaptureDiff == nil || *pooled.CaptureDiff != false {
		t.Errorf("pooled CaptureDiff = %v, want explicit false", pooled.CaptureDiff)
	}

	// A project without H2 fields set: unlimited (0) + default-on (nil).
	multi := byKey["multi"]
	if multi.MaxConcurrentJobs != 0 {
		t.Errorf("multi MaxConcurrentJobs = %d, want 0 (unset = unlimited)", multi.MaxConcurrentJobs)
	}
	if multi.CaptureDiff != nil {
		t.Errorf("multi CaptureDiff = %v, want nil (unset = default-on)", multi.CaptureDiff)
	}

	// HostPath is the server host_path verbatim; ContainerPath is never on the wire
	// (PolicyProject has no such field — structurally guaranteed).
	if byKey["pin-a"].HostPath != "/srv/pin-a" {
		t.Errorf("pin-a HostPath = %q, want /srv/pin-a", byKey["pin-a"].HostPath)
	}
}

// TestComputePolicyDeterministicAndEmpty: output order is deterministic (sorted keys)
// and an empty cfg / no-projects yields a NON-nil empty Projects slice (wire `[]`).
func TestComputePolicyDeterministicAndEmpty(t *testing.T) {
	got := policyKeys(computePolicy(matrixCfg(), "w-a", 1))
	if !sort.StringsAreSorted(got) {
		t.Errorf("policy keys not sorted: %v", got)
	}

	empty := computePolicy(&config.Config{}, "w-a", 5)
	if empty.Projects == nil {
		t.Error("empty Policy.Projects must be non-nil ([]), got nil")
	}
	if len(empty.Projects) != 0 {
		t.Errorf("empty cfg → %d projects, want 0", len(empty.Projects))
	}
	if empty.Rev != 5 {
		t.Errorf("empty Policy.Rev = %d, want 5", empty.Rev)
	}

	if nilCfg := computePolicy(nil, "w-a", 9); len(nilCfg.Projects) != 0 || nilCfg.Rev != 9 {
		t.Errorf("nil cfg → %+v, want {Rev:9, Projects:[]}", nilCfg)
	}
}

// TestCorePolicySourceOneAtomicRead proves PolicyFor derives cfg AND rev from a
// SINGLE Snapshot() (T1-A): the pushed Rev always matches the cfg content it was
// computed from. A config write bumps rev by 1 and PolicyFor immediately reflects the
// NEW (cfg, rev) as one consistent generation — never a torn (old cfg, new rev) pair.
func TestCorePolicySourceOneAtomicRead(t *testing.T) {
	host := t.TempDir()
	root := t.TempDir()
	cfgPath := filepath.Join(t.TempDir(), "gofer.yaml")
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root, DBPath: filepath.Join(root, "gofer.db")},
		Runners: map[string]config.RunnerConfig{
			"pool": {Type: "worker"}, // pool → reaches every worker
		},
		Projects: map[string]config.ProjectConfig{
			"seed": {HostPath: host, AllowedRunners: []string{"pool"}},
		},
	}
	cr, err := Build(cfg, WithAgentDetector(agent.NoopDetector{}), WithConfigPath(cfgPath))
	if err != nil {
		t.Fatalf("build core: %v", err)
	}
	t.Cleanup(func() { _ = cr.Close() })

	src := &corePolicySource{core: cr}

	snap0 := cr.Snapshot()
	pol0, ok := src.PolicyFor("w-x")
	if !ok {
		t.Fatal("PolicyFor ok = false, want true (empty policy is still a legitimate push)")
	}
	if pol0.Rev != snap0.Rev {
		t.Errorf("pol0.Rev = %d, want %d (rev must match the snapshot it was computed from)", pol0.Rev, snap0.Rev)
	}
	// PolicyFor must equal computePolicy over the SAME snapshot (no extra reads).
	if want := computePolicy(snap0.Cfg, "w-x", snap0.Rev); !reflect.DeepEqual(pol0, want) {
		t.Errorf("PolicyFor(w-x) = %+v, want %+v", pol0, want)
	}
	if _, ok := policyByKey(pol0)["seed"]; !ok {
		t.Error("seed project (pool-reachable) missing from w-x policy")
	}

	// A write bumps the generation; PolicyFor returns the NEW rev AND the new content
	// as one consistent read.
	if err := cr.Projects.Add("added", config.ProjectConfig{HostPath: host, AllowedRunners: []string{"pool"}}, false); err != nil {
		t.Fatalf("add project: %v", err)
	}
	snap1 := cr.Snapshot()
	if snap1.Rev != snap0.Rev+1 {
		t.Fatalf("rev after add = %d, want %d", snap1.Rev, snap0.Rev+1)
	}
	pol1, _ := src.PolicyFor("w-x")
	if pol1.Rev != snap1.Rev {
		t.Errorf("pol1.Rev = %d, want %d", pol1.Rev, snap1.Rev)
	}
	byKey := policyByKey(pol1)
	if _, ok := byKey["seed"]; !ok {
		t.Error("seed missing after add")
	}
	if _, ok := byKey["added"]; !ok {
		t.Error("added project (bumped rev) missing → PolicyFor did not read the latest snapshot atomically")
	}
}
