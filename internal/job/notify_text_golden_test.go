package job

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/notify"
)

// The golden files under testdata/notify_golden pin the notification text the job
// service builds (session notices, the update notice, the approval / interaction /
// session-reply messages and the headline of a job event). The *.zh-CN.txt files were
// recorded from the code BEFORE it was migrated into the catalog; the *.en.txt files
// are the English rewrite. GOFER_GOLDEN_UPDATE=1 rewrites the English ones (the
// zh-CN ones only when they do not exist yet).

var notifyTextCJK = regexp.MustCompile(`[\p{Han}]`)

// feishuText decodes a Feishu payload to the text a person reads.
func feishuText(t *testing.T, body []byte) string {
	t.Helper()
	var f struct {
		Content map[string]string `json:"content"`
	}
	if err := json.Unmarshal(body, &f); err != nil {
		t.Fatal(err)
	}
	return f.Content["text"]
}

func jobNotifyTexts(t *testing.T, lang i18n.Lang) map[string]string {
	t.Helper()
	// The idle / expiry clock times are local; pin the zone so the golden is stable.
	oldLocal := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = oldLocal })

	events := []string{EventSessionWaiting, EventSessionAttention, EventSessionHandedOff,
		EventSessionTakeoverReleased, EventSessionNudgePaused, notify.EventUpdateAvailable}
	s := newNotifyService(t, t.TempDir(), []config.WebhookConfig{{
		URL: "https://hooks.example.com/gofer", Kind: "feishu", Events: events,
	}}, nil)
	cfg := s.config()
	cfg.Server.WebBaseURL = "https://gofer.example.com"
	cfg.Server.Language = string(lang)

	var bodies []string
	s.postFn = func(_ context.Context, _, _, _ string, body []byte, _ string, _ config.NotificationConfig) error {
		bodies = append(bodies, feishuText(t, body))
		return nil
	}
	sent := func(fn func()) string {
		bodies = nil
		fn()
		s.DeliverDue(context.Background())
		return strings.Join(bodies, "\n=====\n") + "\n"
	}

	out := map[string]string{}
	out["session_waiting"] = sent(func() { s.NotifySessionWaiting("sid-123456789", "self", "repo: fix", "pick A or B?", 3) })
	out["session_attention"] = sent(func() { s.NotifySessionAttention("sid-123456789", "self", "", "") })
	out["session_attention_detail"] = sent(func() { s.NotifySessionAttention("sid-123456789", "self", "repo: fix", "needs a choice") })
	out["session_handed_off"] = sent(func() { s.NotifySessionHandedOff("sid-123456789", "self", "repo: fix", "job-1") })
	out["session_takeover_released"] = sent(func() {
		s.NotifySessionTakeoverReleased("sid-123456789", "self", "repo: fix", "job-20261021-212412-6a715d34", "job_done")
	})
	out["session_nudge_paused"] = sent(func() {
		s.NotifySessionNudgePaused("sid-123456789", "self", "repo: fix", "ng-1", "3 failures in a row")
	})
	out["update_available"] = sent(func() { s.NotifyUpdateAvailable("v1.0.0", "v1.1.0", "notes here") })
	out["update_available_bare"] = sent(func() { s.NotifyUpdateAvailable("v1.0.0", "v1.1.0", "") })

	// Messages built from a job (the desktop channel and the delivery sweeper share them).
	jr := JobResult{ID: "job-1", Title: "Deploy", ProjectKey: "self", Agent: "codex", Runner: "local",
		Hold: &HoldState{Reason: "needs review", Command: []string{"go", "test"}, ExpiresAt: 1767366240}}
	var b strings.Builder
	for _, ev := range []string{EventJobAwaitingApproval, EventSessionAwaitingReply, EventInteractionCreated, EventJobNeedsReview, "job.terminal"} {
		detail := `{"turn_no":2,"idle_deadline_at":1767366240,"title":"Fix it","reply_preview":"pick one","prompt":""}`
		m, _ := s.immediateMessage(jr, ev, detail, 5)
		b.WriteString(ev + "\n" + describeNotify(m) + "\n")
	}
	out["immediate_messages"] = b.String()

	var r strings.Builder
	for _, reason := range []string{"", "manual_end", "idle_timeout", "max_session_timeout", "boom"} {
		r.WriteString(jobEventText(lang, notify.JobSummary{ID: "job-1", Project: "self", Status: "done", Agent: "codex", Runner: "local", SessionEndReason: reason}) + "\n")
	}
	out["job_event_text"] = r.String()
	return out
}

func describeNotify(m notify.Message) string {
	return "Title: " + m.Title + "\nText: " + m.Text + "\nLink: " + m.Link + "\nLinkLabel: " + m.LinkLabel + "\n"
}

