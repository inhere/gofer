package config

import "testing"

// TestCallerConcurrencyLimit covers the three-state precedence of the E17
// concurrency-cap resolver (design §7.2): caller override > governance default >
// unlimited(0).
func TestCallerConcurrencyLimit(t *testing.T) {
	sc := ServerConfig{
		Governance: GovernanceConfig{DefaultCallerMaxConcurrent: 4},
		Callers: []CallerConfig{
			{ID: "ci-bot", MaxConcurrentJobs: 8}, // own override wins
			{ID: "ops"},                          // no own cap → governance default
			{ID: "zero", MaxConcurrentJobs: 0},   // explicit 0 → still governance default
		},
	}
	cases := []struct {
		name   string
		caller string
		want   int
	}{
		{"own override wins", "ci-bot", 8},
		{"falls back to governance default", "ops", 4},
		{"explicit zero falls back to governance", "zero", 4},
		{"unknown caller falls back to governance", "nobody", 4},
		{"empty caller uses governance default", "", 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sc.CallerConcurrencyLimit(tc.caller); got != tc.want {
				t.Errorf("CallerConcurrencyLimit(%q) = %d, want %d", tc.caller, got, tc.want)
			}
		})
	}

	// No governance default + no caller override → unlimited (0).
	none := ServerConfig{Callers: []CallerConfig{{ID: "ci"}}}
	if got := none.CallerConcurrencyLimit("ci"); got != 0 {
		t.Errorf("no-default CallerConcurrencyLimit = %d, want 0 (unlimited)", got)
	}
	if got := none.CallerConcurrencyLimit(""); got != 0 {
		t.Errorf("no-default empty-caller CallerConcurrencyLimit = %d, want 0", got)
	}
}

// TestCallerRate covers the E17 rate resolver (design §7.3): caller override >
// governance default > unlimited, plus the burst defaulting (<=0 → max(1,
// ceil(rps))).
func TestCallerRate(t *testing.T) {
	sc := ServerConfig{
		Governance: GovernanceConfig{DefaultRateLimit: 5, DefaultRateBurst: 10},
		Callers: []CallerConfig{
			{ID: "ci-bot", RateLimit: 20, RateBurst: 40}, // full own override
			{ID: "ops"},                                  // no own rate → governance default
			{ID: "rate-only", RateLimit: 7},              // own rate, burst falls to governance default
			{ID: "burst-only", RateBurst: 99},            // no own rate → governance rate, but own burst wins
		},
	}
	cases := []struct {
		name      string
		caller    string
		wantRPS   float64
		wantBurst int
	}{
		{"full own override", "ci-bot", 20, 40},
		{"falls back to governance", "ops", 5, 10},
		{"own rate, governance burst", "rate-only", 7, 10},
		{"governance rate, own burst", "burst-only", 5, 99},
		{"unknown caller uses governance", "nobody", 5, 10},
		{"empty caller uses governance", "", 5, 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rps, burst := sc.CallerRate(tc.caller)
			if rps != tc.wantRPS || burst != tc.wantBurst {
				t.Errorf("CallerRate(%q) = (%v, %d), want (%v, %d)", tc.caller, rps, burst, tc.wantRPS, tc.wantBurst)
			}
		})
	}

	// Rate set with no burst anywhere → burst defaults to max(1, ceil(rps)).
	ceilCfg := ServerConfig{Callers: []CallerConfig{{ID: "c", RateLimit: 2.3}}}
	if rps, burst := ceilCfg.CallerRate("c"); rps != 2.3 || burst != 3 {
		t.Errorf("ceil burst: CallerRate = (%v, %d), want (2.3, 3)", rps, burst)
	}
	// rps < 1 with no burst → burst floors at 1.
	subOne := ServerConfig{Callers: []CallerConfig{{ID: "c", RateLimit: 0.4}}}
	if rps, burst := subOne.CallerRate("c"); rps != 0.4 || burst != 1 {
		t.Errorf("sub-one burst: CallerRate = (%v, %d), want (0.4, 1)", rps, burst)
	}

	// No rate anywhere → (0, 0): rate gating disabled (向后兼容).
	none := ServerConfig{Callers: []CallerConfig{{ID: "c"}}}
	if rps, burst := none.CallerRate("c"); rps != 0 || burst != 0 {
		t.Errorf("no-rate CallerRate = (%v, %d), want (0, 0)", rps, burst)
	}
	if rps, burst := none.CallerRate(""); rps != 0 || burst != 0 {
		t.Errorf("no-rate empty-caller CallerRate = (%v, %d), want (0, 0)", rps, burst)
	}
}
