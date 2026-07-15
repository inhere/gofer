package core

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	localrunner "github.com/inhere/gofer/internal/runner/local"
)

// buildFileCore seeds a config file with a valid storage root and a single seed
// project, then loads + builds a Core (NoopDetector so agent resolution is
// deterministic) whose write transaction persists back to that same file. Returns
// the core, the config path and a shared host dir every added project may reuse
// (config.validate only requires host_path non-empty, not on-disk, so one dir is
// enough for all keys). The seed generation is Rev 1.
func buildFileCore(t *testing.T) (cr *Core, cfgPath, host string) {
	t.Helper()
	host = t.TempDir()
	root := t.TempDir()
	cfgPath = filepath.Join(t.TempDir(), "gofer.yaml")
	writeConfig(t, cfgPath, &config.Config{
		Storage:  config.StorageConfig{Root: root, DBPath: filepath.Join(root, "gofer.db")},
		Projects: map[string]config.ProjectConfig{"seed": {HostPath: host}},
	})
	cfg, _, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	cr, err = Build(cfg, WithAgentDetector(agent.NoopDetector{}), WithConfigPath(cfgPath))
	if err != nil {
		t.Fatalf("build core: %v", err)
	}
	t.Cleanup(func() { _ = cr.Close() })
	return cr, cfgPath, host
}

// projectFingerprint is a stable content signature of the config's project set,
// used to assert that a given Rev maps to exactly one config content.
func projectFingerprint(cfg *config.Config) string {
	keys := make([]string, 0, len(cfg.Projects))
	for k := range cfg.Projects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

// TestConcurrentWritesUniqueRevision hammers the serial write transaction from N
// goroutines each adding a DISTINCT project key, and records every committed
// generation via the onCommit seam (fires under updateMu, once per generation).
//
// Invariants (verification 5): every committed Rev is UNIQUE and STRICTLY
// increasing, and no two different config contents ever share a Rev. Because each
// Add is a distinct key, N successful adds must produce Revs 2..N+1 with N+1
// distinct fingerprints; the final snapshot must hold all N keys + the seed.
//
// 先证伪: remove updateMu (updateLocked/reloadFromPathLocked stop locking) and two
// writers clone the same snapshot → both store old+1 → this test reddens with a
// duplicate Rev (and/or a lost key in the final set) under -count=50 -race.
func TestConcurrentWritesUniqueRevision(t *testing.T) {
	cr, _, host := buildFileCore(t)

	const n = 50
	var mu sync.Mutex
	revToFP := map[int64]string{}
	cr.onCommit = func(s ConfigSnapshot) {
		fp := projectFingerprint(s.Cfg)
		mu.Lock()
		if prev, dup := revToFP[s.Rev]; dup {
			mu.Unlock()
			t.Errorf("duplicate Rev %d: %q then %q (two configs share one Rev)", s.Rev, prev, fp)
			return
		}
		revToFP[s.Rev] = fp
		mu.Unlock()
	}

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("p%02d", i)
			if err := cr.Projects.Add(key, config.ProjectConfig{HostPath: host}, false); err != nil {
				t.Errorf("add %s: %v", key, err)
			}
		}(i)
	}
	wg.Wait()

	// No lost update: seed + all 50 keys present.
	snap := cr.Snapshot()
	if got := len(snap.Cfg.Projects); got != n+1 {
		t.Fatalf("expected %d projects (seed + %d), got %d", n+1, n, got)
	}
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("p%02d", i)
		if _, ok := snap.Cfg.Projects[key]; !ok {
			t.Errorf("lost update: %s missing from final config", key)
		}
	}

	// Rev is a true generation counter: seed=1, each add +1, no gaps, no dups.
	if snap.Rev != n+1 {
		t.Fatalf("final Rev = %d, want %d (1 seed + %d unique writes)", snap.Rev, n+1, n)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(revToFP) != n {
		t.Fatalf("recorded %d committed generations, want %d", len(revToFP), n)
	}
	seenFP := map[string]bool{}
	for rev, fp := range revToFP {
		if rev < 2 || rev > n+1 {
			t.Errorf("committed Rev %d outside expected 2..%d", rev, n+1)
		}
		if seenFP[fp] {
			t.Errorf("two Revs share content fingerprint %q", fp)
		}
		seenFP[fp] = true
	}
}

