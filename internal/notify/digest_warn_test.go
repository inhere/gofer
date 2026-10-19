package notify

import (
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/config"
)

func TestDigestNoSubscriberWarning(t *testing.T) {
	off, on := false, true
	hook := func(events ...string) *config.NotificationConfig {
		return &config.NotificationConfig{Webhooks: []config.WebhookConfig{{URL: "http://x", Events: events}}}
	}
	cfg := func(n *config.NotificationConfig) *config.Config {
		c := &config.Config{}
		c.Server.Notification = n
		return c
	}
	assert.Eq(t, "", DigestNoSubscriberWarning(nil))
	// Digest is on by default, but a fresh config (no webhook, digest_enabled unset)
	// must not warn; an explicit digest_enabled: true with no webhook does.
	assert.Eq(t, "", DigestNoSubscriberWarning(cfg(nil)))
	explicit := cfg(nil)
	explicit.Work.DigestEnabled = &on
	assert.True(t, strings.Contains(DigestNoSubscriberWarning(explicit), "work.digest"))
	// A default-events webhook (no filter) subscribes to work.digest.
	assert.Eq(t, "", DigestNoSubscriberWarning(cfg(hook())))
	// A webhook filtered to other events does not.
	filtered := cfg(hook("job.terminal"))
	filtered.Work.DigestEnabled = &on
	assert.True(t, DigestNoSubscriberWarning(filtered) != "")
	assert.Eq(t, "", DigestNoSubscriberWarning(cfg(hook("work.digest"))))
	// An event hook subscribed to work.digest is a subscriber too (AUTO-04).
	withHook := cfg(nil)
	withHook.Server.EventHooks = []config.EventHookConfig{{Name: "h", Command: []string{"x"}, Events: []string{"*"}}}
	assert.Eq(t, "", DigestNoSubscriberWarning(withHook))
	// Digest off and steward off -> nothing to say; steward on re-enables the warning.
	c := cfg(nil)
	c.Work.DigestEnabled = &off
	assert.Eq(t, "", DigestNoSubscriberWarning(c))
	c.Steward.Enabled = true
	assert.True(t, DigestNoSubscriberWarning(c) != "")
}
