package config

import (
	"fmt"
	"strconv"
	"strings"
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
	// BroadcastMaxRecipients is 单次广播最多收件人数 (设计 2026-10-22「D6」). Default 20.
	BroadcastMaxRecipients int `yaml:"broadcast_max_recipients,omitempty"`
	// BroadcastRate is 广播频率, "<count>/<window>" (default "5/10m"): each caller may send
	// that many broadcasts per window, whatever the recipient count.
	BroadcastRate string `yaml:"broadcast_rate,omitempty"`
}

// Mail defaults (seconds), and the broadcast numbers.
const (
	DefaultMailTickSec                = 30
	DefaultMailWakeIdleSec            = 300
	DefaultMailWakeIntervalSec        = 600
	DefaultMailDeliverBySec           = 24 * 3600
	DefaultMailClaimLeaseSec          = 60
	DefaultMailBroadcastMaxRecipients = 20
	DefaultMailBroadcastRate          = "5/10m"
)

// MailSettings is MailConfig resolved to durations.
type MailSettings struct {
	Tick         time.Duration
	WakeIdle     time.Duration
	WakeInterval time.Duration
	DeliverBy    time.Duration
	Lease        time.Duration
	// BroadcastMax / BroadcastRate / BroadcastWindow are the broadcast limits (D6).
	BroadcastMax    int
	BroadcastRate   int
	BroadcastWindow time.Duration
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
	// A malformed rate was already refused by validate; here it falls back so a programmatic
	// config (tests, a caller that skipped validation) still has a working ceiling.
	rate, window, err := ParseBroadcastRate(m.BroadcastRate)
	if err != nil {
		rate, window, _ = ParseBroadcastRate(DefaultMailBroadcastRate)
	}
	return MailSettings{
		Tick:         pick(m.TickSec, DefaultMailTickSec),
		WakeIdle:     pick(m.WakeIdleSec, DefaultMailWakeIdleSec),
		WakeInterval: pick(m.WakeIntervalSec, DefaultMailWakeIntervalSec),
		DeliverBy:    pick(m.DeliverBySec, DefaultMailDeliverBySec),
		Lease:        pick(m.ClaimLeaseSec, DefaultMailClaimLeaseSec),

		BroadcastMax:    pickInt(m.BroadcastMaxRecipients, DefaultMailBroadcastMaxRecipients),
		BroadcastRate:   rate,
		BroadcastWindow: window,
	}
}

// ParseBroadcastRate parses "<count>/<window>" ("5/10m"): the broadcast ceiling of one caller.
// A malformed value is a load error, never silently defaulted — a limit nobody can read is a
// limit nobody can rely on.
func ParseBroadcastRate(v string) (count int, window time.Duration, err error) {
	v = strings.TrimSpace(v)
	if v == "" {
		v = DefaultMailBroadcastRate
	}
	n, w, ok := strings.Cut(v, "/")
	if !ok {
		return 0, 0, fmt.Errorf("mail.broadcast_rate %q: want <count>/<window>, e.g. 5/10m", v)
	}
	count, err = strconv.Atoi(strings.TrimSpace(n))
	if err != nil || count <= 0 {
		return 0, 0, fmt.Errorf("mail.broadcast_rate %q: count must be a positive integer", v)
	}
	window, err = time.ParseDuration(strings.TrimSpace(w))
	if err != nil || window <= 0 {
		return 0, 0, fmt.Errorf("mail.broadcast_rate %q: window must be a positive duration", v)
	}
	return count, window, nil
}

func pickInt(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}
func (m MailConfig) validate() error {
	if _, _, err := ParseBroadcastRate(m.BroadcastRate); err != nil {
		return err
	}
	for _, f := range []struct {
		key string
		v   int
	}{
		{"tick_sec", m.TickSec}, {"wake_idle_sec", m.WakeIdleSec}, {"wake_interval_sec", m.WakeIntervalSec},
		{"deliver_by_sec", m.DeliverBySec}, {"claim_lease_sec", m.ClaimLeaseSec},
		{"broadcast_max_recipients", m.BroadcastMaxRecipients},
	} {
		if f.v < 0 {
			return fmt.Errorf("mail.%s must be >= 0 (0 = default)", f.key)
		}
	}
	return nil
}