// TestConcurrentProjectWritesNoLostUpdate mixes concurrent Add / Remove and a
// concurrent SIGHUP-style reload (Core.Reload) against the same core, then adds a
// fixed final set. It proves the serial transaction never corrupts the project
// map and never loses the terminal set of writes (verification 5, POST/DELETE +
// POST×SIGHUP格). Under -race it also proves POST × SIGHUP take no torn read.
func TestConcurrentProjectWritesNoLostUpdate(t *testing.T) {
	cr, cfgPath, host := buildFileCore(t)

	var wg sync.WaitGroup
	// Churn: add-then-remove throwaway keys.
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("churn%02d", i)
			_ = cr.Projects.Add(key, config.ProjectConfig{HostPath: host}, false)
			_ = cr.Projects.Remove(key)
		}(i)
	}
	// Concurrent SIGHUP reloads from disk (same file), racing the writes above.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = cr.Reload(cfgPath)
		}()
	}
	wg.Wait()

	// After the churn settles, add a fixed set and assert every key survives.
	for i := 0; i < 30; i++ {
		key := fmt.Sprintf("keep%02d", i)
		if err := cr.Projects.Add(key, config.ProjectConfig{HostPath: host}, false); err != nil {
			t.Fatalf("add %s: %v", key, err)
		}
	}
	snap := cr.Snapshot()
	for i := 0; i < 30; i++ {
		key := fmt.Sprintf("keep%02d", i)
		if _, ok := snap.Cfg.Projects[key]; !ok {
			t.Errorf("lost update: %s missing after concurrent churn", key)
		}
	}
	// No churn key leaked into the terminal set.
	for k := range snap.Cfg.Projects {
		if strings.HasPrefix(k, "churn") {
			t.Errorf("churn key %s should have been removed", k)
		}
	}
}

// TestFailedSaveDoesNotPublish forces config.Save to fail (cfgPath under a regular
// file, so MkdirAll fails) and asserts the write transaction publishes NOTHING:
// the on-disk file, the Core snapshot (Rev + content), the project registry and
// the job service all stay on the old generation, and no broadcast fires
// (verification 5 fail-safe + verification 4 negative).
func TestFailedSaveDoesNotPublish(t *testing.T) {
	cr, cfgPath, host := buildFileCore(t)

	before := cr.Snapshot()
	pushes := int64(0)
	cr.pushHook = func() { atomic.AddInt64(&pushes, 1) }

	// Point the save at a path whose parent is a regular file → MkdirAll fails →
	// config.Save errors before writing anything.
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	cr.cfgPath = filepath.Join(blocker, "gofer.yaml")

	err := cr.Projects.Add("newp", config.ProjectConfig{HostPath: host}, false)
	if err == nil {
		t.Fatal("expected Add to fail when config.Save fails")
	}

	// Snapshot unchanged: same Rev, same content, and the key never appeared.
	after := cr.Snapshot()
	if after.Rev != before.Rev {
		t.Errorf("Rev advanced on a failed save: %d -> %d", before.Rev, after.Rev)
	}
	if _, ok := after.Cfg.Projects["newp"]; ok {
		t.Error("failed save must not add the project to the snapshot")
	}
	// Registry (a read path) agrees with the snapshot.
	if _, gerr := cr.Projects.Get("newp"); gerr == nil {
		t.Error("failed save must not add the project to the registry")
	}
	// Job service kept the old config (its snapshot pointer is the old cfg).
	if cr.Jobs.Config() != before.Cfg {
		t.Error("failed save must not swap the job service config")
	}
	// The original file on disk is untouched (still parses, still has seed only).
	diskCfg, _, lerr := config.Load(cfgPath)
	if lerr != nil {
		t.Fatalf("reload original file: %v", lerr)
	}
	if _, ok := diskCfg.Projects["newp"]; ok {
		t.Error("failed save must not write the project to disk")
	}
	// No broadcast on a failed write.
	if got := atomic.LoadInt64(&pushes); got != 0 {
		t.Errorf("failed save triggered %d policy pushes, want 0", got)
	}
}

