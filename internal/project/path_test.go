package project

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

func TestSafeJoin(t *testing.T) {
	host := t.TempDir()
	// create a nested subdir that exists for the positive case
	sub := filepath.Join(host, "tools", "gofer")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	proj := config.ProjectConfig{HostPath: host}

	tests := []struct {
		name    string
		cwd     string
		wantErr bool
		wantAbs string // expected resolved abs (only checked when !wantErr)
	}{
		{"dot", ".", false, host},
		{"empty", "", false, host},
		{"nested subdir", "tools/gofer", false, sub},
		{"parent escape", "..", true, ""},
		{"parent other escape", "../other", true, ""},
		{"windows drive", "D:\\x", true, ""},
		{"windows drive fwd", "C:/y", true, ""},
		{"backslash root", "\\x", true, ""},
		{"clean escape via dots", "tools/../../etc", true, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SafeJoin(proj.HostPath, tc.cwd)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for cwd=%q, got %q", tc.cwd, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for cwd=%q: %v", tc.cwd, err)
			}
			wantAbs, _ := filepath.Abs(tc.wantAbs)
			if got != wantAbs {
				t.Errorf("cwd=%q got %q want %q", tc.cwd, got, wantAbs)
			}
		})
	}
}

func TestResultBaseDirBranches(t *testing.T) {
	host := t.TempDir()
	proj := config.ProjectConfig{
		HostPath:       host,
		ExchangeSubdir: "tmp",
		ResultSubdir:   "gofer",
	}

	// Branch 1: storage.root unset -> <host>/tmp/gofer
	cfg := &config.Config{}
	cfg.Storage.DefaultExchangeSubdir = "tmp"
	cfg.Storage.DefaultResultSubdir = "gofer"
	base, err := ResultBaseDir(cfg, "proj1", proj)
	if err != nil {
		t.Fatal(err)
	}
	wantHost := filepath.Join(host, "tmp", "gofer")
	if base != wantHost {
		t.Errorf("no-root base = %q, want %q", base, wantHost)
	}

	// JobResultDir adds the job id.
	jr, err := JobResultDir(cfg, "proj1", proj, "job-123")
	if err != nil {
		t.Fatal(err)
	}
	if jr != filepath.Join(wantHost, "job-123") {
		t.Errorf("job result dir = %q", jr)
	}

	// Branch 2: storage.root set -> <root>/<projKey>
	root := t.TempDir()
	cfg.Storage.Root = root
	base, err = ResultBaseDir(cfg, "proj1", proj)
	if err != nil {
		t.Fatal(err)
	}
	wantRoot := filepath.Join(root, "proj1")
	if base != wantRoot {
		t.Errorf("root base = %q, want %q", base, wantRoot)
	}
}

func TestExchangeDirDefaults(t *testing.T) {
	host := t.TempDir()
	proj := config.ProjectConfig{HostPath: host} // no exchange_subdir set
	cfg := &config.Config{}
	cfg.Storage.DefaultExchangeSubdir = "tmp"
	cfg.Storage.DefaultResultSubdir = "gofer"

	ex, err := ExchangeDir(cfg, proj)
	if err != nil {
		t.Fatal(err)
	}
	if ex != filepath.Join(host, "tmp") {
		t.Errorf("exchange dir = %q", ex)
	}
}

func TestRegistryGetUnknown(t *testing.T) {
	cfg := &config.Config{Projects: map[string]config.ProjectConfig{}}
	reg := NewRegistry(cfg, "")
	if _, err := reg.Get("nope"); err == nil {
		t.Fatal("expected error for unknown project_key")
	}
}

func TestRegistryAddRemoveList(t *testing.T) {
	dir := t.TempDir()
	host := t.TempDir()
	cfgPath := filepath.Join(dir, "cfg.yaml")
	cfg := &config.Config{Projects: map[string]config.ProjectConfig{}}
	reg := NewRegistry(cfg, cfgPath)

	if err := reg.Add("a", config.ProjectConfig{HostPath: host}, false); err != nil {
		t.Fatal(err)
	}
	// duplicate without force fails
	if err := reg.Add("a", config.ProjectConfig{HostPath: host}, false); err == nil {
		t.Fatal("expected duplicate add to fail without force")
	}
	// with force succeeds
	if err := reg.Add("a", config.ProjectConfig{HostPath: host}, true); err != nil {
		t.Fatal(err)
	}
	if err := reg.Add("b", config.ProjectConfig{HostPath: host}, false); err != nil {
		t.Fatal(err)
	}
	keys := reg.List()
	if len(keys) != 2 || keys[0] != "a" || keys[1] != "b" {
		t.Errorf("list = %v", keys)
	}
	if err := reg.Remove("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Get("a"); err == nil {
		t.Error("a should be removed")
	}
	// removing unknown fails
	if err := reg.Remove("zzz"); err == nil {
		t.Error("expected error removing unknown project")
	}
}

