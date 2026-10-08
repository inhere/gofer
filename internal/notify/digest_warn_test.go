package notify

import (
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/config"
)

func TestDigestNoSubscriberWarning(t *testing.T) {
	off := false
	hook := func(events ...string) *config.NotificationConfig {
		return &config.NotificationConfig{Webhooks: []config.WebhookConfig{{URL: "http://x", Events: events}}}
	}
	cfg := func(n *config.NotificationConfig) *config.Config {
		c := &config.Config{}
		c.Server.Notification = n
		return c
	}
	assert.Eq(t, "", DigestNoSubscriberWarning(nil))
	// Digest is on by default; no webhook at all -> warn.
	assert.True(t, strings.Contains(DigestNoSubscriberWarning(cfg(nil)), "work.digest"))
	// A default-events webhook (no filter) subscribes to work.digest.
	assert.Eq(t, "", DigestNoSubscriberWarning(cfg(hook())))
	// A webhook filtered to other events does not.
	assert.True(t, DigestNoSubscriberWarning(cfg(hook("job.terminal"))) != "")
	assert.Eq(t, "", DigestNoSubscriberWarning(cfg(hook("work.digest"))))
	// Digest off and steward off -> nothing to say; steward on re-enables the warning.
	c := cfg(nil)
	c.Work.DigestEnabled = &off
	assert.Eq(t, "", DigestNoSubscriberWarning(c))
	c.Steward.Enabled = true
	assert.True(t, DigestNoSubscriberWarning(c) != "")
}
