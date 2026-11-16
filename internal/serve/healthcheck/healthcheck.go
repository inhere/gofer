// Package healthcheck is the server self-check loop (design 2026-11-14 §5, plan R10).
//
// It owns the CADENCE and the CONSECUTIVE-FAILURE counting only: every item's probe is
// supplied by the package that owns the capability (jobstore, the notification
// dispatcher, the schedule sweeper), so this package never re-implements one. A check
// counts as failing after Threshold consecutive failures (server.health.fail_threshold,
// default 3) and recovers on the FIRST success after that — one slow probe must not
// raise a card, one recovery must clear it.
package healthcheck

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultInterval is the self-check cadence (design §5: every 60 seconds).
const DefaultInterval = 60 * time.Second

// Check names. They are stable identifiers: they travel on the wire
// (`/v1/health/checks`, `/v1/stats` health_failing), in the 「等我」 card key
// (`server.health:<name>`) and in the notification payload, so renaming one breaks a
// person's saved card state.
const (
	CheckStorage      = "storage"
	CheckDisk         = "disk"
	CheckSweep        = "sweep"
	CheckNotification = "notification"
)

// Result is one item's state after the latest run.
type Result struct {
	// Name is the stable check identifier (see the Check* constants).
	Name string `json:"name"`
	// OK reports whether the LATEST run of the probe succeeded.
	OK bool `json:"ok"`
	// ConsecutiveFailures counts the consecutive failed runs since the last success.
	ConsecutiveFailures int `json:"consecutive_failures"`
	// Failing reports that ConsecutiveFailures reached the threshold, i.e. this item is
	// a fault now (OK=false is a single miss; Failing is the reported fault).
	Failing bool `json:"failing"`
	// LastError is the latest failure's message ("" when the latest run succeeded).
	LastError string `json:"last_error,omitempty"`
	// CheckedAt is the unix second of the latest run (0 = never run).
	CheckedAt int64 `json:"checked_at"`
}

// Check is one self-check item: a stable name plus the probe the owning package
// supplies.
type Check struct {
	Name  string
	Probe func() error
}

// Runner runs the checks on a cadence and keeps each item's consecutive-failure count.
type Runner struct {
	threshold int
	checks    []Check
	now       func() time.Time

	mu      sync.Mutex
	results map[string]*Result
	// order keeps the configured check order in every snapshot, so the console and
	// `gofer doctor` list the items the same way on every run.
	order []string

	// onFailing / onRecovered fire on the TRANSITION only (a fault is announced once, a
	// recovery once), never on every failing run.
	onFailing   func(Result)
	onRecovered func(Result)
}

// New builds a runner for checks with the given consecutive-failure threshold. A
// threshold <= 0 takes DefaultFailThreshold, so a config that never mentions the block
// behaves like the documented default.
func New(threshold int, checks ...Check) *Runner {
	if threshold <= 0 {
		threshold = DefaultFailThreshold
	}
	r := &Runner{threshold: threshold, checks: checks, now: time.Now, results: map[string]*Result{}}
	for _, c := range checks {
		r.order = append(r.order, c.Name)
		r.results[c.Name] = &Result{Name: c.Name, CheckedAt: 0}
	}
	return r
}

// DefaultFailThreshold mirrors config.DefaultHealthFailThreshold for callers of this
// package that have no config at hand (tests, the doctor path).
const DefaultFailThreshold = 3

// Threshold is the resolved consecutive-failure threshold.
func (r *Runner) Threshold() int { return r.threshold }

// OnTransition sets the fault / recovery callbacks. They run OUTSIDE the runner's lock
// and are therefore free to read Snapshot or notify the world.
func (r *Runner) OnTransition(failing, recovered func(Result)) {
	r.mu.Lock()
	r.onFailing, r.onRecovered = failing, recovered
	r.mu.Unlock()
}

// RunOnce runs every probe once, updates the counters, and reports the transitions that
// happened. A probe panic is not recovered: probes are server-side and a panic there is
// a bug, not a health result.
func (r *Runner) RunOnce() (failures, recoveries []Result) {
	at := r.now().Unix()
	type transition struct {
		res       Result
		recovered bool
	}
	var events []transition
	r.mu.Lock()
	for _, c := range r.checks {
		res := r.results[c.Name]
		err := c.Probe()
		res.CheckedAt = at
		if err != nil {
			res.OK = false
			res.LastError = err.Error()
			res.ConsecutiveFailures++
			if !res.Failing && res.ConsecutiveFailures >= r.threshold {
				res.Failing = true
				events = append(events, transition{res: *res})
			}
			continue
		}
		wasFailing := res.Failing
		res.OK = true
		res.Failing = false
		res.ConsecutiveFailures = 0
		res.LastError = ""
		if wasFailing {
			events = append(events, transition{res: *res, recovered: true})
		}
	}
	failing := r.onFailing
	recovered := r.onRecovered
	r.mu.Unlock()
	for _, ev := range events {
		if ev.recovered {
			recoveries = append(recoveries, ev.res)
			if recovered != nil {
				recovered(ev.res)
			}
			continue
		}
		failures = append(failures, ev.res)
		if failing != nil {
			failing(ev.res)
		}
	}
	return failures, recoveries
}

// Snapshot returns a copy of every item's state in the configured order. Never nil.
func (r *Runner) Snapshot() []Result {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Result, 0, len(r.order))
	for _, name := range r.order {
		if res, ok := r.results[name]; ok {
			out = append(out, *res)
		}
	}
	return out
}

// Failing returns the names of the items currently failing, sorted: the list
// `/v1/stats` publishes and `serve status` prints.
func (r *Runner) Failing() []string {
	var out []string
	for _, res := range r.Snapshot() {
		if res.Failing {
			out = append(out, res.Name)
		}
	}
	sort.Strings(out)
	return out
}

// Degraded reports whether any item is failing now — the single bit /health publishes
// (with no detail).
func (r *Runner) Degraded() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, res := range r.results {
		if res.Failing {
			return true
		}
	}
	return false
}

// Describe renders a one-line human summary of the given results ("disk: no space left").
// An empty input yields "".
func Describe(results []Result) string {
	parts := make([]string, 0, len(results))
	for _, res := range results {
		if strings.TrimSpace(res.LastError) == "" {
			parts = append(parts, res.Name)
			continue
		}
		parts = append(parts, res.Name+": "+res.LastError)
	}
	return strings.Join(parts, "; ")
}

// Loop runs the checks once immediately and then every interval until stop closes.
func (r *Runner) Loop(interval time.Duration, stop <-chan struct{}) {
	if interval <= 0 {
		interval = DefaultInterval
	}
	r.RunOnce()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			r.RunOnce()
		}
	}
}
