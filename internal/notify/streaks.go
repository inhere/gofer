package notify

import (
	"net/url"
	"strings"
	"sync"
)

// ChannelStreaks counts, per outbound notification channel, how many deliveries failed
// IN A ROW; a successful delivery clears that channel's count.
//
// It is the read side of the server self-check's `notification` item (design
// 2026-11-14 §5): one channel that keeps refusing messages is a real fault, while a
// single failure (a provider hiccup, one timeout) must not raise a card. The delivery
// executor records every attempt here; the health probe only reads.
//
// Every method is nil-receiver safe, so a process without notifications wired (or a
// test) can leave the field nil and nothing has to be checked at the call site.
type ChannelStreaks struct {
	mu sync.Mutex
	// fails is channel name → consecutive failed deliveries.
	fails map[string]int
}

// NewChannelStreaks builds an empty tracker.
func NewChannelStreaks() *ChannelStreaks {
	return &ChannelStreaks{fails: map[string]int{}}
}

// Record accounts one delivery attempt of channel: a nil err is a success (the streak
// goes back to zero, and the channel leaves the map), any error extends the streak.
func (c *ChannelStreaks) Record(channel string, err error) {
	if c == nil || strings.TrimSpace(channel) == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil {
		delete(c.fails, channel)
		return
	}
	if c.fails == nil {
		c.fails = map[string]int{}
	}
	c.fails[channel]++
}

// Fails returns a copy of the per-channel consecutive-failure counts; nil when nothing
// is failing. It is the closure the health probe's NotifyProbe takes.
func (c *ChannelStreaks) Fails() map[string]int {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.fails) == 0 {
		return nil
	}
	out := make(map[string]int, len(c.fails))
	for name, n := range c.fails {
		out[name] = n
	}
	return out
}

// ChannelName labels one outbound webhook channel for the health report: its kind plus
// the target's host ("dingtalk:oapi.dingtalk.com"), because the config names a webhook
// by URL only — the host is what an operator recognizes, and the path usually carries
// the bot's access token, which must not be published. A target that is not a URL falls
// back to the kind alone.
func ChannelName(kind, target string) string {
	kind = NormalizeKind(kind)
	if u, err := url.Parse(strings.TrimSpace(target)); err == nil && u.Host != "" {
		return kind + ":" + u.Host
	}
	return kind
}
