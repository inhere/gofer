package commands

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/client"
)

func TestWaitWorkerUpgradePollsUntilTerminal(t *testing.T) {
	calls := 0
	get := func() (client.WorkerDetail, error) {
		calls++
		switch {
		case calls == 1:
			return client.WorkerDetail{}, errors.New("connection refused") // worker/server blip
		case calls == 2:
			return client.WorkerDetail{Upgrade: &client.WorkerUpgradeRecord{UpgradeID: "old", State: client.WorkerUpgradeSucceeded}}, nil // a previous upgrade
		case calls == 3:
			return client.WorkerDetail{Upgrade: &client.WorkerUpgradeRecord{UpgradeID: "u1", State: client.WorkerUpgradePending}}, nil
		}
		return client.WorkerDetail{Upgrade: &client.WorkerUpgradeRecord{UpgradeID: "u1", State: client.WorkerUpgradeSucceeded, TargetVersion: "v2", DurationMS: 4200}}, nil
	}
	slept := time.Duration(0)
	rec, err := waitWorkerUpgrade(get, "w1", "u1", time.Minute, time.Second, func(d time.Duration) { slept += d })
	if err != nil || rec.State != client.WorkerUpgradeSucceeded || calls != 4 || slept != 3*time.Second {
		t.Fatalf("rec=%+v err=%v calls=%d slept=%s", rec, err, calls, slept)
	}
	if got := describeWorkerUpgrade(rec); !strings.Contains(got, "已升级到 v2") || !strings.Contains(got, "4.2s") {
		t.Fatalf("describe = %q", got)
	}
}

func TestWaitWorkerUpgradeTimesOut(t *testing.T) {
	get := func() (client.WorkerDetail, error) {
		return client.WorkerDetail{Upgrade: &client.WorkerUpgradeRecord{UpgradeID: "u1", State: client.WorkerUpgradePending}}, nil
	}
	_, err := waitWorkerUpgrade(get, "w1", "u1", 3*time.Second, time.Second, func(time.Duration) {})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v", err)
	}
}

func TestDescribeWorkerUpgradeRollback(t *testing.T) {
	got := describeWorkerUpgrade(client.WorkerUpgradeRecord{State: client.WorkerUpgradeRolledBack, Error: "new process never registered", FromVersion: "v1"})
	if !strings.Contains(got, "已回滚：new process never registered") {
		t.Fatalf("describe = %q", got)
	}
}
