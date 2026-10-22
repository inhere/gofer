package job

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/notify"
)

// TestDeliverDueProviderErrorIsFinal proves gofer-map1: an IM bot's business
// rejection (HTTP 200 + errcode 310000 / code 19024 — keyword not matched, bad
// 加签 signature, invalid token) fails the delivery on the FIRST attempt instead
// of burning the whole backoff table, and the provider's own description is what
// the delivery record keeps.
func TestDeliverDueProviderErrorIsFinal(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		webhook config.WebhookConfig
		perr    *notify.ProviderError
		want    []string
	}{
		{
			name:    "dingtalk keyword mismatch",
			webhook: config.WebhookConfig{URL: "https://hooks.example.com/dingtalk", Kind: "dingtalk"},
			perr: &notify.ProviderError{Endpoint: notify.KindDingTalk, Code: 310000,
				Message: "keywords not in content", Permanent: true},
			want: []string{"310000", "keywords not in content"},
		},
		{
			name:    "feishu keyword not found",
			webhook: config.WebhookConfig{URL: "https://hooks.example.com/feishu", Kind: "feishu"},
			perr: &notify.ProviderError{Endpoint: notify.KindFeishu, Code: 19024,
				Message: "Key Words Not Found", Permanent: true},
			want: []string{"19024", "Key Words Not Found"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newNotifyService(t, t.TempDir(), []config.WebhookConfig{tc.webhook}, nil)
			s.config().Server.Notification.MaxAttempts = 5

			clk := &fixedClock{}
			clk.set(1000)
			s.nowFn = func() time.Time { return time.Unix(clk.now(), 0) }

			var calls atomic.Int64
			s.postFn = func(_ context.Context, kind, _, _ string, _ []byte, _ string, _ config.NotificationConfig) error {
				calls.Add(1)
				if kind != normalizeKind(tc.webhook.Kind) {
					t.Errorf("post kind = %q, want %q", kind, tc.webhook.Kind)
				}
				return tc.perr
			}

			jobID, _ := submitDeliveredJob(t, s)
			if got := s.DeliverDue(context.Background()); got != 1 {
				t.Fatalf("DeliverDue processed %d, want 1", got)
			}
			d := mustOneDelivery(t, s, jobID)
			if d.Status != jobstore.DeliveryFailed {
				t.Fatalf("status = %q, want failed (a permanent refusal must not retry)", d.Status)
			}
			if d.Attempts != 1 {
				t.Errorf("attempts = %d, want 1", d.Attempts)
			}
			for _, want := range tc.want {
				if !strings.Contains(d.LastError, want) {
					t.Errorf("last_error = %q, want it to carry %q", d.LastError, want)
				}
			}
			// Never claimed again, however long we wait.
			clk.set(1_000_000)
			if got := s.DeliverDue(context.Background()); got != 0 {
				t.Fatalf("sweep after the final failure processed %d, want 0", got)
			}
			if calls.Load() != 1 {
				t.Fatalf("post attempted %d times, want exactly 1", calls.Load())
			}
		})
	}
}

