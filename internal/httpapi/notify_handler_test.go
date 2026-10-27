package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/desktop"
	"github.com/inhere/gofer/internal/notify"
)

// notifyTestServer is the handler-level fixture: one generic and one dingtalk webhook, plus
// a desktop channel, and the two outbound seams replaced so nothing leaves the process.
func notifyTestServer(t *testing.T, cfg *config.Config) *Server {
	t.Helper()
	if cfg.Server.Notification == nil {
		cfg.Server.Notification = &config.NotificationConfig{}
	}
	return newConfigViewTestServer(t, cfg)
}

// TestNotifyEventsCatalog pins the catalog endpoint: it is generated from internal/notify
// (not from a second list in the console), every default event is described, and the
// desktop capability is reported so the console can say whether the platform pops at all.
func TestNotifyEventsCatalog(t *testing.T) {
	t.Parallel()
	s := notifyTestServer(t, &config.Config{Server: config.ServerConfig{Token: "tk"}})

	resp := do(t, s, http.MethodGet, "/v1/notify/events", "tk", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200: %s", resp.StatusCode, bodyText(t, resp))
	}
	var got struct {
		Events []notify.EventInfo `json:"events"`
		Groups []notify.EventGroup
		Desktop struct {
			Supported       bool `json:"supported"`
			MergeSecDefault int  `json:"merge_sec_default"`
		}
	}
	decode(t, resp, &got)
	if len(got.Events) == 0 || len(got.Groups) == 0 {
		t.Fatalf("catalog is empty: %+v", got)
	}
	var terminal *notify.EventInfo
	for i := range got.Events {
		e := got.Events[i]
		if e.Zh == "" || e.En == "" || e.Group == "" {
			t.Fatalf("event %q is missing a group or a description: %+v", e.Key, e)
		}
		if e.Key == "job.terminal" {
			terminal = &got.Events[i]
		}
	}
	// job.terminal is in DefaultTriggerEvents, so the flag must come through as true.
	if terminal == nil || !terminal.Default {
		t.Fatalf("job.terminal = %+v, want a default catalog entry", terminal)
	}
	if got.Desktop.MergeSecDefault != config.DefaultDesktopMergeSec {
		t.Fatalf("merge_sec_default=%d, want %d", got.Desktop.MergeSecDefault, config.DefaultDesktopMergeSec)
	}
	if got.Desktop.Supported != desktop.PlatformSupported() {
		t.Fatalf("desktop.supported=%v, want the platform's %v", got.Desktop.Supported, desktop.PlatformSupported())
	}
}

// The catalog is a config READ: a caller holding config.read (every seeded user, unlike
// config.write which the fixture keeps root-only) can render the pickers.
func TestNotifyEventsRequiresConfigRead(t *testing.T) {
	t.Parallel()
	yamlText, _, _ := configWriteFixture(t)
	s, _, _ := newConfigWriteTestServer(t, yamlText, agent.NoopDetector{})
	resp := do(t, s, http.MethodGet, "/v1/notify/events", opToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200 for a config.read caller: %s", resp.StatusCode, bodyText(t, resp))
	}
}

// TestNotifyTestWebhook drives the test button: the configured target is rendered for its
// kind and POSTed through the seam, and the response reports the delivered target.
func TestNotifyTestWebhook(t *testing.T) {
	// No t.Parallel: the 加签 secret comes from a process environment variable (t.Setenv).
	cfg := &config.Config{
		Server: config.ServerConfig{
			Token:      "tk",
			WebBaseURL: "https://gofer.example.test/",
			Notification: &config.NotificationConfig{Webhooks: []config.WebhookConfig{{
				URL:      "https://oapi.dingtalk.com/robot/send?access_token=tok",
				Kind:     config.WebhookKindDingTalk,
				Keyword:  "gofer",
				SecretEnv: "HOOK_SECRET",
			}}},
		},
	}
	s := notifyTestServer(t, cfg)
	t.Setenv("HOOK_SECRET", "SECxyz")

	var gotKind, gotTarget, gotType, gotSecret string
	var gotBody []byte
	s.notifyPost = func(_ context.Context, kind, target, eventType string, body []byte, secretValue string, _ config.NotificationConfig) error {
		gotKind, gotTarget, gotType, gotSecret, gotBody = kind, target, eventType, secretValue, body
		return nil
	}

	resp := do(t, s, http.MethodPost, "/v1/notify/test", "tk", map[string]any{"target": "webhook", "index": 0})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200: %s", resp.StatusCode, bodyText(t, resp))
	}
	var got struct {
		OK     bool   `json:"ok"`
		Target string `json:"target"`
		Index  int    `json:"index"`
		Kind   string `json:"kind"`
		Error  string `json:"error"`
	}
	decode(t, resp, &got)
	if !got.OK || got.Target != "webhook" || got.Index != 0 || got.Kind != notify.KindDingTalk || got.Error != "" {
		t.Fatalf("response = %+v, want a delivered dingtalk test", got)
	}
	if gotType != EventNotifyTest || gotKind != notify.KindDingTalk {
		t.Fatalf("POST event=%q kind=%q, want %q / %q", gotType, gotKind, EventNotifyTest, notify.KindDingTalk)
	}
	// DingTalk signs in the URL, so the HMAC secret must NOT also ride the header.
	if gotSecret != "" {
		t.Fatalf("hmac secret=%q, want empty for an IM kind", gotSecret)
	}
	if !strings.Contains(gotTarget, "sign=") {
		t.Fatalf("target=%q, want the 加签 query param applied", gotTarget)
	}
	var payload map[string]any
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatalf("rendered body is not JSON: %v: %s", err, gotBody)
	}
	if payload["msgtype"] == nil {
		t.Fatalf("rendered body = %s, want a dingtalk message", gotBody)
	}
}

