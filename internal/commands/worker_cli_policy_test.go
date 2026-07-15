package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/worker"
	"github.com/inhere/gofer/internal/wsproto"
)

// --- T6 verification 18: `gofer project list` / `gofer config validate` stay sane
// in BOTH the LEGACY and POLICY worker forms (LEGACY verbatim; POLICY reads the
// last-known-good policy cache; a missing cache never panics; the doctor does not
// FAIL a POLICY worker just for having 0 local projects). ---

// writeWorkerYAMLAt writes a worker.yaml body to dir/worker.yaml (the path
// loadWorkerConfig("") auto-discovers under GOFER_CONFIG_DIR) and returns it.
func writeWorkerYAMLAt(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, config.WorkerConfigFileName)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write worker.yaml: %v", err)
	}
	return p
}

// seedPolicyCache writes a last-known-good policy cache for cacheWorkerID at the
// path a running worker would use (workerPolicyCachePath). cacheWorkerID is passed
// separately so a test can seed a FOREIGN worker's cache (worker_id mismatch).
func seedPolicyCache(t *testing.T, cacheWorkerID string, p *wsproto.Policy) {
	t.Helper()
	if err := worker.WritePolicyCacheFile(workerPolicyCachePath(cacheWorkerID), cacheWorkerID, p, 1); err != nil {
		t.Fatalf("seed policy cache: %v", err)
	}
}

const workerLinkYAML = "" +
	"server_link:\n" +
	"  urls: [ws://127.0.0.1:LIVE-PORT/v1/workers/connect]\n" +
	"  token: tok\n"

// TestProjectListLegacyWorkerVerbatim: a LEGACY worker (projects, no roots) lists
// its projects VERBATIM from worker.yaml — the pre-T6 behaviour, unchanged.
func TestProjectListLegacyWorkerVerbatim(t *testing.T) {
	cfgDir, host := t.TempDir(), t.TempDir()
	t.Setenv(config.EnvConfigDir, cfgDir)
	t.Setenv(config.EnvRunMode, config.RunModeWorker)
	writeWorkerYAMLAt(t, cfgDir, ""+
		"worker_id: w1\n"+workerLinkYAML+
		"projects:\n"+
		"  w1:\n"+
		"    host_path: "+host+"\n"+
		"    allowed_runners: [local]\n")

	projs, src, err := localProjects()
	if err != nil {
		t.Fatalf("localProjects (legacy): %v", err)
	}
	if len(projs) != 1 || projs["w1"].HostPath != host {
		t.Fatalf("legacy worker should list worker.yaml projects verbatim, got %v", projs)
	}
	if !strings.Contains(src, "worker.yaml") {
		t.Errorf("src label = %q, want it to mention worker.yaml", src)
	}
	// End-to-end runner must not panic/err either.
	c := bindProjectListCmd(t)
	if err := runProjectList(c, nil); err != nil {
		t.Fatalf("runProjectList (legacy): %v", err)
	}
}

// TestProjectListPolicyWorkerFromCache: a POLICY worker (roots) lists the SERVER's
// projects read from the policy cache and projected onto its roots. A project whose
// host_path maps under a root is listed at its MAPPED local path; one outside every
// root is dropped (not effective).
func TestProjectListPolicyWorkerFromCache(t *testing.T) {
	cfgDir, toDir := t.TempDir(), t.TempDir()
	t.Setenv(config.EnvConfigDir, cfgDir)
	t.Setenv(config.EnvRunMode, config.RunModeWorker)
	writeWorkerYAMLAt(t, cfgDir, ""+
		"worker_id: w1\n"+workerLinkYAML+
		"roots:\n"+
		"  - from: D:/work/x\n"+
		"    to: "+toDir+"\n"+
		"guards:\n"+
		"  allow_exec: true\n")

	seedPolicyCache(t, "w1", &wsproto.Policy{Rev: 7, Projects: []wsproto.PolicyProject{
		{Key: "alpha", HostPath: "D:/work/x/alpha", AllowedAgents: []string{"exec"}, AllowExec: true},
		{Key: "beta", HostPath: "D:/other/beta", AllowedAgents: []string{"exec"}, AllowExec: true},
	}})

	projs, src, err := localProjects()
	if err != nil {
		t.Fatalf("localProjects (policy): %v", err)
	}
	if _, ok := projs["beta"]; ok {
		t.Errorf("beta is outside every root — must NOT be effective, got %v", projs)
	}
	p, ok := projs["alpha"]
	if !ok {
		t.Fatalf("alpha should be effective (maps under root), got %v", projs)
	}
	wantHost := filepath.Join(toDir, "alpha")
	if p.HostPath != wantHost {
		t.Errorf("alpha host_path = %q, want mapped %q", p.HostPath, wantHost)
	}
	if !strings.Contains(src, "policy 缓存") {
		t.Errorf("src label = %q, want it to mention policy 缓存", src)
	}
	c := bindProjectListCmd(t)
	if err := runProjectList(c, nil); err != nil {
		t.Fatalf("runProjectList (policy): %v", err)
	}
}

