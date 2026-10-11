package job

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/notify"
	"github.com/inhere/gofer/internal/secret"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// newHookService is newNotifyService plus event hooks (AUTO-04).
func newHookService(t *testing.T, webhooks []config.WebhookConfig, hooks []config.EventHookConfig) *Service {
	t.Helper()
	s := newNotifyService(t, t.TempDir(), webhooks, nil)
	s.config().Server.EventHooks = hooks
	return s
}

// hookCapture is what the testcmd `hook-capture` stand-in recorded.
type hookCapture struct {
	Stdin string            `json:"stdin"`
	Cwd   string            `json:"cwd"`
	Env   map[string]string `json:"env"`
}

func readHookCapture(t *testing.T, path string) hookCapture {
	t.Helper()
	b, err := os.ReadFile(path)
	assert.Require(t, assert.NoErr(t, err))
	var c hookCapture
	assert.Require(t, assert.NoErr(t, json.Unmarshal(b, &c)))
	return c
}

func hookRows(ds []jobstore.Delivery) []jobstore.Delivery {
	var out []jobstore.Delivery
	for _, d := range ds {
		if _, ok := config.EventHookName(d.Target); ok {
			out = append(out, d)
		}
	}
	return out
}

// TestEventHookEnqueueIgnoresNotificationGates proves the job-event path enqueues a
// hook row even when notification is paused globally AND the project opted out of
// notification: those switches are for notifying people, not for hooks.
func TestEventHookEnqueueIgnoresNotificationGates(t *testing.T) {
	t.Parallel()
	off := false
	s := newHookService(t, []config.WebhookConfig{{URL: "https://hooks.example.com/gofer"}},
		[]config.EventHookConfig{{Name: "audit", Command: []string{"x"}, Events: []string{EventJobTerminal}}})
	s.config().Server.Notification.Enabled = &off
	proj := s.config().Projects["self"]
	proj.NotifyEnabled = &off
	s.config().Projects["self"] = proj

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	ds, err := s.ListDeliveriesByJob(final.ID)
	assert.Require(t, assert.NoErr(t, err))
	assert.Require(t, assert.Len(t, ds, 1))
	assert.Eq(t, "hook:audit", ds[0].Target)
	assert.Eq(t, jobstore.DeliveryPending, ds[0].Status)
	assert.Eq(t, "", ds[0].Body) // rebuilt from the event at run time, like a generic webhook
}

// TestEventHookEnqueueScopedAndNotifyEvent covers the other two enqueue paths: a
// scoped (non-job) event and a pre-rendered NotifyEvent, whose hook row carries the
// GENERIC JSON body (not an IM rendering) even when the webhook is a DingTalk bot.
func TestEventHookEnqueueScopedAndNotifyEvent(t *testing.T) {
	t.Parallel()
	s := newHookService(t,
		[]config.WebhookConfig{{URL: "https://hooks.example.com/robot", Kind: "dingtalk", Events: []string{EventSessionWaiting}}},
		[]config.EventHookConfig{{Name: "all", Command: []string{"x"}, Events: []string{"*"}}})

	s.RecordScopedEvent("xfer:t1", "xfer.put", "self", map[string]any{"path": "a.txt"})
	ds, err := s.ListDeliveriesByJob("xfer:t1")
	assert.Require(t, assert.NoErr(t, err))
	assert.Require(t, assert.Len(t, ds, 1))
	assert.Eq(t, "hook:all", ds[0].Target)

	n := s.NotifyEvent(EventSessionWaiting, "self", notify.Message{Title: "会话等待回复", Text: "选 A 还是 B"})
	assert.Eq(t, 2, n) // the DingTalk webhook + the hook
	claimed, err := s.meta.ClaimDueDeliveries(s.nowFn().Unix()+1, 50, 60)
	assert.Require(t, assert.NoErr(t, err))
	var hookBody string
	for _, d := range claimed {
		if d.Target == "hook:all" && d.EventType == EventSessionWaiting {
			hookBody = d.Body
		}
	}
	var body struct {
		Event struct {
			Type string `json:"type"`
		} `json:"event"`
		Title string `json:"title"`
		Text  string `json:"text"`
	}
	assert.Require(t, assert.NoErr(t, json.Unmarshal([]byte(hookBody), &body)))
	assert.Eq(t, EventSessionWaiting, body.Event.Type)
	assert.Eq(t, "会话等待回复", body.Title)
	assert.Eq(t, "选 A 还是 B", body.Text)
}

