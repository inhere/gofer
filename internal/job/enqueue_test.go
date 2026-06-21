package job

import (
	"path/filepath"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
)

// newNotifyService builds a Service with an E14 notification config attached. The
// "self" project's notify gate is controlled by selfNotify (nil => default on).
func newNotifyService(t *testing.T, root string, webhooks []config.WebhookConfig, selfNotify *bool) *Service {
	t.Helper()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
				NotifyEnabled:  selfNotify,
			},
		},
		Server: config.ServerConfig{
			Notification: &config.NotificationConfig{
				Webhooks:   webhooks,
				AllowHosts: []string{"hooks.example.com"},
			},
		},
	}
	projReg := project.NewRegistry(cfg, "")
	agentReg := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	meta, err := jobstore.Open(filepath.Join(root, "gofer.db"))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	return NewService(cfg, projReg, agentReg, runners, meta, nil)
}

// TestEnqueueTerminalDelivery proves a job.terminal event enqueues a pending
// delivery for a subscribed webhook (default trigger set), and that the
// non-trigger job.running event does NOT.
func TestEnqueueTerminalDelivery(t *testing.T) {
	root := t.TempDir()
	s := newNotifyService(t, root, []config.WebhookConfig{
		{URL: "https://hooks.example.com/gofer"}, // default trigger set
	}, nil)

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("setup: expected done, got %s", final.Status)
	}

	deliveries, err := s.ListDeliveriesByJob(final.ID)
	if err != nil {
		t.Fatalf("ListDeliveriesByJob: %v", err)
	}
	// Exactly one delivery, for the terminal event (job.submitted/running are not in
	// the default trigger set; job.terminal is).
	if len(deliveries) != 1 {
		t.Fatalf("expected 1 delivery (job.terminal), got %d: %+v", len(deliveries), deliveries)
	}
	d := deliveries[0]
	if d.Status != jobstore.DeliveryPending {
		t.Errorf("delivery status = %q", d.Status)
	}
	if d.Target != "https://hooks.example.com/gofer" {
		t.Errorf("delivery target = %q", d.Target)
	}
	if d.JobID != final.ID {
		t.Errorf("delivery job_id = %q want %q", d.JobID, final.ID)
	}
}

// TestEnqueueRespectsProjectGate proves notify_enabled:false suppresses all
// deliveries for the project's jobs.
func TestEnqueueRespectsProjectGate(t *testing.T) {
	root := t.TempDir()
	off := false
	s := newNotifyService(t, root, []config.WebhookConfig{
		{URL: "https://hooks.example.com/gofer"},
	}, &off)

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("setup: expected done, got %s", final.Status)
	}
	deliveries, err := s.ListDeliveriesByJob(final.ID)
	if err != nil {
		t.Fatalf("ListDeliveriesByJob: %v", err)
	}
	if len(deliveries) != 0 {
		t.Fatalf("notify_enabled:false must enqueue nothing, got %d", len(deliveries))
	}
}

// TestEnqueueNoneWithoutConfig proves the default test service (no notification
// config) enqueues nothing — the regression guarantee (zero behaviour change).
func TestEnqueueNoneWithoutConfig(t *testing.T) {
	s := newTestService(t, t.TempDir())
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	deliveries, err := s.ListDeliveriesByJob(final.ID)
	if err != nil {
		t.Fatalf("ListDeliveriesByJob: %v", err)
	}
	if len(deliveries) != 0 {
		t.Fatalf("no notification config => no deliveries, got %d", len(deliveries))
	}
}

// failingDeliverySink fails InsertDelivery, to prove enqueue is best-effort.
type failingDeliverySink struct{ calls int }

func (f *failingDeliverySink) InsertDelivery(jobstore.Delivery) (int64, error) {
	f.calls++
	return 0, errDeliverySink
}

var errDeliverySink = errorString("boom")

type errorString string

func (e errorString) Error() string { return string(e) }

// TestEnqueueBestEffort proves a failing delivery sink does not affect the job's
// terminal state (enqueue is best-effort) yet is still invoked.
func TestEnqueueBestEffort(t *testing.T) {
	root := t.TempDir()
	s := newNotifyService(t, root, []config.WebhookConfig{
		{URL: "https://hooks.example.com/gofer"},
	}, nil)
	sink := &failingDeliverySink{}
	s.deliveries = sink

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("failing delivery sink changed terminal state: got %s", final.Status)
	}
	if sink.calls == 0 {
		t.Fatal("expected enqueue to be attempted at least once")
	}
}