// A provider business refusal (keyword not matched, bad signature) is REPORTED, not turned
// into a 5xx: the operator needs the bot's own words.
func TestNotifyTestWebhookProviderError(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Server: config.ServerConfig{
		Token:        "tk",
		Notification: &config.NotificationConfig{Webhooks: []config.WebhookConfig{{URL: "https://hooks.example.test/robot", Kind: config.WebhookKindFeishu}}},
	}}
	s := notifyTestServer(t, cfg)
	s.notifyPost = func(context.Context, string, string, string, []byte, string, config.NotificationConfig) error {
		return &notify.ProviderError{Endpoint: notify.KindFeishu, Code: 19024, Message: "Key Words Not Found", Permanent: true}
	}

	resp := do(t, s, http.MethodPost, "/v1/notify/test", "tk", map[string]any{"target": "webhook"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200: %s", resp.StatusCode, bodyText(t, resp))
	}
	var got struct {
		OK        bool   `json:"ok"`
		Error     string `json:"error"`
	}
	decode(t, resp, &got)
	if got.OK || !strings.Contains(got.Error, "Key Words Not Found") || !strings.Contains(got.Error, "19024") {
		t.Fatalf("response = %+v, want the provider's own error", got)
	}
}

// An index that names no configured webhook is a bad REQUEST (400), not a failed send.
func TestNotifyTestWebhookUnknownIndex(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Server: config.ServerConfig{
		Token:        "tk",
		Notification: &config.NotificationConfig{Webhooks: []config.WebhookConfig{{URL: "https://hooks.example.test/a"}}},
	}}
	s := notifyTestServer(t, cfg)
	s.notifyPost = func(context.Context, string, string, string, []byte, string, config.NotificationConfig) error {
		t.Error("no POST expected for an unknown index")
		return nil
	}
	for _, body := range []map[string]any{
		{"target": "webhook", "index": 3},
		{"target": "webhook", "index": -1},
		{"target": "carrier-pigeon"},
	} {
		resp := do(t, s, http.MethodPost, "/v1/notify/test", "tk", body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("body=%v: status=%d, want 400: %s", body, resp.StatusCode, bodyText(t, resp))
		}
	}
	// No notification block at all: nothing to send, still a 400 rather than a panic.
	s = notifyTestServer(t, &config.Config{Server: config.ServerConfig{Token: "tk"}})
	resp := do(t, s, http.MethodPost, "/v1/notify/test", "tk", map[string]any{"target": "webhook", "index": 0})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("no notification config: status=%d, want 400", resp.StatusCode)
	}
}

// The desktop target reports both outcomes: shown, and "this machine cannot show it"
// (a SkipError), which the console renders as a platform limitation.
func TestNotifyTestDesktop(t *testing.T) {
	t.Parallel()
	s := notifyTestServer(t, &config.Config{Server: config.ServerConfig{Token: "tk"}})

	var shown desktop.Notification
	s.notifyDesktop = func(n desktop.Notification) error {
		shown = n
		return nil
	}
	resp := do(t, s, http.MethodPost, "/v1/notify/test", "tk", map[string]any{"target": "desktop"})
	type desktopResp struct {
		OK      bool   `json:"ok"`
		Target  string `json:"target"`
		Skipped bool   `json:"skipped"`
		Error   string `json:"error"`
	}
	var got desktopResp
	decode(t, resp, &got)
	if resp.StatusCode != http.StatusOK || !got.OK || got.Skipped || got.Target != "desktop" {
		t.Fatalf("status=%d body=%+v, want a shown desktop test", resp.StatusCode, got)
	}
	if shown.Title == "" || shown.EventType != EventNotifyTest {
		t.Fatalf("notification = %+v, want the test message", shown)
	}

	s.notifyDesktop = func(desktop.Notification) error {
		return &desktop.SkipError{Reason: "windows session 0"}
	}
	resp = do(t, s, http.MethodPost, "/v1/notify/test", "tk", map[string]any{"target": "desktop"})
	got = desktopResp{}
	decode(t, resp, &got)
	if resp.StatusCode != http.StatusOK || got.OK || !got.Skipped || !strings.Contains(got.Error, "session 0") {
		t.Fatalf("skip: status=%d body=%+v, want ok=false skipped=true", resp.StatusCode, got)
	}

	s.notifyDesktop = func(desktop.Notification) error { return errors.New("powershell exploded") }
	resp = do(t, s, http.MethodPost, "/v1/notify/test", "tk", map[string]any{"target": "desktop"})
	got = desktopResp{}
	decode(t, resp, &got)
	if got.OK || got.Skipped || !strings.Contains(got.Error, "powershell exploded") {
		t.Fatalf("failure: body=%+v, want ok=false skipped=false with the error", got)
	}
}