// TestEventHookRunsCommand runs a real hook process: stdin is exactly the generic
// webhook body, the event identity is in the env, the hook's env is layered last,
// gofer's credential variables are stripped, and the cwd is the hook's dir.
func TestEventHookRunsCommand(t *testing.T) {
	t.Setenv("GOFER_TOKEN", "operator-token")
	bin := testcmd.Path(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "capture.json")
	s := newHookService(t, nil, []config.EventHookConfig{{
		Name:    "audit",
		Command: []string{bin, "hook-capture", out, EnvHookEventType, EnvHookEventSeq, EnvHookDeliveryID, EnvHookJobID, EnvHookName, "GOFER_TOKEN", "HOOK_EXTRA"},
		Events:  []string{EventJobTerminal},
		Dir:     dir,
		Env:     map[string]string{"HOOK_EXTRA": "from-hook"},
	}})

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	ds, _ := s.ListDeliveriesByJob(final.ID)
	assert.Require(t, assert.Len(t, ds, 1))
	want, _, ok := s.buildDeliveryBody(ds[0])
	assert.Require(t, assert.True(t, ok))

	assert.Eq(t, 1, s.DeliverDue(context.Background()))
	ds, _ = s.ListDeliveriesByJob(final.ID)
	assert.Eq(t, jobstore.DeliveryDelivered, ds[0].Status)

	c := readHookCapture(t, out)
	assert.Eq(t, string(want), c.Stdin)
	assert.Eq(t, EventJobTerminal, c.Env[EnvHookEventType])
	assert.Eq(t, strconv.FormatInt(ds[0].EventSeq, 10), c.Env[EnvHookEventSeq])
	assert.Eq(t, strconv.FormatInt(ds[0].ID, 10), c.Env[EnvHookDeliveryID])
	assert.Eq(t, final.ID, c.Env[EnvHookJobID])
	assert.Eq(t, "audit", c.Env[EnvHookName])
	assert.Eq(t, "from-hook", c.Env["HOOK_EXTRA"])
	assert.NotContainsKey(t, c.Env, "GOFER_TOKEN")
	wantDir, _ := filepath.EvalSymlinks(dir)
	gotDir, _ := filepath.EvalSymlinks(c.Cwd)
	assert.Eq(t, wantDir, gotDir)
}

// TestEventHookFailureRetryThenFail: a non-zero exit retries on the webhook backoff
// table and fails at the hook's max_attempts; last_error carries the REDACTED stderr
// tail (the hook's own env values and credential-looking pairs scrubbed).
func TestEventHookFailureRetryThenFail(t *testing.T) {
	t.Parallel()
	bin := testcmd.Path(t)
	s := newHookService(t, nil, []config.EventHookConfig{{
		Name:        "flaky",
		Command:     []string{bin, "stderr-exit", "3", "boom: password=hunter22 key s3cr3t-value"},
		Events:      []string{"*"},
		Dir:         t.TempDir(),
		Env:         map[string]string{"HOOK_KEY": "s3cr3t-value"},
		MaxAttempts: 2,
	}})
	clk := &fixedClock{}
	clk.set(1000)
	s.nowFn = func() time.Time { return time.Unix(clk.now(), 0) }

	s.RecordScopedEvent("xfer:t1", "xfer.put", "self", nil)

	assert.Eq(t, 1, s.DeliverDue(context.Background()))
	ds, _ := s.ListDeliveriesByJob("xfer:t1")
	assert.Require(t, assert.Len(t, ds, 1))
	d := ds[0]
	assert.Eq(t, jobstore.DeliveryPending, d.Status)
	assert.Eq(t, 1, d.Attempts)
	assert.Eq(t, int64(1030), d.NextRetryAt)
	assert.StrContains(t, d.LastError, "exit status 3")
	assert.StrContains(t, d.LastError, "boom")
	assert.StrContains(t, d.LastError, secret.Placeholder)
	assert.StrNotContains(t, d.LastError, "hunter22")
	assert.StrNotContains(t, d.LastError, "s3cr3t-value")

	clk.set(1030)
	assert.Eq(t, 1, s.DeliverDue(context.Background()))
	ds, _ = s.ListDeliveriesByJob("xfer:t1")
	assert.Eq(t, jobstore.DeliveryFailed, ds[0].Status)
	assert.Eq(t, 2, ds[0].Attempts)
}

// TestEventHookTimeoutKills: a hook past its timeout is killed (the sweep returns
// long before the hook's own sleep would end) and the attempt is recorded.
func TestEventHookTimeoutKills(t *testing.T) {
	t.Parallel()
	bin := testcmd.Path(t)
	s := newHookService(t, nil, []config.EventHookConfig{{
		Name: "slow", Command: []string{bin, "sleep", "60s"}, Events: []string{"*"}, Dir: t.TempDir(), TimeoutSec: 1,
	}})
	s.RecordScopedEvent("xfer:t1", "xfer.put", "self", nil)

	start := time.Now()
	assert.Eq(t, 1, s.DeliverDue(context.Background()))
	assert.Lt(t, time.Since(start), 30*time.Second)
	ds, _ := s.ListDeliveriesByJob("xfer:t1")
	assert.Eq(t, jobstore.DeliveryPending, ds[0].Status)
	assert.Eq(t, 1, ds[0].Attempts)
	assert.StrContains(t, ds[0].LastError, "timeout after 1s")
}

