package tunnel

import (
	"log/slog"
	"testing"
)

// TestLogLevel pins the denoise decision: the steps of opening one tunnel are DEBUG
// (a busy listener emits them per connection), the lines that describe a tunnel on
// their own stay INFO.
func TestLogLevel(t *testing.T) {
	for _, ev := range []string{"tunnel.requested", "tunnel.rendezvous_started", "tunnel.worker_connected", "tunnel.first_up", "tunnel.first_down", "session.opened"} {
		if got := LogLevel(ev); got != slog.LevelDebug {
			t.Errorf("LogLevel(%q)=%v, want DEBUG", ev, got)
		}
	}
	for _, ev := range []string{"tunnel.opened", "session.closed", "forward.started", "session.rejected", "something.new"} {
		if got := LogLevel(ev); got != slog.LevelInfo {
			t.Errorf("LogLevel(%q)=%v, want INFO", ev, got)
		}
	}
}
