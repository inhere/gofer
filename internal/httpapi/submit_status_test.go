package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/job"
)

// TestSubmitStatusMapping proves the Submit-error → HTTP status mapping, including
// the P2 ErrNoEligibleWorker → 503 (transient: retry / pick another worker).
func TestSubmitStatusMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"unknown project → 404", fmt.Errorf("%w: x", job.ErrUnknownProject), http.StatusNotFound},
		{"no eligible worker → 503", fmt.Errorf("%w: labels", job.ErrNoEligibleWorker), http.StatusServiceUnavailable},
		{"invalid request → 400", fmt.Errorf("%w: x", job.ErrInvalidRequest), http.StatusBadRequest},
		{"generic → 400", errors.New("boom"), http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := submitStatus(tc.err); got != tc.want {
				t.Fatalf("submitStatus = %d, want %d", got, tc.want)
			}
		})
	}
}
