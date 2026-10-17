package config

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// Defaults and bounds of server.worker_enrollment (design 「默认数值」).
const (
	DefaultEnrollDecideSec    = 120
	DefaultEnrollCollectSec   = 60
	DefaultEnrollPerSourceMin = 3
	DefaultEnrollMaxPending   = 20

	MinEnrollWindowSec = 30
	MaxEnrollWindowSec = 600
	MaxEnrollPerSource = 60
	MaxEnrollPending   = 200
)

// WorkerEnrollmentConfig is server.worker_enrollment: the self-service worker
// registration switch and its limits. Zero numeric fields mean the default. The whole
// block is hot-reloadable and read from the current config snapshot on every request;
// TrustedProxies is file-only (the console cannot edit it).
type WorkerEnrollmentConfig struct {
	// Enabled accepts new enrollment requests (default false). Turning it off never
	// strands requests already in flight: the sweeper still expires them.
	Enabled bool `yaml:"enabled,omitempty"`
	// DecideSec is how long an administrator has to decide (default 120; 30-600).
	DecideSec int `yaml:"decide_sec,omitempty"`
	// CollectSec is how long the applicant has to collect its token (default 60; 30-600).
	CollectSec int `yaml:"collect_sec,omitempty"`
	// PerSourcePerMin caps requests per source address per minute (default 3; 1-60).
	PerSourcePerMin int `yaml:"per_source_per_min,omitempty"`
	// MaxPending caps open requests server-wide (default 20; 1-200).
	MaxPending int `yaml:"max_pending,omitempty"`
	// TrustedProxies lists CIDRs of reverse proxies whose forwarded-for header may be
	// believed when deriving a request's source address. Empty = the connection address.
	TrustedProxies []string `yaml:"trusted_proxies,omitempty"`
}

// EffectiveDecideSec returns the decision window in seconds after defaulting.
func (w WorkerEnrollmentConfig) EffectiveDecideSec() int {
	if w.DecideSec <= 0 {
		return DefaultEnrollDecideSec
	}
	return w.DecideSec
}

// EffectiveCollectSec returns the collect window in seconds after defaulting.
func (w WorkerEnrollmentConfig) EffectiveCollectSec() int {
	if w.CollectSec <= 0 {
		return DefaultEnrollCollectSec
	}
	return w.CollectSec
}

// EffectivePerSourcePerMin returns the per-source rate after defaulting.
func (w WorkerEnrollmentConfig) EffectivePerSourcePerMin() int {
	if w.PerSourcePerMin <= 0 {
		return DefaultEnrollPerSourceMin
	}
	return w.PerSourcePerMin
}

// EffectiveMaxPending returns the global pending cap after defaulting.
func (w WorkerEnrollmentConfig) EffectiveMaxPending() int {
	if w.MaxPending <= 0 {
		return DefaultEnrollMaxPending
	}
	return w.MaxPending
}

// TrustedProxyPrefixes parses TrustedProxies (already validated at load time; entries
// that fail to parse are skipped).
func (w WorkerEnrollmentConfig) TrustedProxyPrefixes() []netip.Prefix {
	out := make([]netip.Prefix, 0, len(w.TrustedProxies))
	for _, s := range w.TrustedProxies {
		if p, err := netip.ParsePrefix(strings.TrimSpace(s)); err == nil {
			out = append(out, p)
		}
	}
	return out
}

func (w WorkerEnrollmentConfig) clone() WorkerEnrollmentConfig {
	w.TrustedProxies = slices.Clone(w.TrustedProxies)
	return w
}

func (w WorkerEnrollmentConfig) validate() error {
	in := func(name string, v, lo, hi int) error {
		if v != 0 && (v < lo || v > hi) {
			return fmt.Errorf("server.worker_enrollment.%s must be between %d and %d (got %d)", name, lo, hi, v)
		}
		return nil
	}
	if err := in("decide_sec", w.DecideSec, MinEnrollWindowSec, MaxEnrollWindowSec); err != nil {
		return err
	}
	if err := in("collect_sec", w.CollectSec, MinEnrollWindowSec, MaxEnrollWindowSec); err != nil {
		return err
	}
	if err := in("per_source_per_min", w.PerSourcePerMin, 1, MaxEnrollPerSource); err != nil {
		return err
	}
	if err := in("max_pending", w.MaxPending, 1, MaxEnrollPending); err != nil {
		return err
	}
	for i, s := range w.TrustedProxies {
		if _, err := netip.ParsePrefix(strings.TrimSpace(s)); err != nil {
			return fmt.Errorf("server.worker_enrollment.trusted_proxies[%d] %q is not a valid CIDR", i, s)
		}
	}
	return nil
}
