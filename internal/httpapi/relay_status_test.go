package httpapi

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/sessionrelay"
)

// TestRelayStatusNothingHeldIsConflict: 「现在送」 on a session with nothing waiting is a
// state conflict the caller can see and retry later, not a server error.
func TestRelayStatusNothingHeldIsConflict(t *testing.T) {
	err := fmt.Errorf("held send: %w", sessionrelay.ErrNothingHeld)
	if got := relayStatus(err); got != http.StatusConflict {
		t.Fatalf("relayStatus(ErrNothingHeld) = %d, want %d", got, http.StatusConflict)
	}
}
