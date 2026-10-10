package job

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/testutil/wait"
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
	var got JobResult
	wait.For(t, 5*time.Second, "job "+id+" awaiting input", func() (bool, any) {
		var ok bool
		got, ok = s.Get(id)
		return ok && got.Status == StatusAwaitingInput && (turn == 0 || got.TurnNo == turn), got.Status
	})
	return got
}

// waitDeliveries polls until the job has want webhook deliveries (the reminder is a
// 1s timer: a fixed sleep after it left the timer ~200ms under load).
func waitDeliveries(t *testing.T, s *Service, id string, want int) {
	t.Helper()
	wait.For(t, 5*time.Second, "session reply reminder deliveries", func() (bool, any) {
		deliveries, err := s.ListDeliveriesByJob(id)
		return err == nil && len(deliveries) >= want, len(deliveries)
	})
}

func TestSessionAwaitingReplyNotifiesAfterDelay(t *testing.T) {
	s := newACPService(t, t.TempDir(), acptest.Options{})
	configureSessionReplyNotify(s, 1)
	result, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".", Prompt: "hello", Session: true, TimeoutSec: 30, IdleTimeoutSec: 10, Title: "测试会话"})
	if err != nil {
		t.Fatal(err)
	}
	waitAwaitingInput(t, s, result.ID, 1)
	waitDeliveries(t, s, result.ID, 1)
	deliveries, err := s.ListDeliveriesByJob(result.ID)
	if err != nil {
		t.Fatal(err)
	}
	events, _ := s.ListJobEvents(result.ID, 0)
	reminded := false
	for _, ev := range events {
		reminded = reminded || ev.Type == EventSessionAwaitingReply
	}
	if len(deliveries) != 1 || deliveries[0].EventSeq == 0 || !reminded {
		t.Fatalf("deliveries = %+v", deliveries)
	}
}

func TestSessionAwaitingReplyCancelledBySay(t *testing.T) {
	s := newACPService(t, t.TempDir(), acptest.Options{})
	// The say must land before the reminder is due; a delay well above the test's own
	// latency keeps a slow machine from turning that into a race.
	configureSessionReplyNotify(s, int(5*wait.Scale()))
	result, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".", Prompt: "hello", Session: true, TimeoutSec: 30, IdleTimeoutSec: 10})
	if err != nil {
		t.Fatal(err)
	}
	waitAwaitingInput(t, s, result.ID, 1)
	// The reminder is armed just after the status flips to awaiting_input.
	entry := s.entry(result.ID)
	var generation uint64
	wait.Until(t, 5*time.Second, "reminder armed", func() bool {
		entry.mu.Lock()
		defer entry.mu.Unlock()
		generation = entry.awaitReplyGeneration
		return entry.awaitReplyTimer != nil
	})
	if err := s.SaySession(result.ID, "again"); err != nil {
		t.Fatal(err)
	}
	entry.mu.Lock()
	superseded := entry.awaitReplyGeneration != generation
	entry.mu.Unlock()
	if !superseded {
		t.Fatal("say left the pending reminder in force")
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
	waitDeliveries(t, s, result.ID, 1)
	if err := s.SaySession(result.ID, "again"); err != nil {
		t.Fatal(err)
	}
	waitAwaitingInput(t, s, result.ID, 2)
	waitDeliveries(t, s, result.ID, 2)
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