// TestProjectListPolicyWorkerNoCache: a POLICY worker with NO cache (not running /
// no policy yet) lists nothing — WITHOUT erroring or panicking — and the empty hint
// says why.
func TestProjectListPolicyWorkerNoCache(t *testing.T) {
	cfgDir, toDir := t.TempDir(), t.TempDir()
	t.Setenv(config.EnvConfigDir, cfgDir)
	t.Setenv(config.EnvRunMode, config.RunModeWorker)
	writeWorkerYAMLAt(t, cfgDir, ""+
		"worker_id: w1\n"+workerLinkYAML+
		"roots:\n"+
		"  - from: D:/work/x\n"+
		"    to: "+toDir+"\n")

	projs, src, err := localProjects()
	if err != nil {
		t.Fatalf("no-cache POLICY worker must not error, got: %v", err)
	}
	if len(projs) != 0 {
		t.Fatalf("no-cache POLICY worker should list nothing, got %v", projs)
	}
	if !strings.Contains(src, "未运行或尚未收到 Policy") {
		t.Errorf("empty hint = %q, want the 'not running / no policy yet' explanation", src)
	}
	out := captureOutput(t, func() {
		c := bindProjectListCmd(t)
		if err := runProjectList(c, nil); err != nil {
			t.Fatalf("runProjectList (no cache): %v", err)
		}
	})
	if !strings.Contains(out, "no projects") {
		t.Errorf("output = %q, want a '(no projects …)' line", out)
	}
}

// TestProjectListPolicyWorkerUnusableCache: a half/foreign policy cache (worker_id
// mismatch) is treated as "no cache" — no panic, no error — with a distinct hint.
func TestProjectListPolicyWorkerUnusableCache(t *testing.T) {
	cfgDir, toDir := t.TempDir(), t.TempDir()
	t.Setenv(config.EnvConfigDir, cfgDir)
	t.Setenv(config.EnvRunMode, config.RunModeWorker)
	writeWorkerYAMLAt(t, cfgDir, ""+
		"worker_id: w1\n"+workerLinkYAML+
		"roots:\n"+
		"  - from: D:/work/x\n"+
		"    to: "+toDir+"\n")

	// Seed a cache stamped with a DIFFERENT worker_id at w1's cache path.
	if err := worker.WritePolicyCacheFile(workerPolicyCachePath("w1"), "other-worker",
		&wsproto.Policy{Rev: 1, Projects: []wsproto.PolicyProject{{Key: "x", HostPath: "D:/work/x/x"}}}, 1); err != nil {
		t.Fatalf("seed foreign cache: %v", err)
	}

	projs, src, err := localProjects()
	if err != nil {
		t.Fatalf("unusable cache must not error, got: %v", err)
	}
	if len(projs) != 0 {
		t.Fatalf("unusable cache should list nothing, got %v", projs)
	}
	if !strings.Contains(src, "不可用") {
		t.Errorf("hint = %q, want it to flag the cache as unusable", src)
	}
}

// bindProjectListCmd builds the `project list` sub-command with its flags bound
// (so runProjectList's option vars are initialised) and --remote off.
func bindProjectListCmd(t *testing.T) *gcli.Command {
	t.Helper()
	c := bindCmd(findSub(t, NewProjectCmd(), "list"))
	projectListOpts.remote = false
	t.Cleanup(func() { projectListOpts.remote = false })
	return c
}

// --- config validate worker (doctor), mode-aware ---

// bindWorkerValidateCmd binds `config validate` with target=worker and points
// InputCfgFile at path.
func bindWorkerValidateCmd(t *testing.T, path string) *gcli.Command {
	t.Helper()
	c := bindCmd(NewConfigCmd().Subs[0])
	c.Arg("target").WithValue("worker")
	config.InputCfgFile = path
	t.Cleanup(func() { config.InputCfgFile = "" })
	return c
}

