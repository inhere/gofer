package oneshot

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/bgagent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

type fakeJobs struct {
	mu    sync.Mutex
	specs []JobSpec
	out   string
	err   error
}

func (f *fakeJobs) RunReadOnly(_ context.Context, s JobSpec) (JobOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.specs = append(f.specs, s)
	if f.err != nil {
		return JobOutput{}, f.err
	}
	return JobOutput{JobID: "job-" + string(rune('a'+len(f.specs))), SessionID: "sess", Output: []byte(f.out)}, nil
}

func testAgents() *agent.Registry {
	return agent.NewRegistry(&config.Config{})
}

func openStore(t *testing.T, path string) *jobstore.Store {
	t.Helper()
	st, err := jobstore.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func request(t *testing.T, limit int) Request {
	t.Helper()
	args, err := NoToolsArgs(testAgents(), "claude", []string{"--extra"})
	if err != nil {
		t.Fatal(err)
	}
	return Request{Kind: KindJobCheck, Project: "p", Agent: "claude", Prompt: "judge", Args: args,
		Limits: bgagent.Limits{DailyRuns: limit}}
}

// TestInvokerRunsOneReadOnlyJob: the run is handed to the job seam with the forced argv,
// the input is appended to the prompt as JSON, the answer is validated and the run is
// logged under its kind.
func TestInvokerRunsOneReadOnlyJob(t *testing.T) {
	st := openStore(t, filepath.Join(t.TempDir(), "gofer.db"))
	jobs := &fakeJobs{out: `{"ok":true}`}
	inv := NewInvoker(bgagent.New(st, bgagent.Options{}), jobs, testAgents())
	req := request(t, 0)
	req.Input = map[string]string{"evidence": "e1"}
	req.OutputSchema = func(out []byte) error {
		if !strings.Contains(string(out), "ok") {
			return errors.New("no ok")
		}
		return nil
	}
	res, err := inv.Run(context.Background(), req)
	if err != nil || string(res.Output) != `{"ok":true}` || res.JobID == "" {
		t.Fatalf("Run = %+v, %v", res, err)
	}
	if len(jobs.specs) != 1 || !strings.Contains(jobs.specs[0].Prompt, `"evidence": "e1"`) ||
		strings.Join(jobs.specs[0].Args, " ") != "--extra --tools  --no-session-persistence" {
		t.Fatalf("job spec = %+v", jobs.specs)
	}
	stats, err := st.BackgroundStatsSince(KindJobCheck, 0)
	if err != nil || stats.Runs != 1 {
		t.Fatalf("run log = %+v, %v", stats, err)
	}
	// An answer outside the schema is returned wrapping ErrOutputInvalid.
	jobs.out = "garbage"
	if _, err := inv.Run(context.Background(), req); !errors.Is(err, ErrOutputInvalid) {
		t.Fatalf("invalid answer err = %v, want ErrOutputInvalid", err)
	}
}

// TestInvokerRefusesArgsWithoutForcedArgv: a request whose Args lack the no-tools argv,
// and an agent type without a known argv, never reach the job seam.
func TestInvokerRefusesArgsWithoutForcedArgv(t *testing.T) {
	st := openStore(t, filepath.Join(t.TempDir(), "gofer.db"))
	jobs := &fakeJobs{out: "x"}
	inv := NewInvoker(bgagent.New(st, bgagent.Options{}), jobs, testAgents())
	req := request(t, 0)
	req.Args = []string{"--extra"}
	if _, err := inv.Run(context.Background(), req); err == nil || !strings.Contains(err.Error(), "forced no-tools argv") {
		t.Fatalf("args without forced argv err = %v", err)
	}
	req = request(t, 0)
	req.Agent = "mystery"
	if _, err := inv.Run(context.Background(), req); err == nil || !strings.Contains(err.Error(), "no known no-tools argv") {
		t.Fatalf("unknown agent err = %v", err)
	}
	if len(jobs.specs) != 0 {
		t.Fatalf("a refused request ran %d jobs", len(jobs.specs))
	}
}

// TestDailyLimitSurvivesRebuildAndAuditsHardStop: the count is read from the store, so a
// rebuilt invoker (a restarted server) still sees today's runs; the refusal is a
// HardStopError, runs no job and writes a hard-stop audit event.
func TestDailyLimitSurvivesRebuildAndAuditsHardStop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gofer.db")
	st := openStore(t, path)
	jobs := &fakeJobs{out: "x"}
	inv := NewInvoker(bgagent.New(st, bgagent.Options{}), jobs, testAgents())
	for i := 0; i < 2; i++ {
		if _, err := inv.Run(context.Background(), request(t, 2)); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	_ = st.Close()

	st2 := openStore(t, path)
	jobs2 := &fakeJobs{out: "x"}
	inv2 := NewInvoker(bgagent.New(st2, bgagent.Options{}), jobs2, testAgents())
	_, err := inv2.Run(context.Background(), request(t, 2))
	var stop *bgagent.HardStopError
	if !errors.As(err, &stop) || stop.Reason != bgagent.StopDailyLimit || stop.Used != 2 {
		t.Fatalf("after rebuild err = %v, want the daily_limit hard stop with 2 used", err)
	}
	if len(jobs2.specs) != 0 {
		t.Fatalf("a hard stop ran %d jobs", len(jobs2.specs))
	}
	evs, err := st2.ListAuditEventsSince(jobstore.AuditBackgroundStopped, 0, 10)
	if err != nil || len(evs) != 1 || !strings.Contains(evs[0].Detail, `"reason":"daily_limit"`) {
		t.Fatalf("hard-stop audit = %+v, %v", evs, err)
	}
}
