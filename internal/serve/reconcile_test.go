package serve

import (
	"errors"
	"testing"

	"github.com/gookit/goutil/testutil/assert"
)

// reconcileSupervisors (event-driven, y5wt) spawns a sup ONLY when a sup is wanted
// (demand>0) AND none is running (active<desired), filling the deficit to desired. Idle
// (demand<=0) or already-running (active>=desired) ⇒ zero submits ⇒ zero claude cost.
func TestReconcileSupervisorsDemandGate(t *testing.T) {
	cases := []struct {
		name       string
		active     int
		demand     int
		desired    int
		wantSubmit int
	}{
		{"idle: no demand, no spawn", 0, 0, 1, 0},      // 核心: 空闲零成本
		{"demand, empty fleet → spawn", 0, 1, 1, 1},    // 核心: 按需唤醒
		{"demand, deficit of two", 0, 5, 3, 3},         // 填满 desired (一个 sup 也能清空, 但仍补满)
		{"demand but sup already running", 1, 9, 1, 0}, // active 闸: 已有 sup 在清, 不重复派
		{"demand, partial fleet", 1, 4, 3, 2},          // 补足缺额
		{"over desired, with demand", 5, 9, 2, 0},      // 永不超派
		{"demand cleared mid-fleet", 2, 2, 2, 0},       // 已达 desired
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			submits := 0
			reconcileSupervisors(tc.desired,
				func() (int, error) { return tc.active, nil },
				func() (int, error) { return tc.demand, nil },
				func() error { submits++; return nil },
				func(string, ...any) {}, func(string, ...any) {})
			assert.Eq(t, tc.wantSubmit, submits)
		})
	}
}

// The demand count is NOT consulted when a sup is already active (cheapest gate first):
// active>=desired short-circuits before any demand query.
func TestReconcileSupervisorsActiveGateSkipsDemand(t *testing.T) {
	submits, demandCalls := 0, 0
	reconcileSupervisors(1,
		func() (int, error) { return 1, nil }, // already at desired
		func() (int, error) { demandCalls++; return 9, nil },
		func() error { submits++; return nil },
		func(string, ...any) {}, func(string, ...any) {})
	assert.Eq(t, 0, submits)
	assert.Eq(t, 0, demandCalls) // demand never queried — active gate short-circuits
}

// An active-count error aborts with zero submits (no blind dispatch on unknown state).
func TestReconcileSupervisorsActiveCountErrorSkips(t *testing.T) {
	submits := 0
	reconcileSupervisors(3,
		func() (int, error) { return 0, errors.New("db down") },
		func() (int, error) { return 9, nil },
		func() error { submits++; return nil },
		func(string, ...any) {}, func(string, ...any) {})
	assert.Eq(t, 0, submits)
}

// A demand-count error aborts with zero submits (don't spawn on unknown demand).
func TestReconcileSupervisorsDemandCountErrorSkips(t *testing.T) {
	submits := 0
	reconcileSupervisors(3,
		func() (int, error) { return 0, nil },
		func() (int, error) { return 0, errors.New("db down") },
		func() error { submits++; return nil },
		func(string, ...any) {}, func(string, ...any) {})
	assert.Eq(t, 0, submits)
}

// A submit error aborts THIS call (best-effort) rather than spinning the whole deficit.
func TestReconcileSupervisorsSubmitErrorStops(t *testing.T) {
	submits := 0
	reconcileSupervisors(5,
		func() (int, error) { return 0, nil },
		func() (int, error) { return 9, nil },
		func() error { submits++; return errors.New("submit failed") },
		func(string, ...any) {}, func(string, ...any) {})
	assert.Eq(t, 1, submits) // first submit attempted, error stops the loop
}