// TestConfigValidateWorkerLegacyWarns: a LEGACY worker still validates its local
// projects (today's behaviour) but earns a deprecation WARN pointing at the
// migration doc — it does NOT fail.
func TestConfigValidateWorkerLegacyWarns(t *testing.T) {
	cfgDir, host := t.TempDir(), t.TempDir()
	t.Setenv(config.EnvConfigDir, cfgDir)
	path := writeWorkerYAMLAt(t, t.TempDir(), ""+
		"worker_id: w1\n"+workerLinkYAML+
		"projects:\n"+
		"  w1:\n"+
		"    host_path: "+host+"\n"+
		"    allowed_runners: [local]\n"+
		"    allow_exec: true\n")

	c := bindWorkerValidateCmd(t, path)
	out := captureOutput(t, func() {
		if err := runConfigValidate(c, nil); err != nil {
			t.Fatalf("legacy worker should validate (WARN, not FAIL): %v", err)
		}
	})
	if !strings.Contains(out, "WARN") || !strings.Contains(out, "已废弃") {
		t.Errorf("expected a deprecation WARN, got:\n%s", out)
	}
	if !strings.Contains(out, migrationDoc) {
		t.Errorf("deprecation WARN should point at %s, got:\n%s", migrationDoc, out)
	}
}

// TestConfigValidateWorkerPolicyPasses is the core of verification 18: a POLICY
// worker with 0 LOCAL projects must PASS (projects come from the server) — NOT fail
// the pre-T6 "no projects" rule — and report the roots/guards + effective count.
func TestConfigValidateWorkerPolicyPasses(t *testing.T) {
	cfgDir, toDir := t.TempDir(), t.TempDir()
	t.Setenv(config.EnvConfigDir, cfgDir)
	path := writeWorkerYAMLAt(t, t.TempDir(), ""+
		"worker_id: w1\n"+workerLinkYAML+
		"roots:\n"+
		"  - from: D:/work/x\n"+
		"    to: "+toDir+"\n"+
		"guards:\n"+
		"  allow_exec: true\n"+
		"  allow_interactive: false\n")

	c := bindWorkerValidateCmd(t, path)
	out := captureOutput(t, func() {
		if err := runConfigValidate(c, nil); err != nil {
			t.Fatalf("POLICY worker with 0 local projects must PASS, got: %v", err)
		}
	})
	if !strings.Contains(out, "projects 由 server 下发") {
		t.Errorf("expected the INFO 'projects pushed by server', got:\n%s", out)
	}
	if !strings.Contains(out, "worker config OK") {
		t.Errorf("expected an overall OK, got:\n%s", out)
	}
	// roots OK line present.
	if !strings.Contains(out, "roots[0]") {
		t.Errorf("expected a roots check line, got:\n%s", out)
	}
}

// TestConfigValidateWorkerPolicyEffectiveCount: with a seeded cache the doctor
// reports the number of EFFECTIVE (mapped) projects, not the raw policy size.
func TestConfigValidateWorkerPolicyEffectiveCount(t *testing.T) {
	cfgDir, toDir := t.TempDir(), t.TempDir()
	t.Setenv(config.EnvConfigDir, cfgDir)
	path := writeWorkerYAMLAt(t, t.TempDir(), ""+
		"worker_id: w1\n"+workerLinkYAML+
		"roots:\n"+
		"  - from: D:/work/x\n"+
		"    to: "+toDir+"\n"+
		"guards:\n"+
		"  allow_exec: true\n")

	// Two under-root projects + one outside → effective count must be 2.
	seedPolicyCache(t, "w1", &wsproto.Policy{Rev: 3, Projects: []wsproto.PolicyProject{
		{Key: "a", HostPath: "D:/work/x/a", AllowedAgents: []string{"exec"}},
		{Key: "b", HostPath: "D:/work/x/b", AllowedAgents: []string{"exec"}},
		{Key: "c", HostPath: "D:/elsewhere/c", AllowedAgents: []string{"exec"}},
	}})

	c := bindWorkerValidateCmd(t, path)
	out := captureOutput(t, func() {
		if err := runConfigValidate(c, nil); err != nil {
			t.Fatalf("POLICY worker should PASS: %v", err)
		}
	})
	if !strings.Contains(out, "当前生效 2 个") {
		t.Errorf("expected effective count 2 (c is outside roots), got:\n%s", out)
	}
}

// TestConfigValidateWorkerPolicyBadRootFails: a root whose `to` does not exist is a
// FAIL (coded, non-zero exit).
func TestConfigValidateWorkerPolicyBadRootFails(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv(config.EnvConfigDir, cfgDir)
	missing := filepath.Join(t.TempDir(), "no-such-dir")
	path := writeWorkerYAMLAt(t, t.TempDir(), ""+
		"worker_id: w1\n"+workerLinkYAML+
		"roots:\n"+
		"  - from: D:/work/x\n"+
		"    to: "+missing+"\n"+
		"guards:\n"+
		"  allow_exec: true\n")

	c := bindWorkerValidateCmd(t, path)
	var out string
	err := func() error {
		var e error
		out = captureOutput(t, func() { e = runConfigValidate(c, nil) })
		return e
	}()
	if err == nil {
		t.Fatal("expected a FAIL for a non-existent root `to`")
	}
	assertCodedExit(t, err)
	if !strings.Contains(out, "to 目录不存在") {
		t.Errorf("expected a 'to dir missing' FAIL line, got:\n%s", out)
	}
}