// TestFourWritePathsBumpRevAndPush walks the FOUR config write paths — SIGHUP
// reload, web add, web update, web delete — and asserts each one bumps Rev by
// exactly 1 AND fires exactly one policy broadcast (verification 5: Rev is a true
// generation counter across all write paths; verification 4: every web write
// republishes).
//
// 先证伪 (verification 4): revert Registry.Add to mutate the live cfg in place
// (bypassing the applier) → the add path stops routing through Core.Update →
// pushHook never fires → the "add pushes==1" assertion reddens.
func TestFourWritePathsBumpRevAndPush(t *testing.T) {
	cr, cfgPath, host := buildFileCore(t)

	pushes := int64(0)
	cr.pushHook = func() { atomic.AddInt64(&pushes, 1) }

	step := func(name string, mutate func() error) {
		beforeRev := cr.Snapshot().Rev
		atomic.StoreInt64(&pushes, 0)
		if err := mutate(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		afterRev := cr.Snapshot().Rev
		if afterRev != beforeRev+1 {
			t.Errorf("%s: Rev %d -> %d, want +1", name, beforeRev, afterRev)
		}
		if got := atomic.LoadInt64(&pushes); got != 1 {
			t.Errorf("%s: %d policy pushes, want exactly 1", name, got)
		}
	}

	step("web-add", func() error {
		return cr.Projects.Add("alpha", config.ProjectConfig{HostPath: host}, false)
	})
	step("web-update", func() error {
		return cr.Projects.Add("alpha", config.ProjectConfig{HostPath: host, AllowExec: true}, true)
	})
	step("web-delete", func() error {
		return cr.Projects.Remove("alpha")
	})
	step("sighup-reload", func() error {
		return cr.Reload(cfgPath)
	})
}

// TestConcurrentSubmitAndProjectAddNoRace drives job Submit against a live
// project WHILE other goroutines add new projects through the write transaction.
// Before B2 the two raced on the shared cfg.Projects map (Add mutated it in place
// while Submit read it through the job service's snapshot); the copy-on-write
// transaction now hands the job service a fresh atomic pointer, so a Submit sees
// either the old or the new project set, never a torn map. Run under -race this is
// the direct proof the既存 POST /v1/projects × Submit race is gone (verification 19).
func TestConcurrentSubmitAndProjectAddNoRace(t *testing.T) {
	host := t.TempDir()
	root := t.TempDir()
	cfgPath := filepath.Join(t.TempDir(), "gofer.yaml") // save target for Add (never the real user config)
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root, DBPath: filepath.Join(root, "gofer.db")},
		Projects: map[string]config.ProjectConfig{
			"self": {HostPath: host, AllowedAgents: []string{"a"}, AllowedRunners: []string{localrunner.Name}},
		},
		Agents: map[string]config.AgentConfig{
			"a": {Type: "cli-agent", Command: "cmd", Args: []string{"{{prompt}}"}},
		},
	}
	cr, err := Build(cfg, WithAgentDetector(agent.NoopDetector{}), WithConfigPath(cfgPath))
	if err != nil {
		t.Fatalf("build core: %v", err)
	}
	t.Cleanup(func() { _ = cr.Close() })
	// Record commands instead of executing them (the agent's command is fictitious).
	cr.Runners[localrunner.Name] = &recordingRunner{cmd: map[string]string{}}

	var wg sync.WaitGroup
	// Writers: add projects through the serial transaction.
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = cr.Projects.Add(fmt.Sprintf("x%02d", i), config.ProjectConfig{HostPath: host}, false)
		}(i)
	}
	// Submitters: keep submitting against the stable "self" project during the churn.
	ids := make(chan string, 20*8)
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				res, serr := cr.Jobs.Submit(job.JobRequest{
					ProjectKey: "self", Agent: "a", Runner: localrunner.Name,
					Prompt: "hi", Cwd: ".", TimeoutSec: 30,
				})
				if serr != nil {
					t.Errorf("submit saw a torn config view: %v", serr)
					continue
				}
				ids <- res.ID
			}
		}()
	}
	wg.Wait()
	close(ids)
	for id := range ids {
		cr.Jobs.Wait(id)
	}
}
