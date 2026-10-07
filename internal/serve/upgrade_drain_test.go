package serve

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/servicemgr"
)

func TestUpgradeBridgeMissingControlRestoresTimedOutDrain(t *testing.T) {
	jobs := new(job.Service)
	permit, err := jobs.BeginUpgradeWork()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := jobs.CloseUpgradeAdmission(ctx, "timed-out-upgrade"); err == nil {
		t.Fatal("held permit did not block drain")
	}
	owner := jobs.UpgradeAdmissionOwner()
	if owner != "timed-out-upgrade" {
		t.Fatalf("gate owner lost: %q", owner)
	}
	permit.Release()
	owner, accepted := reconcileMissingUpgradeControl(jobs, nil, owner, false)
	if owner != "" || accepted {
		t.Fatalf("missing control left gate owned: %q, %v", owner, accepted)
	}
	if p, err := jobs.BeginUpgradeWork(); err != nil {
		t.Fatal(err)
	} else {
		p.Release()
	}
}

func TestUpgradeBridgeInitialAdmission(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name     string
		phase    servicemgr.UpgradePhase
		deadline time.Time
		closed   bool
	}{
		{"accepted-past-deadline", servicemgr.UpgradeAccepted, now.Add(-time.Second), true},
		{"switching-past-deadline", servicemgr.UpgradeSwitching, now.Add(-time.Second), true},
		{"draining-past-deadline", servicemgr.UpgradeDraining, now.Add(-time.Second), false},
		{"draining-future", servicemgr.UpgradeDraining, now.Add(time.Minute), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jobs := new(job.Service)
			control := servicemgr.UpgradeControl{UpgradeID: "upgrade-test", Phase: tc.phase, Deadline: tc.deadline}
			id, accepted, err := initializeUpgradeAdmission(jobs, control, nil, now)
			if err != nil {
				t.Fatal(err)
			}
			if (id != "") != tc.closed || (jobs.UpgradeAdmissionOwner() != "") != tc.closed {
				t.Fatalf("gate owner=%q, want closed=%v", id, tc.closed)
			}
			if accepted != (tc.closed && tc.phase != servicemgr.UpgradeDraining) {
				t.Fatalf("accepted=%v", accepted)
			}
			if tc.closed {
				if _, err := jobs.BeginUpgradeWork(); !errors.Is(err, job.ErrUpgradeDraining) {
					t.Fatalf("closed gate allowed work: %v", err)
				}
			}
		})
	}
	jobs := new(job.Service)
	if _, _, err := initializeUpgradeAdmission(jobs, servicemgr.UpgradeControl{}, os.ErrNotExist, now); err != nil {
		t.Fatal(err)
	}
	if jobs.UpgradeAdmissionOwner() != "" {
		t.Fatal("missing control closed admission")
	}
	jobs = new(job.Service)
	if _, _, err := initializeUpgradeAdmission(jobs, servicemgr.UpgradeControl{}, errors.New("bad JSON"), now); err != nil {
		t.Fatal(err)
	}
	if jobs.UpgradeAdmissionOwner() != "invalid-upgrade-control" {
		t.Fatal("malformed control did not close admission")
	}
	_ = jobs.OpenUpgradeAdmission("invalid-upgrade-control")
}
