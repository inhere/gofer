package job

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/config"
)

func TestSessionReplyPreviewUsesNotifyLimit(t *testing.T) {
	dir := t.TempDir()
	content := "--- turn 3 ---\n" + strings.Repeat("回复", 80)
	if err := os.WriteFile(filepath.Join(dir, "stdout.log"), []byte(content), 0o600); err != nil {
		t.Fatalf("write stdout: %v", err)
	}
	got := sessionReplyPreview(dir, 3, 64)
	if len([]rune(got)) != 64 {
		t.Fatalf("preview rune count = %d, want 64", len([]rune(got)))
	}
	if strings.Contains(got, "turn 3") {
		t.Fatalf("preview retained turn marker: %q", got)
	}
}

func configureSessionReplyNotify(s *Service, delay int) {
	s.config().Server.WebBaseURL = "https://gofer.example"
	s.config().Server.Notification = &config.NotificationConfig{
		SessionReplyDelaySec: &delay,
		Webhooks:             []config.WebhookConfig{{URL: "https://hooks.example.com/y2", Events: []string{EventSessionAwaitingReply}}},
		AllowHosts:           []string{"hooks.example.com"},
	}
}

func waitAwaitingInput(t *testing.T, s *Service, id string, turn int) JobResult {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, ok := s.Get(id)
		if ok && got.Status == StatusAwaitingInput && (turn == 0 || got.TurnNo == turn) {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s did not await input", id)
	return JobResult{}
}

func TestSessionAwaitingReplyNotifiesAfterDelay(t *testing.T) {
	s := newACPService(t, t.TempDir(), acptest.Options{})
	configureSessionReplyNotify(s, 1)
	result, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".", Prompt: "hello", Session: true, TimeoutSec: 30, IdleTimeoutSec: 10, Title: "测试会话"})
	if err != nil {
		t.Fatal(err)
	}
	waitAwaitingInput(t, s, result.ID, 1)
	time.Sleep(1200 * time.Millisecond)
	deliveries, err := s.ListDeliveriesByJob(result.ID)
	if err != nil {
		t.Fatal(err)
	}
	events, _ := s.ListJobEvents(result.ID, 0)
	if len(deliveries) != 1 || deliveries[0].EventSeq == 0 || len(events) == 0 || events[len(events)-1].Type != EventSessionAwaitingReply {
		t.Fatalf("deliveries = %+v", deliveries)
	}
}

func TestSessionAwaitingReplyCancelledBySay(t *testing.T) {
	s := newACPService(t, t.TempDir(), acptest.Options{})
	configureSessionReplyNotify(s, 1)
	result, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".", Prompt: "hello", Session: true, TimeoutSec: 30, IdleTimeoutSec: 10})
	if err != nil {
		t.Fatal(err)
	}
	waitAwaitingInput(t, s, result.ID, 1)
	if err := s.SaySession(result.ID, "again"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	deliveries, err := s.ListDeliveriesByJob(result.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 0 {
		t.Fatalf("say should cancel the pending reminder: %+v", deliveries)
	}
	_ = s.Cancel(result.ID)
}

func TestSessionAwaitingReplyOncePerWait(t *testing.T) {
	s := newACPService(t, t.TempDir(), acptest.Options{})
	configureSessionReplyNotify(s, 1)
	result, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".", Prompt: "hello", Session: true, TimeoutSec: 30, IdleTimeoutSec: 10})
	if err != nil {
		t.Fatal(err)
	}
	waitAwaitingInput(t, s, result.ID, 1)
	time.Sleep(1200 * time.Millisecond)
	if err := s.SaySession(result.ID, "again"); err != nil {
		t.Fatal(err)
	}
	waitAwaitingInput(t, s, result.ID, 2)
	time.Sleep(1200 * time.Millisecond)
	deliveries, err := s.ListDeliveriesByJob(result.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 2 {
		t.Fatalf("want one reminder per wait, got %+v", deliveries)
	}
	_ = s.Cancel(result.ID)
}

func TestSessionManualEndNotNotified(t *testing.T) {
	s := newACPService(t, t.TempDir(), acptest.Options{})
	delay := 0
	s.config().Server.Notification = &config.NotificationConfig{
		SessionReplyDelaySec: &delay,
		Webhooks:             []config.WebhookConfig{{URL: "https://hooks.example.com/y2"}},
		AllowHosts:           []string{"hooks.example.com"},
	}
	result, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".", Prompt: "hello", Session: true, TimeoutSec: 30, IdleTimeoutSec: 10})
	if err != nil {
		t.Fatal(err)
	}
	waitAwaitingInput(t, s, result.ID, 1)
	if err := s.EndSession(result.ID); err != nil {
		t.Fatal(err)
	}
	final, ok := s.Wait(result.ID)
	if !ok || final.SessionEndReason != "manual_end" {
		t.Fatalf("final = %+v", final)
	}
	deliveries, err := s.ListDeliveriesByJob(result.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 0 {
		t.Fatalf("manual_end should not notify: %+v", deliveries)
	}
}
