package notify

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// W1: work.remind and work.digest are default triggers (a webhook with no `events`
// filter receives them) and render as DingTalk markdown within max_text_runes.
func TestWorkEventsAreDefaultTriggersAndRender(t *testing.T) {
	cfg := &config.NotificationConfig{Webhooks: []config.WebhookConfig{{URL: "https://hooks.example.test/all", Kind: "dingtalk"}}}
	for _, ev := range []string{EventWorkRemind, EventWorkDigest} {
		if got := MatchWebhooks(cfg, ev, "proj"); len(got) != 1 {
			t.Fatalf("a default webhook did not match %s: %+v", ev, got)
		}
	}
	if EventWorkRemind != "work.remind" || EventWorkDigest != "work.digest" {
		t.Fatalf("event names = %q %q", EventWorkRemind, EventWorkDigest)
	}

	long := "等我 3 · 等资源 1 · 需现场 2 · 待验收 0\n\n搁置超过 7 天（1）：\n- [老搁置](http://g/work?id=w-1)\n" + strings.Repeat("很长的内容", 200)
	body, err := RenderMessageWithLimit(KindDingTalk, Message{
		EventType: EventWorkDigest, Title: "工作摘要 · 2026-10-05", Text: long, Link: "http://g/work", LinkLabel: "打开工作页",
	}, 120)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Msgtype  string `json:"msgtype"`
		Markdown struct{ Title, Text string }
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Msgtype != "markdown" || !strings.Contains(payload.Markdown.Text, "工作摘要") ||
		!strings.Contains(payload.Markdown.Text, "[打开工作页](http://g/work)") {
		t.Fatalf("payload = %+v", payload)
	}
	// The quoted body honours the rune budget (title/link are outside it).
	quoted := payload.Markdown.Text[strings.Index(payload.Markdown.Text, "> "):strings.Index(payload.Markdown.Text, "[打开工作页]")]
	if n := len([]rune(quoted)); n > 200 {
		t.Fatalf("quoted body has %d runes, max_text_runes=120 not applied", n)
	}
}

// X2: work.needs_me matches a webhook without an events filter (the work.needs_me_notify
// switch is what keeps it quiet) and an explicit subscription; a webhook that lists other
// events only does not get it.
func TestWorkNeedsMeEventMatching(t *testing.T) {
	if EventWorkNeedsMe != "work.needs_me" {
		t.Fatalf("event name = %q", EventWorkNeedsMe)
	}
	all := &config.NotificationConfig{Webhooks: []config.WebhookConfig{{URL: "https://hooks.example.test/all", Kind: "dingtalk"}}}
	if got := MatchWebhooks(all, EventWorkNeedsMe, "p"); len(got) != 1 {
		t.Fatalf("default webhook did not match: %+v", got)
	}
	only := &config.NotificationConfig{Webhooks: []config.WebhookConfig{{URL: "https://hooks.example.test/only", Kind: "dingtalk", Events: []string{"job.terminal"}}}}
	if got := MatchWebhooks(only, EventWorkNeedsMe, "p"); len(got) != 0 {
		t.Fatalf("a webhook listing other events must not match: %+v", got)
	}
	explicit := &config.NotificationConfig{Webhooks: []config.WebhookConfig{{URL: "https://hooks.example.test/x", Kind: "dingtalk", Events: []string{EventWorkNeedsMe}}}}
	if got := MatchWebhooks(explicit, EventWorkNeedsMe, "p"); len(got) != 1 {
		t.Fatalf("explicit subscription did not match: %+v", got)
	}
}
