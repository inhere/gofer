package job

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestUpgradeAdmissionTimeoutAndRestore(t *testing.T) {
	s := new(Service)
	permit, err := s.BeginUpgradeWork()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // deterministic timeout while a permit is held
	if err := s.CloseUpgradeAdmission(ctx, "upgrade-one"); !errors.Is(err, context.Canceled) {
		t.Fatalf("close = %v", err)
	}
	if s.UpgradeAdmissionOwner() != "upgrade-one" {
		t.Fatal("timeout lost the gate owner")
	}
	if _, err := s.BeginUpgradeWork(); !errors.Is(err, ErrUpgradeDraining) {
		t.Fatalf("closed gate allowed admission: %v", err)
	}
	permit.Release()
	if err := s.OpenUpgradeAdmission("other-upgrade"); err == nil {
		t.Fatal("other transaction reopened gate")
	}
	if err := s.OpenUpgradeAdmission("upgrade-one"); err != nil {
		t.Fatal(err)
	}
	if p, err := s.BeginUpgradeWork(); err != nil {
		t.Fatal(err)
	} else {
		p.Release()
	}
}

func TestUpgradeAdmissionWaitsForProducer(t *testing.T) {
	s := new(Service)
	permit, err := s.BeginUpgradeWork()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.CloseUpgradeAdmission(ctx, "upgrade-two") }()
	permit.Release() // explicit release unblocks close; no scheduler timing assumption
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := s.OpenUpgradeAdmission("upgrade-two"); err != nil {
		t.Fatal(err)
	}
}
