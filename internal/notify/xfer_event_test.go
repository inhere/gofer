package notify

import (
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// TestXferEventNotInDefaultTriggerSet: transfer events are OPT-IN. A webhook with no
// `events` filter subscribes to the default trigger set (job.terminal / a new
// interaction / needs_review); if xfer.put were added to it, every existing webhook
// would start receiving a message for every file anyone moves. Subscribing explicitly
// must work — that is the whole point of routing transfer events through the pipeline.
func TestXferEventNotInDefaultTriggerSet(t *testing.T) {
	for _, ev := range DefaultTriggerEvents {
		if strings.HasPrefix(ev, "xfer.") {
			t.Fatalf("DefaultTriggerEvents = %v, want no transfer event in it", DefaultTriggerEvents)
		}
	}
	cfg := &config.NotificationConfig{Webhooks: []config.WebhookConfig{{URL: "https://hooks.example.test/all"}}}
	for _, ev := range []string{"xfer.put", "xfer.get"} {
		if got := MatchWebhooks(cfg, ev, "proj"); len(got) != 0 {
			t.Fatalf("a default webhook matched %s: %+v", ev, got)
		}
	}
	cfg.Webhooks[0].Events = []string{"xfer.put"}
	if got := MatchWebhooks(cfg, "xfer.put", "proj"); len(got) != 1 {
		t.Fatalf("an explicit xfer.put subscription matched %+v, want the one webhook", got)
	}
	if got := MatchWebhooks(cfg, "xfer.get", "proj"); len(got) != 0 {
		t.Fatalf("an xfer.put-only subscription matched xfer.get: %+v", got)
	}
}
