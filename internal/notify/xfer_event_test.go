package notify

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// TestXferEventNotInDefaultTriggerSet: transfer events are OPT-IN. A webhook with no
// `events` filter subscribes to the default trigger set (job.terminal / a new
// interaction / needs_review); if xfer.put were added to it, every existing webhook
// would start receiving a message for every file anyone moves. Subscribing explicitly
// must work — that is the whole point of routing transfer events through the pipeline.
func TestXferEventNotInDefaultTriggerSet(t *testing.T) {
	for _, ev := range DefaultTriggerEvents {
		if strings.HasPrefix(ev, "xfer.") {
			t.Fatalf("DefaultTriggerEvents = %v, want no transfer event in it", DefaultTriggerEvents)
		}
	}
	cfg := &config.NotificationConfig{Webhooks: []config.WebhookConfig{{URL: "https://hooks.example.test/all"}}}
	for _, ev := range []string{"xfer.put", "xfer.get"} {
		if got := MatchWebhooks(cfg, ev, "proj"); len(got) != 0 {
			t.Fatalf("a default webhook matched %s: %+v", ev, got)
		}
	}
	cfg.Webhooks[0].Events = []string{"xfer.put"}
	if got := MatchWebhooks(cfg, "xfer.put", "proj"); len(got) != 1 {
		t.Fatalf("an explicit xfer.put subscription matched %+v, want the one webhook", got)
	}
	if got := MatchWebhooks(cfg, "xfer.get", "proj"); len(got) != 0 {
		t.Fatalf("an xfer.put-only subscription matched xfer.get: %+v", got)
	}
}

// TestTransferMessageShortShape: the IM (dingtalk/feishu) rendering of a transfer is
// its own short message — who moved what, from which machine and project, how big —
// rather than an empty job line ("job xfer.put", id `xfer:<id>`), and another event
// type falls back to the job shape.
func TestTransferMessageShortShape(t *testing.T) {
	detail := `{"xfer_id":"xf-1","op":"put","runner":"w-plc","project":"shop-floor","path":"tmp/in/fw.bin","size":5242880,"by":"alice"}`
	msg, ok := TransferMessage("xfer.put", detail, 1758000000)
	if !ok {
		t.Fatal("a transfer event must render as a transfer message")
	}
	if msg.Title != "file pushed" {
		t.Fatalf("title = %q, want the push direction named", msg.Title)
	}
	for _, want := range []string{"alice", "w-plc", "shop-floor", "tmp/in/fw.bin", "5.0MB"} {
		if !strings.Contains(msg.Text, want) {
			t.Fatalf("text = %q, want it to carry %q", msg.Text, want)
		}
	}
	body, err := RenderMessage(KindDingTalk, msg)
	if err != nil {
		t.Fatalf("render dingtalk: %v", err)
	}
	var out struct {
		Markdown struct{ Text string } `json:"markdown"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode dingtalk body: %v", err)
	}
	if !strings.Contains(out.Markdown.Text, "5.0MB") {
		t.Fatalf("dingtalk body = %s, want the transfer text", body)
	}

	if _, ok := TransferMessage("job.terminal", detail, 0); ok {
		t.Fatal("a job event must not be rendered as a transfer message")
	}
}
