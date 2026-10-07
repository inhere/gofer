package serve

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/servicemgr"
)

func TestUpgradeGateWaitsForClaimThenSubmit(t *testing.T) {
	jobs := new(job.Service)
	permit, err := jobs.BeginUpgradeWork()
	if err != nil {
		t.Fatal(err)
	}
	advanced := make(chan struct{})
	finishAdvance := make(chan struct{})
	swept := make(chan struct{})
	submits, disables := 0, 0
	go func() {
		defer close(swept)
		defer permit.Release()
		sweepSchedules(120, []jobstore.ScheduleRecord{{ID: "once", ScheduleType: "once", NextRunAt: 60}}, 3600,
			func(string, int64) (int64, error) { return 0, nil },
			func(string, int64, int64) (bool, error) { close(advanced); <-finishAdvance; return true, nil },
			func(jobstore.ScheduleRecord) (string, error) { submits++; return "created", nil },
			func(string, string) {}, func(string, int) { disables++ },
			func(string, ...any) {}, func(string, ...any) {})
	}()
	<-advanced // producer has entered its claim while holding the permit
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	closed := make(chan error, 1)
	go func() { closed <- jobs.CloseUpgradeAdmission(ctx, "upgrade-racing-schedule") }()
	for jobs.UpgradeAdmissionOwner() == "" {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		default:
			runtime.Gosched()
		}
	}
	select {
	case err := <-closed:
		t.Fatalf("gate closed before producer completed: %v", err)
	default:
	}
	close(finishAdvance)
	<-swept
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if submits != 1 || disables != 1 {
		t.Fatalf("once schedule lost: submits=%d disables=%d", submits, disables)
	}
	if _, err := jobs.BeginUpgradeWork(); err != job.ErrUpgradeDraining {
		t.Fatalf("closed gate admitted another sweep: %v", err)
	}
}

func TestUpgradePreloadedSwitchingBlocksDueOnce(t *testing.T) {
	jobs := new(job.Service)
	control := servicemgr.UpgradeControl{UpgradeID: "upgrade-preloaded", Phase: servicemgr.UpgradeSwitching, Deadline: time.Now().Add(-time.Second)}
	if _, _, err := initializeUpgradeAdmission(jobs, control, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	advanced := 0
	if permit, err := jobs.BeginUpgradeWork(); err == nil {
		defer permit.Release()
		sweepSchedules(120, []jobstore.ScheduleRecord{{ID: "once", ScheduleType: "once", NextRunAt: 60}}, 3600,
			func(string, int64) (int64, error) { return 0, nil },
			func(string, int64, int64) (bool, error) { advanced++; return true, nil },
			func(jobstore.ScheduleRecord) (string, error) { return "job", nil },
			func(string, string) {}, func(string, int) {}, func(string, ...any) {}, func(string, ...any) {})
	}
	if advanced != 0 {
		t.Fatalf("preloaded switching advanced once schedule %d times", advanced)
	}
}
