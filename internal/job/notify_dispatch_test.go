package job

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/notify"
)

// TestNotifySessionWaitingEndToEnd covers the OBS-07a path a phone actually
// exercises: a relayed session opens a turn → NotifySessionWaiting enqueues a
// PRE-RENDERED delivery (no job, no events row) → the sweeper posts the
// DingTalk message with the provider signature applied to the URL.
func TestNotifySessionWaitingEndToEnd(t *testing.T) {
	root := t.TempDir()
	s := newNotifyService(t, root, []config.WebhookConfig{{
		URL:       "https://hooks.example.com/robot/send?access_token=tok",
		Kind:      "dingtalk",
		SecretEnv: "TEST_DING_SECRET",
		Events:    []string{EventSessionWaiting},
	}}, nil)
	cfg := s.config()
	cfg.Server.WebBaseURL = "https://gofer.example.com/"
	t.Setenv("TEST_DING_SECRET", "SECabc")

	var gotURL, gotType string
	var gotBody []byte
	var gotSecret string
	s.postFn = func(_ context.Context, target, eventType string, body []byte, secret string, _ config.NotificationConfig) error {
		gotURL, gotType, gotBody, gotSecret = target, eventType, body, secret
		return nil
	}

	s.NotifySessionWaiting("sid-123456789", "self", "repo: 修 bug", "第一步做完了，选 A 还是 B？", 3)

	n := s.DeliverDue(context.Background())
	assert.Eq(t, 1, n)
	assert.Eq(t, EventSessionWaiting, gotType)
	// The provider signs in the URL, so no HMAC header secret is passed through.
	assert.Eq(t, "", gotSecret)

	u, err := url.Parse(gotURL)
	assert.NoErr(t, err)
	assert.Eq(t, "tok", u.Query().Get("access_token"))
	assert.True(t, u.Query().Get("sign") != "")
	assert.True(t, u.Query().Get("timestamp") != "")

	var d struct {
		MsgType  string            `json:"msgtype"`
		Markdown map[string]string `json:"markdown"`
	}
	assert.NoErr(t, json.Unmarshal(gotBody, &d))
	assert.Eq(t, "markdown", d.MsgType)
	assert.True(t, strings.Contains(d.Markdown["title"], "会话等待回复"))
	assert.True(t, strings.Contains(d.Markdown["title"], "repo: 修 bug"))
	assert.True(t, strings.Contains(d.Markdown["text"], "选 A 还是 B？"))
	// The link points at the session drawer through the configured public base.
	assert.True(t, strings.Contains(d.Markdown["text"], "https://gofer.example.com/sessions?sid=sid-123456789"))
}

// TestNotifyEventSubscriptionRules proves session events are opt-in: a webhook
// with no `events` filter (default job triggers) is NOT notified, a project
// filter is honoured, and notify_enabled=false silences the project.
func TestNotifyEventSubscriptionRules(t *testing.T) {
	root := t.TempDir()
	msg := notify.Message{Title: "t", Text: "x"}

	// default filter → job triggers only → no delivery for a session event
	s := newNotifyService(t, root, []config.WebhookConfig{
		{URL: "https://hooks.example.com/a"},
	}, nil)
	assert.Eq(t, 0, s.NotifyEvent(EventSessionWaiting, "self", msg))

	// explicit subscription → enqueued
	s2 := newNotifyService(t, t.TempDir(), []config.WebhookConfig{
		{URL: "https://hooks.example.com/b", Events: []string{EventSessionWaiting}},
	}, nil)
	assert.Eq(t, 1, s2.NotifyEvent(EventSessionWaiting, "self", msg))

	// project filter that does not match → skipped
	s3 := newNotifyService(t, t.TempDir(), []config.WebhookConfig{
		{URL: "https://hooks.example.com/c", Events: []string{EventSessionWaiting}, Projects: []string{"other"}},
	}, nil)
	assert.Eq(t, 0, s3.NotifyEvent(EventSessionWaiting, "self", msg))

	// per-project notify_enabled=false silences it
	off := false
	s4 := newNotifyService(t, t.TempDir(), []config.WebhookConfig{
		{URL: "https://hooks.example.com/d", Events: []string{EventSessionWaiting}},
	}, &off)
	assert.Eq(t, 0, s4.NotifyEvent(EventSessionWaiting, "self", msg))

	// unknown project key still notifies (no project gate to consult)
	s5 := newNotifyService(t, t.TempDir(), []config.WebhookConfig{
		{URL: "https://hooks.example.com/e", Events: []string{EventSessionWaiting}},
	}, nil)
	assert.Eq(t, 1, s5.NotifyEvent(EventSessionWaiting, "nosuch", msg))
}

// TestJobEventRendersForIMKind proves a job-event delivery to an IM webhook is
// re-rendered as a bot message instead of the machine `{event, job}` contract.
func TestJobEventRendersForIMKind(t *testing.T) {
	root := t.TempDir()
	s := newNotifyService(t, root, []config.WebhookConfig{{
		URL: "https://hooks.example.com/gofer", Kind: "feishu",
	}}, nil)
	s.config().Server.WebBaseURL = "https://gofer.example.com"

	var gotBody []byte
	s.postFn = func(_ context.Context, _, _ string, body []byte, _ string, _ config.NotificationConfig) error {
		gotBody = body
		return nil
	}
	jobID, _ := submitDeliveredJob(t, s)
	assert.Eq(t, 1, s.DeliverDue(context.Background()))

	var f struct {
		MsgType string            `json:"msg_type"`
		Content map[string]string `json:"content"`
	}
	assert.NoErr(t, json.Unmarshal(gotBody, &f))
	assert.Eq(t, "text", f.MsgType)
	assert.True(t, strings.Contains(f.Content["text"], "job job.terminal"))
	assert.True(t, strings.Contains(f.Content["text"], jobID))
	assert.True(t, strings.Contains(f.Content["text"], "https://gofer.example.com/jobs/"+jobID))
}

// TestWebURLWithoutBase proves notifications degrade to link-less when
// server.web_base_url is unset (the server cannot invent its public address).
func TestWebURLWithoutBase(t *testing.T) {
	s := newNotifyService(t, t.TempDir(), nil, nil)
	assert.Eq(t, "", s.webURL("/sessions?sid=x"))
	s.config().Server.WebBaseURL = "https://x.example.com/"
	assert.Eq(t, "https://x.example.com/sessions", s.webURL("sessions"))
}