// TestConfigValidateWorkerPolicyGuardsWarn: an unset guards block WARNs (do-not-
// tighten default) but does NOT fail.
func TestConfigValidateWorkerPolicyGuardsWarn(t *testing.T) {
	cfgDir, toDir := t.TempDir(), t.TempDir()
	t.Setenv(config.EnvConfigDir, cfgDir)
	path := writeWorkerYAMLAt(t, t.TempDir(), ""+
		"worker_id: w1\n"+workerLinkYAML+
		"roots:\n"+
		"  - from: D:/work/x\n"+
		"    to: "+toDir+"\n")

	c := bindWorkerValidateCmd(t, path)
	out := captureOutput(t, func() {
		if err := runConfigValidate(c, nil); err != nil {
			t.Fatalf("unset guards should WARN, not FAIL: %v", err)
		}
	})
	if !strings.Contains(out, "WARN") || !strings.Contains(out, "护栏未设置") {
		t.Errorf("expected a guards WARN, got:\n%s", out)
	}
}

// TestConfigValidateWorkerPolicyDuplicateRootFails: two roots with the same `from`
// are ambiguous → FAIL.
func TestConfigValidateWorkerPolicyDuplicateRootFails(t *testing.T) {
	cfgDir, to1, to2 := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv(config.EnvConfigDir, cfgDir)
	path := writeWorkerYAMLAt(t, t.TempDir(), ""+
		"worker_id: w1\n"+workerLinkYAML+
		"roots:\n"+
		"  - from: D:/work/x\n"+
		"    to: "+to1+"\n"+
		"  - from: D:/work/x\n"+
		"    to: "+to2+"\n"+
		"guards:\n"+
		"  allow_exec: true\n")

	c := bindWorkerValidateCmd(t, path)
	var out string
	var err error
	out = captureOutput(t, func() { err = runConfigValidate(c, nil) })
	if err == nil {
		t.Fatal("expected a FAIL for a duplicate root `from`")
	}
	assertCodedExit(t, err)
	if !strings.Contains(out, "重复的 from") {
		t.Errorf("expected a duplicate-from FAIL line, got:\n%s", out)
	}
}

// TestConfigValidateWorkerPolicyOverlapInfo: a wildcard root plus a more-specific
// override (longer `from`) is INTENTIONAL (§6-H3) — an INFO hint, not a failure.
func TestConfigValidateWorkerPolicyOverlapInfo(t *testing.T) {
	cfgDir, to1, to2 := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv(config.EnvConfigDir, cfgDir)
	path := writeWorkerYAMLAt(t, t.TempDir(), ""+
		"worker_id: w1\n"+workerLinkYAML+
		"roots:\n"+
		"  - from: D:/work/x\n"+
		"    to: "+to1+"\n"+
		"  - from: D:/work/x/proj-a\n"+
		"    to: "+to2+"\n"+
		"guards:\n"+
		"  allow_exec: true\n")

	c := bindWorkerValidateCmd(t, path)
	out := captureOutput(t, func() {
		if err := runConfigValidate(c, nil); err != nil {
			t.Fatalf("overlapping roots are intentional, must PASS: %v", err)
		}
	})
	if !strings.Contains(out, "INFO") || !strings.Contains(out, "覆盖") {
		t.Errorf("expected an overlap INFO (更具体者优先), got:\n%s", out)
	}
}

// TestConfigValidateWorkerEmptyFails: neither roots nor projects → the worker can
// run nothing → FAIL (unchanged pre-T6 behaviour, now with a clearer message).
func TestConfigValidateWorkerEmptyFails(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv(config.EnvConfigDir, cfgDir)
	path := writeWorkerYAMLAt(t, t.TempDir(), "worker_id: w1\n"+workerLinkYAML)

	c := bindWorkerValidateCmd(t, path)
	var out string
	var err error
	out = captureOutput(t, func() { err = runConfigValidate(c, nil) })
	if err == nil {
		t.Fatal("expected a FAIL for a worker with no roots and no projects")
	}
	assertCodedExit(t, err)
	if !strings.Contains(out, "跑不了任何 job") {
		t.Errorf("expected an empty-worker FAIL message, got:\n%s", out)
	}
}