// The webhook list view carries the masked URL the console LISTS beside the raw one it
// writes back.
func TestConfigViewWebhookURLRedacted(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Server: config.ServerConfig{
		Token: "tk",
		Notification: &config.NotificationConfig{Webhooks: []config.WebhookConfig{{
			URL: "https://oapi.dingtalk.com/robot/send?access_token=supersecret-token",
		}}},
	}}
	s := notifyTestServer(t, cfg)
	view := getConfigView(t, s, "tk")
	if view.Server.Notification == nil || len(view.Server.Notification.Webhooks) != 1 {
		t.Fatalf("notification view = %+v, want one webhook", view.Server.Notification)
	}
	w := view.Server.Notification.Webhooks[0]
	if strings.Contains(w.URLRedacted, "supersecret-token") {
		t.Fatalf("url_redacted=%q still carries the token", w.URLRedacted)
	}
	if !slices.Contains([]string{w.URL}, "https://oapi.dingtalk.com/robot/send?access_token=supersecret-token") {
		t.Fatalf("url=%q, want the raw URL kept for a write-back", w.URL)
	}
}

// TestNotificationDesktopPatch pins the one config write the console needed and did not
// have (gofer-x9mb): server.notification.desktop is patched member-by-member, so a form
// that carries only the switch does not drop the configured events / projects / window.
func TestNotificationDesktopPatch(t *testing.T) {
	t.Parallel()
	yamlText, _, _ := configWriteFixture(t)
	const block = `  notification:
    desktop:
      events:
        - job.terminal
      merge_sec: 30
`
	yamlText = strings.Replace(yamlText, "server:\n", "server:\n"+block, 1)
	if !strings.Contains(yamlText, "merge_sec: 30") {
		t.Fatalf("fixture injection failed:\n%s", yamlText)
	}
	s, cr, _ := newConfigWriteTestServer(t, yamlText, agent.NoopDetector{})

	// Only the switch and the projects: the events and the window must survive.
	resp := do(t, s, http.MethodPut, "/v1/config/server", adminToken, map[string]any{
		"notification": map[string]any{
			"desktop": map[string]any{"enabled": false, "projects": []string{"seed"}},
		},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT desktop status=%d, want 200: %s", resp.StatusCode, bodyText(t, resp))
	}
	got := cr.Jobs.Config().Server.Notification.Desktop
	if got == nil || got.IsEnabled() {
		t.Fatalf("desktop=%+v, want the channel paused", got)
	}
	if !slices.Equal(got.Events, []string{"job.terminal"}) || got.EffectiveMergeSec() != 30 {
		t.Fatalf("desktop=%+v, want the configured events and window kept", got)
	}
	if !slices.Equal(got.Projects, []string{"seed"}) {
		t.Fatalf("projects=%v, want the edited list", got.Projects)
	}

	// An empty events array is the operator resetting to the channel defaults, and
	// merge_sec 0 is an explicit "no merging". The projects still survive.
	resp = do(t, s, http.MethodPut, "/v1/config/server", adminToken, map[string]any{
		"notification": map[string]any{
			"desktop": map[string]any{"enabled": true, "events": []string{}, "merge_sec": 0},
		},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT desktop (reset) status=%d, want 200: %s", resp.StatusCode, bodyText(t, resp))
	}
	got = cr.Jobs.Config().Server.Notification.Desktop
	if !got.IsEnabled() || len(got.Events) != 0 || got.EffectiveMergeSec() != 0 {
		t.Fatalf("desktop=%+v, want it on with the default events and no merging", got)
	}
	if !slices.Equal(got.Projects, []string{"seed"}) {
		t.Fatalf("projects=%v, want them kept by a body that mentions none", got.Projects)
	}

	// A negative window is refused by the existing validation, not silently clamped.
	resp = do(t, s, http.MethodPut, "/v1/config/server", adminToken, map[string]any{
		"notification": map[string]any{"desktop": map[string]any{"merge_sec": -5}},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("merge_sec=-5 status=%d, want 400: %s", resp.StatusCode, bodyText(t, resp))
	}
}
