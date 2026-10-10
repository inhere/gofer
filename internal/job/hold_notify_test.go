package job

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/notify"
)

// TestHoldNotifiesApprover (gofer-9b1b): job.awaiting_approval is a default trigger, so
// a webhook with no events filter gets it — a DingTalk bot a pre-rendered 「待批准」
// message linking to the job page, a generic endpoint the {event, job} body plus that
// link. The decision events are not defaults.
func TestHoldNotifiesApprover(t *testing.T) {
	root := t.TempDir()
	s := newNotifyService(t, root, []config.WebhookConfig{
		{URL: "https://hooks.example.com/ding", Kind: "dingtalk"},
		{URL: "https://hooks.example.com/generic"},
	}, nil)
	s.config().Server.WebBaseURL = "https://gofer.example.com/"

	bodies := map[string][]byte{}
	types := map[string][]string{}
	s.postFn = func(_ context.Context, target, eventType string, body []byte, _ string, _ config.NotificationConfig) error {
		if eventType == EventJobAwaitingApproval {
			bodies[target] = body
		}
		types[target] = append(types[target], eventType)
		return nil
	}

	res, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local", Title: "push release",
		Cmd: []string{"git", "push", "origin", "main"}, Cwd: ".", TimeoutSec: 30,
		Hold: true, HoldReason: "ship v1",
	})
	if err != nil || res.Status != StatusAwaitingApproval {
		t.Fatalf("held submit = %s, %v", res.Status, err)
	}
	if n := s.DeliverDue(context.Background()); n != 2 {
		t.Fatalf("DeliverDue = %d, want one delivery per webhook", n)
	}
	link := "https://gofer.example.com/jobs/" + res.ID

	var ding struct {
		Markdown map[string]string `json:"markdown"`
	}
	if err := json.Unmarshal(bodies["https://hooks.example.com/ding"], &ding); err != nil {
		t.Fatalf("dingtalk body: %v", err)
	}
	if !strings.Contains(ding.Markdown["title"], "待批准：push release") {
		t.Fatalf("dingtalk title = %q", ding.Markdown["title"])
	}
	for _, want := range []string{"理由：ship v1", "git push origin main", "过期：", link, "去批准"} {
		if !strings.Contains(ding.Markdown["text"], want) {
			t.Fatalf("dingtalk text missing %q:\n%s", want, ding.Markdown["text"])
		}
	}

	var generic notify.Payload
	if err := json.Unmarshal(bodies["https://hooks.example.com/generic"], &generic); err != nil {
		t.Fatalf("generic body: %v", err)
	}
	if generic.Event.Type != EventJobAwaitingApproval || generic.Job.ID != res.ID || generic.Link != link {
		t.Fatalf("generic body = %+v", generic)
	}

	// The rejection's own event is not a default trigger; its terminal is.
	if _, err := s.RejectJob(res.ID, "alice", "", false); err != nil {
		t.Fatalf("RejectJob: %v", err)
	}
	s.DeliverDue(context.Background())
	for target, ts := range types {
		for _, ty := range ts {
			if ty == EventJobHoldRejected {
				t.Fatalf("%s got %s, which is not a default trigger", target, ty)
			}
		}
	}
}
