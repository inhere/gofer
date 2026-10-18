package config

import (
	"fmt"
	"time"
)

// MailConfig is the top-level `mail:` block: the gofer 信箱 server deliverer's numbers
// (design 2026-10-16「默认数值」, plan M5). Every key is optional; 0 / unset takes the
// documented default, and a negative value is a load error. The deliverer reads them at
// each reload, so an edit applies without a restart (the tick interval from the sweep after
// the one in flight).
type MailConfig struct {
	// TickSec is how often the deliverer sweeps (release expired leases, park overdue
	// letters, wake idle sessions, deliver to suga / ACP jobs). Default 30.
	TickSec int `yaml:"tick_sec,omitempty"`
	// WakeIdleSec is how long a session must have been silent (last_seen_at) before an
	// idle / offline session with pending letters is woken. Default 300 (5 minutes).
	WakeIdleSec int `yaml:"wake_idle_sec,omitempty"`
	// WakeIntervalSec is the minimum gap between two wakes of one session. Default 600.
	WakeIntervalSec int `yaml:"wake_interval_sec,omitempty"`
	// DeliverBySec is the delivery deadline of NEW letters: past it an undelivered letter
	// goes to the capture box. Default 86400 (24 hours).
	DeliverBySec int `yaml:"deliver_by_sec,omitempty"`
	// ClaimLeaseSec is the lease of the deliverer's claims (suga send / job say): a claim not
	// confirmed within it returns to pending. Default 60. Hook claims keep the 60 s default.
	ClaimLeaseSec int `yaml:"claim_lease_sec,omitempty"`
}

// Mail defaults (seconds).
const (
	DefaultMailTickSec         = 30
	DefaultMailWakeIdleSec     = 300
	DefaultMailWakeIntervalSec = 600
	DefaultMailDeliverBySec    = 24 * 3600
	DefaultMailClaimLeaseSec   = 60
)

// MailSettings is MailConfig resolved to durations.
type MailSettings struct {
	Tick         time.Duration
	WakeIdle     time.Duration
	WakeInterval time.Duration
	DeliverBy    time.Duration
	Lease        time.Duration
}

// EffectiveMail resolves the `mail:` block with the defaults applied.
func (c *Config) EffectiveMail() MailSettings {
	var m MailConfig
	if c != nil {
		m = c.Mail
	}
	pick := func(v, def int) time.Duration {
		if v <= 0 {
			v = def
		}
		return time.Duration(v) * time.Second
	}
	return MailSettings{
		Tick:         pick(m.TickSec, DefaultMailTickSec),
		WakeIdle:     pick(m.WakeIdleSec, DefaultMailWakeIdleSec),
		WakeInterval: pick(m.WakeIntervalSec, DefaultMailWakeIntervalSec),
		DeliverBy:    pick(m.DeliverBySec, DefaultMailDeliverBySec),
		Lease:        pick(m.ClaimLeaseSec, DefaultMailClaimLeaseSec),
	}
}

// validate rejects negative numbers (0 means "default").
func (m MailConfig) validate() error {
	for _, f := range []struct {
		key string
		v   int
	}{
		{"tick_sec", m.TickSec}, {"wake_idle_sec", m.WakeIdleSec}, {"wake_interval_sec", m.WakeIntervalSec},
		{"deliver_by_sec", m.DeliverBySec}, {"claim_lease_sec", m.ClaimLeaseSec},
	} {
		if f.v < 0 {
			return fmt.Errorf("mail.%s must be >= 0 (0 = default)", f.key)
		}
	}
	return nil
}
