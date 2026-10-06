package config

import (
	"testing"
	"time"
)

func TestWorkNeedsMeNotifyDefaultsOffAndThrottle(t *testing.T) {
	var w WorkConfig
	if w.NeedsMeNotifyOn() {
		t.Fatal("work.needs_me_notify must default to OFF")
	}
	if w.NeedsMeThrottle() != 30*time.Minute {
		t.Fatalf("default throttle = %v", w.NeedsMeThrottle())
	}
	on := true
	w = WorkConfig{NeedsMeNotify: &on, NeedsMeThrottleMin: 5}
	if !w.NeedsMeNotifyOn() || w.NeedsMeThrottle() != 5*time.Minute {
		t.Fatalf("explicit values lost: %v %v", w.NeedsMeNotifyOn(), w.NeedsMeThrottle())
	}
	if err := (WorkConfig{NeedsMeThrottleMin: -1}).validate(); err == nil {
		t.Fatal("a negative throttle must be rejected")
	}
}