func TestJobNotifyTextGolden(t *testing.T) {
	for _, lang := range i18n.Supported() {
		t.Run(string(lang), func(t *testing.T) {
			update := os.Getenv("GOFER_GOLDEN_UPDATE") != ""
			for name, got := range jobNotifyTexts(t, lang) {
				file := filepath.Join("testdata", "notify_golden", name+"."+string(lang)+".txt")
				raw, err := os.ReadFile(file)
				if err != nil && lang == i18n.ZhCN {
					t.Fatalf("%s: the zh-CN golden is the recorded original and must exist: %v", file, err)
				}
				if err != nil || (update && lang != i18n.ZhCN) {
					if werr := os.WriteFile(file, []byte(got), 0o644); werr != nil {
						t.Fatal(werr)
					}
					if err != nil {
						t.Errorf("%s did not exist: written with the current text, review and commit it", file)
					}
					continue
				}
				if got != string(raw) {
					t.Errorf("%s differs from its golden file\n--- got ---\n%s\n--- want ---\n%s", file, got, raw)
				}
				if lang == i18n.EN && notifyTextCJK.MatchString(got) {
					t.Errorf("%s: the English text carries CJK:\n%s", name, got)
				}
			}
		})
	}
}

// TestNotifyLanguageFollowsProjectThenServer: a notification has no request, so its
// words follow the project it belongs to (projects.<key>.language), else the server's
// language; a server-wide notice (no project) uses the server's. The renderer's own
// words (default link label, truncation marker) follow the same language.
func TestNotifyLanguageFollowsProjectThenServer(t *testing.T) {
	s := newNotifyService(t, t.TempDir(), []config.WebhookConfig{{
		URL: "https://hooks.example.com/gofer", Kind: "feishu",
		Events: []string{EventSessionAttention, notify.EventUpdateAvailable, "x.custom"},
	}}, nil)
	cfg := s.config()
	cfg.Server.Language = "en"
	proj := cfg.Projects["self"]
	proj.Language = "zh-CN"
	cfg.Projects["self"] = proj
	cfg.Projects["other"] = config.ProjectConfig{HostPath: proj.HostPath, AllowedAgents: []string{"exec"}, AllowedRunners: []string{"local"}, AllowExec: true}

	var bodies []string
	s.postFn = func(_ context.Context, _, _, _ string, body []byte, _ string, _ config.NotificationConfig) error {
		bodies = append(bodies, feishuText(t, body))
		return nil
	}
	send := func(fn func()) string {
		bodies = nil
		fn()
		s.DeliverDue(context.Background())
		return strings.Join(bodies, "")
	}
	if got := send(func() { s.NotifySessionAttention("sid-1", "self", "t", "") }); !strings.Contains(got, "会话需要确认") {
		t.Errorf("project zh-CN under server en = %q, want Chinese", got)
	}
	if got := send(func() { s.NotifySessionAttention("sid-1", "other", "t", "") }); !strings.Contains(got, "Session needs confirmation") || notifyTextCJK.MatchString(got) {
		t.Errorf("project without a language under server en = %q, want English", got)
	}
	if got := send(func() { s.NotifyUpdateAvailable("v1", "v2", "") }); !strings.Contains(got, "New gofer version v2") || notifyTextCJK.MatchString(got) {
		t.Errorf("server-wide notice under server en = %q, want English", got)
	}
	// A message built elsewhere (no Lang) still gets its link label and marker in the
	// notification's language.
	long := notify.Message{Title: "T", Text: strings.Repeat("x", 40), Link: "https://x/y"}
	cfg.Server.Notification.Webhooks[0].MaxTextRunes = 5
	got := send(func() { s.NotifyEvent("x.custom", "self", long) })
	if !strings.Contains(got, "已截断") || !strings.Contains(got, "在 gofer 打开") {
		t.Errorf("default words under project zh-CN = %q, want Chinese", got)
	}
	got = send(func() { s.NotifyEvent("x.custom", "other", long) })
	if notifyTextCJK.MatchString(got) || !strings.Contains(got, "Open in gofer") {
		t.Errorf("default words under server en = %q, want English", got)
	}
}

// TestJobEventDeliveryFollowsProjectLanguage: the delivery sweeper rebuilds a job event's
// IM message at post time; its link label follows the job's project language.
func TestJobEventDeliveryFollowsProjectLanguage(t *testing.T) {
	for lang, want := range map[string]string{"en": "View job: https://gofer.example.com/jobs/", "zh-CN": "查看 job：https://gofer.example.com/jobs/"} {
		t.Run(lang, func(t *testing.T) {
			s := newNotifyService(t, t.TempDir(), []config.WebhookConfig{{URL: "https://hooks.example.com/gofer", Kind: "feishu"}}, nil)
			cfg := s.config()
			cfg.Server.WebBaseURL = "https://gofer.example.com"
			proj := cfg.Projects["self"]
			proj.Language = lang
			cfg.Projects["self"] = proj
			var got string
			s.postFn = func(_ context.Context, _, _, _ string, body []byte, _ string, _ config.NotificationConfig) error {
				got = feishuText(t, body)
				return nil
			}
			submitDeliveredJob(t, s)
			if s.DeliverDue(context.Background()) != 1 || !strings.Contains(got, want) {
				t.Errorf("delivered text = %q, want it to contain %q", got, want)
			}
		})
	}
}
