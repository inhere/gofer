package serve

import (
	"errors"
	"testing"

	"github.com/gookit/goutil/testutil/assert"
)

// reconcileSupervisors fills the deficit (desired - active) by submitting one sup job
// per missing replica; it is a no-op when already at/over desired (P4b).
func TestReconcileSupervisorsFillsDeficit(t *testing.T) {
	cases := []struct {
		name       string
		active     int
		desired    int
		wantSubmit int
	}{
		{"empty fleet", 0, 1, 1},
		{"deficit of two", 1, 3, 2},
		{"at desired", 2, 2, 0},
		{"over desired", 5, 2, 0}, // never cancels — only fills up
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			submits := 0
			reconcileSupervisors(tc.desired,
				func() (int, error) { return tc.active, nil },
				func() error { submits++; return nil },
				func(string, ...any) {}, func(string, ...any) {})
			assert.Eq(t, tc.wantSubmit, submits)
		})
	}
}

// A count error aborts the tick with zero submits (no blind dispatch on unknown state).
func TestReconcileSupervisorsCountErrorSkips(t *testing.T) {
	submits := 0
	reconcileSupervisors(3,
		func() (int, error) { return 0, errors.New("db down") },
		func() error { submits++; return nil },
		func(string, ...any) {}, func(string, ...any) {})
	assert.Eq(t, 0, submits)
}

// A submit error aborts THIS tick (best-effort) rather than spinning the whole deficit.
func TestReconcileSupervisorsSubmitErrorStops(t *testing.T) {
	submits := 0
	reconcileSupervisors(5,
		func() (int, error) { return 0, nil },
		func() error { submits++; return errors.New("submit failed") },
		func(string, ...any) {}, func(string, ...any) {})
	assert.Eq(t, 1, submits) // first submit attempted, error stops the loop
}