// TestDeliverDueRateLimitRetries proves a throttling business code (DingTalk
// 130101 / Feishu 11232) keeps the existing backoff + retry flow and only ends
// failed once the attempt cap is reached.
func TestDeliverDueRateLimitRetries(t *testing.T) {
	t.Parallel()
	s := newNotifyService(t, t.TempDir(), []config.WebhookConfig{
		{URL: "https://hooks.example.com/dingtalk", Kind: "dingtalk"},
	}, nil)
	s.config().Server.Notification.MaxAttempts = 3

	clk := &fixedClock{}
	clk.set(1000)
	s.nowFn = func() time.Time { return time.Unix(clk.now(), 0) }

	var calls atomic.Int64
	s.postFn = func(_ context.Context, _, _, _ string, _ []byte, _ string, _ config.NotificationConfig) error {
		calls.Add(1)
		return &notify.ProviderError{Endpoint: notify.KindDingTalk, Code: 130101,
			Message: "send too fast", Permanent: false}
	}

	jobID, _ := submitDeliveredJob(t, s)

	if got := s.DeliverDue(context.Background()); got != 1 {
		t.Fatalf("sweep1 processed %d, want 1", got)
	}
	d := mustOneDelivery(t, s, jobID)
	if d.Status != jobstore.DeliveryPending || d.Attempts != 1 || d.NextRetryAt != 1030 {
		t.Fatalf("after sweep1: status=%s attempts=%d next=%d (want pending/1/1030)", d.Status, d.Attempts, d.NextRetryAt)
	}
	if !strings.Contains(d.LastError, "130101") || !strings.Contains(d.LastError, "send too fast") {
		t.Errorf("last_error = %q, want the provider code + text", d.LastError)
	}

	clk.set(1030)
	if got := s.DeliverDue(context.Background()); got != 1 {
		t.Fatalf("sweep2 processed %d, want 1", got)
	}
	d = mustOneDelivery(t, s, jobID)
	if d.Status != jobstore.DeliveryPending || d.Attempts != 2 {
		t.Fatalf("after sweep2: status=%s attempts=%d (want pending/2)", d.Status, d.Attempts)
	}

	clk.set(1150)
	if got := s.DeliverDue(context.Background()); got != 1 {
		t.Fatalf("sweep3 processed %d, want 1", got)
	}
	d = mustOneDelivery(t, s, jobID)
	if d.Status != jobstore.DeliveryFailed || d.Attempts != 3 {
		t.Fatalf("after sweep3: status=%s attempts=%d (want failed/3)", d.Status, d.Attempts)
	}
	if calls.Load() != 3 {
		t.Fatalf("post attempted %d times, want 3", calls.Load())
	}
}

// TestDeliverDueProviderSuccessAndKeyword proves the other two halves of
// gofer-map1: a delivered envelope (errcode 0) marks the row delivered, and the
// webhook's keyword reaches the posted IM body so a 自定义关键词 robot accepts it.
func TestDeliverDueProviderSuccessAndKeyword(t *testing.T) {
	t.Parallel()
	s := newNotifyService(t, t.TempDir(), []config.WebhookConfig{
		{URL: "https://hooks.example.com/dingtalk", Kind: "dingtalk", Keyword: "gofer"},
	}, nil)

	var gotBody []byte
	s.postFn = func(_ context.Context, _, _, _ string, body []byte, _ string, _ config.NotificationConfig) error {
		gotBody = body
		// Exactly what notify.PostWebhook returns for {"errcode":0,"errmsg":"ok"}.
		return notify.CheckProviderResponse(notify.KindDingTalk, []byte(`{"errcode":0,"errmsg":"ok"}`))
	}

	jobID, _ := submitDeliveredJob(t, s)
	if got := s.DeliverDue(context.Background()); got != 1 {
		t.Fatalf("DeliverDue processed %d, want 1", got)
	}
	d := mustOneDelivery(t, s, jobID)
	if d.Status != jobstore.DeliveryDelivered {
		t.Fatalf("status = %q, want delivered", d.Status)
	}

	var out struct {
		Msgtype  string                       `json:"msgtype"`
		Markdown struct{ Title, Text string } `json:"markdown"`
	}
	if err := json.Unmarshal(gotBody, &out); err != nil {
		t.Fatalf("posted body is not the dingtalk envelope: %v (%s)", err, gotBody)
	}
	if !strings.HasPrefix(out.Markdown.Title, "gofer ") {
		t.Errorf("markdown.title = %q, want the keyword prefix", out.Markdown.Title)
	}
	if !strings.Contains(out.Markdown.Text, "gofer ") {
		t.Errorf("markdown.text = %q, want the keyword in the body too", out.Markdown.Text)
	}
}

// normalizeKind mirrors notify.NormalizeKind for the kind assertion above.
func normalizeKind(k string) string { return notify.NormalizeKind(k) }