func TestRegistryValidate(t *testing.T) {
	host := t.TempDir()
	cfg := &config.Config{
		Projects: map[string]config.ProjectConfig{
			"ok": {
				HostPath:       host,
				ExchangeSubdir: "tmp",
				ResultSubdir:   "gofer",
				DefaultAgent:   "exec",
				AllowedAgents:  []string{"exec"},
				AllowedRunners: []string{"local"},
			},
		},
		Agents:  map[string]config.AgentConfig{"exec": {Type: "exec"}},
		Runners: map[string]config.RunnerConfig{},
	}
	reg := NewRegistry(cfg, "")
	results, ok, err := reg.Validate("ok")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Errorf("expected valid, results: %+v", results)
	}

	// missing agent reference fails
	cfg.Projects["bad"] = config.ProjectConfig{
		HostPath:     host,
		DefaultAgent: "ghost",
	}
	_, ok, err = reg.Validate("bad")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected validation to fail for missing default_agent")
	}

	// non-existent exec_path (= host_path by default) fails
	cfg.Projects["nohost"] = config.ProjectConfig{HostPath: filepath.Join(host, "does-not-exist")}
	results, ok, err = reg.Validate("nohost")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Errorf("expected fail for missing exec_path: %+v", results)
	}
	// ensure the exec_path check is the failing one
	found := false
	for _, r := range results {
		if r.Name == "exec_path" && !r.OK {
			found = true
		}
	}
	if !found {
		t.Error("expected exec_path check to fail")
	}
}

// TestRegistryValidateWorkerOnlySkipsLocalFS: a project whose allowed_runners
// has no "local" (worker-only, e.g. dispatched to a remote worker) must skip
// the local FS checks — and above all must NOT MkdirAll the project tree on
// this machine (bug: web project add auto-created worker-only host_path dirs
// on the server node).
func TestRegistryValidateWorkerOnlySkipsLocalFS(t *testing.T) {
	base := t.TempDir()
	host := filepath.Join(base, "only-on-worker") // does NOT exist locally
	cfg := &config.Config{
		Projects: map[string]config.ProjectConfig{
			"remote": {
				HostPath:       host,
				AllowedRunners: []string{"w-remote"},
			},
		},
		Agents: map[string]config.AgentConfig{},
		Runners: map[string]config.RunnerConfig{
			"w-remote": {Type: "worker", WorkerID: "w-remote"},
		},
	}
	reg := NewRegistry(cfg, "")
	results, ok, err := reg.Validate("remote")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Errorf("worker-only project should validate OK without local FS, results: %+v", results)
	}
	for _, r := range results {
		if r.Name == "exec_path" || r.Name == "exchange_dir" || r.Name == "result_dir" {
			t.Errorf("local FS check %q must be skipped for worker-only project", r.Name)
		}
	}
	// The probe side effect must not have fabricated the project tree locally.
	if _, statErr := os.Stat(host); !os.IsNotExist(statErr) {
		t.Errorf("host_path %q must not be created on this node (stat err=%v)", host, statErr)
	}
}

// TestRegistryValidateDefaultAgentInAllowed covers D5: when allowed_agents is
// restricted, default_agent must be a member, else the default_agent check FAILs
// (prevents an overlay from borrowing default_agent to bypass admission).
func TestRegistryValidateDefaultAgentInAllowed(t *testing.T) {
	host := t.TempDir()
	cfg := &config.Config{
		Projects: map[string]config.ProjectConfig{},
		Agents: map[string]config.AgentConfig{
			"claude": {Type: "exec"},
			"codex":  {Type: "exec"},
		},
		Runners: map[string]config.RunnerConfig{},
	}
	reg := NewRegistry(cfg, "")

	defAgentResult := func(results []CheckResult) (CheckResult, bool) {
		for _, r := range results {
			if r.Name == "default_agent" {
				return r, true
			}
		}
		return CheckResult{}, false
	}

	// default_agent not in allowed_agents → default_agent check FAIL (D5).
	cfg.Projects["deny"] = config.ProjectConfig{
		HostPath:      host,
		DefaultAgent:  "codex",
		AllowedAgents: []string{"claude"},
	}
	results, ok, err := reg.Validate("deny")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Errorf("expected validation to fail when default_agent not in allowed_agents: %+v", results)
	}
	if r, found := defAgentResult(results); !found || r.OK {
		t.Errorf("expected default_agent check to fail, got %+v (found=%v)", r, found)
	}

	// default_agent in allowed_agents → default_agent check PASS.
	cfg.Projects["allow"] = config.ProjectConfig{
		HostPath:      host,
		DefaultAgent:  "claude",
		AllowedAgents: []string{"claude"},
	}
	results, _, err = reg.Validate("allow")
	if err != nil {
		t.Fatal(err)
	}
	if r, found := defAgentResult(results); !found || !r.OK {
		t.Errorf("expected default_agent check to pass, got %+v (found=%v)", r, found)
	}
}
