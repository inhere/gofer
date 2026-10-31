package serve

import (
	"testing"
	"time"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/wait"
)

func seedSynthesisRun(t *testing.T, st *jobstore.Store, id, status string, started, finished int64) {
	t.Helper()
	if err := st.InsertMemorySynthesisRun(jobstore.MemorySynthesisRun{ID: id, ProjectKey: "p-" + id, Day: "2026-10-01", Status: status,
		InputDigest: id, MaterialSchema: "daily-memory-materials.v1", StartedAt: started, FinishedAt: finished}); err != nil {
		t.Fatal(err)
	}
}

// A run left running by the previous process becomes failed ("server restart") once it is
// older than the configured timeout; a young one is left alone.
func TestRecoverMemorySynthesisRunsFailsTimedOutRunning(t *testing.T) {
	cr, _ := buildGateCore(t)
	st := cr.Jobs.Meta()
	now := time.Now()
	seedSynthesisRun(t, st, "stuck", "running", now.Add(-2*time.Hour).Unix(), 0)
	seedSynthesisRun(t, st, "young", "running", now.Add(-time.Minute).Unix(), 0)
	recoverMemorySynthesisRuns(gcli.NewCommand("t", ""), cr.Jobs, config.SynthesisConfig{Timeout: "30m"}, now)
	stuck, _, _ := st.GetMemorySynthesisRun("stuck")
	if stuck.Status != "failed" || stuck.Error != "server restart" {
		t.Fatalf("stuck=%+v", stuck)
	}
	young, _, _ := st.GetMemorySynthesisRun("young")
	if young.Status != "running" {
		t.Fatalf("young=%+v", young)
	}
}

// The prune loop applies retain_days to finished synthesis runs even with every other
// retention off, and never touches a running one.
func TestPruneLoopSweepsExpiredMemorySynthesisRuns(t *testing.T) {
	t.Setenv(config.EnvConfigDir, t.TempDir())
	cr, _ := buildGateCore(t)
	st := cr.Jobs.Meta()
	now := time.Now()
	seedSynthesisRun(t, st, "old", "succeeded", now.AddDate(0, 0, -40).Unix(), now.AddDate(0, 0, -40).Unix())
	seedSynthesisRun(t, st, "recent", "succeeded", now.AddDate(0, 0, -2).Unix(), now.AddDate(0, 0, -2).Unix())
	seedSynthesisRun(t, st, "running", "running", now.AddDate(0, 0, -40).Unix(), 0)

	stop := make(chan struct{})
	defer close(stop)
	startPruneLoop(gcli.NewCommand("t", ""), cr.Jobs, config.RetentionConfig{}, false, 0, 0, nil,
		memorySynthesisPrune(cr.Jobs, config.SynthesisConfig{RetainDays: 30}), stop)

	wait.Until(t, 10*time.Second, "expired synthesis run removed", func() bool {
		_, ok, _ := st.GetMemorySynthesisRun("old")
		return !ok
	})
	for _, id := range []string{"recent", "running"} {
		if _, ok, _ := st.GetMemorySynthesisRun(id); !ok {
			t.Fatalf("%s must be kept", id)
		}
	}
}
