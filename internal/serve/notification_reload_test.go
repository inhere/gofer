package serve

import (
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
)

func TestNotificationEnableHotReload(t *testing.T) {
	if got := notificationInterval(&config.NotificationConfig{}); got != deliveryInterval {
		t.Fatalf("default notification interval=%s, want %s", got, deliveryInterval)
	}
	n := &config.NotificationConfig{IntervalSec: 3}
	if got := notificationInterval(n); got != 3*time.Second {
		t.Fatalf("configured notification interval=%s, want 3s", got)
	}
}