// TestEventHookStartFailure: a command that does not exist is a failed attempt, not a
// panic or a stuck row.
func TestEventHookStartFailure(t *testing.T) {
	t.Parallel()
	s := newHookService(t, nil, []config.EventHookConfig{{
		Name: "gone", Command: []string{filepath.Join(t.TempDir(), "no-such-hook")}, Events: []string{"*"}, MaxAttempts: 1,
	}})
	s.RecordScopedEvent("xfer:t1", "xfer.put", "self", nil)
	assert.Eq(t, 1, s.DeliverDue(context.Background()))
	ds, _ := s.ListDeliveriesByJob("xfer:t1")
	assert.Eq(t, jobstore.DeliveryFailed, ds[0].Status)
	assert.StrContains(t, ds[0].LastError, "start:")
}

// TestEventHookRemovedOrDisabledAfterEnqueue: a row whose hook is gone or paused by
// the time the sweep runs is retired as failed without running anything.
func TestEventHookRemovedOrDisabledAfterEnqueue(t *testing.T) {
	t.Parallel()
	bin := testcmd.Path(t)
	marker := filepath.Join(t.TempDir(), "ran.json")
	s := newHookService(t, nil, []config.EventHookConfig{
		{Name: "a", Command: []string{bin, "hook-capture", marker}, Events: []string{"*"}},
		{Name: "b", Command: []string{bin, "hook-capture", marker}, Events: []string{"*"}},
	})
	s.RecordScopedEvent("xfer:t1", "xfer.put", "self", nil)

	off := false
	cfg := s.config()
	cfg.Server.EventHooks = []config.EventHookConfig{
		{Name: "b", Command: []string{bin, "hook-capture", marker}, Events: []string{"*"}, Enabled: &off},
	}
	assert.Eq(t, 2, s.DeliverDue(context.Background()))
	ds, _ := s.ListDeliveriesByJob("xfer:t1")
	assert.Require(t, assert.Len(t, ds, 2))
	got := map[string]string{}
	for _, d := range ds {
		assert.Eq(t, jobstore.DeliveryFailed, d.Status)
		got[d.Target] = d.LastError
	}
	assert.Eq(t, "hook removed", got["hook:a"])
	assert.Eq(t, "hook disabled", got["hook:b"])
	assert.FileNotExists(t, marker)
}

// TestEventHookSlowDoesNotBlockWebhook proves hook rows run beside the webhook POSTs:
// the hook (claimed FIRST) waits for a file only the webhook POST creates. Were the
// sweep serial, the hook would time out waiting and the row would not be delivered.
func TestEventHookSlowDoesNotBlockWebhook(t *testing.T) {
	t.Parallel()
	bin := testcmd.Path(t)
	flag := filepath.Join(t.TempDir(), "posted")
	s := newHookService(t, []config.WebhookConfig{{URL: "https://hooks.example.com/gofer", Events: []string{"*"}}},
		[]config.EventHookConfig{{
			Name: "slow", Command: []string{bin, "wait-file", flag, "20s"}, Events: []string{"*"}, Dir: t.TempDir(), TimeoutSec: 30,
		}})
	s.postFn = func(_ context.Context, _, _ string, _ []byte, _ string, _ config.NotificationConfig) error {
		return os.WriteFile(flag, []byte("1"), 0o644)
	}
	now := s.nowFn().Unix()
	hookID, err := s.meta.InsertDelivery(jobstore.Delivery{Target: "hook:slow", Body: `{}`, EventType: "x.y", NextRetryAt: now, CreatedAt: now})
	assert.Require(t, assert.NoErr(t, err))
	_, err = s.meta.InsertDelivery(jobstore.Delivery{Target: "https://hooks.example.com/gofer", Body: `{}`, EventType: "x.y", NextRetryAt: now, CreatedAt: now})
	assert.Require(t, assert.NoErr(t, err))

	assert.Eq(t, 2, s.DeliverDue(context.Background()))
	ds, err := s.ListDeliveriesByJob("") // pre-rendered rows carry no job id
	assert.Require(t, assert.NoErr(t, err))
	assert.Require(t, assert.Len(t, ds, 2))
	for _, d := range ds {
		assert.Eq(t, jobstore.DeliveryDelivered, d.Status, "row %d (%s): %s", d.ID, d.Target, d.LastError)
	}
	assert.Eq(t, hookID, ds[0].ID)
}
