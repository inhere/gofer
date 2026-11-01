package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/core"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/wait"
	"github.com/inhere/gofer/internal/trigger"
)

const imHTTPToken = "verify-token-demo"

func imFixture(t *testing.T, provider, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "trigger", "testdata", provider, name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(raw))
}

// newIMHTTPServer is newTriggerHTTPServer plus the IM triggers "dd" (dingtalk) and "fs"
// (feishu) whose task book runs an echoing cli-agent, so the rendered prompt can be read
// back from the job.
func newIMHTTPServer(t *testing.T) (*Server, *core.Core, *trigger.Engine) {
	t.Helper()
	t.Setenv("GOFER_TRIGGER_HTTP_TEST", triggerTestSecret)
	t.Setenv("GOFER_TRIGGER_HTTP_TOKEN", imHTTPToken)
	yamlText := triggerHTTPFixture(t)
	const im = `    - name: dd
      enabled: true
      provider: dingtalk
      owner_caller: web-op
      project: seed
      template: run-im
      auth: {secret_env: GOFER_TRIGGER_HTTP_TEST}
      im: {command_prefix: /run}
      input:
        vars: {who: im.sender_name, room: im.conversation_id, text: im.text}
    - name: fs
      enabled: true
      provider: feishu
      owner_caller: web-op
      project: seed
      template: run-im
      auth: {secret_env: GOFER_TRIGGER_HTTP_TEST, verification_token_env: GOFER_TRIGGER_HTTP_TOKEN}
      im: {command_prefix: /run}
      input:
        vars: {who: im.sender_id, room: im.conversation_id, text: im.text}
`
	yamlText = strings.Replace(yamlText, "  triggers:\n", "  triggers:\n"+im, 1)
	s, cr, _ := newConfigWriteTestServer(t, yamlText, agent.NoopDetector{})
	hostDir := ""
	if proj, ok := cr.Config().Projects["seed"]; ok {
		hostDir = cr.Config().ExecPath(proj)
	}
	tpl := "---\nagent: echoer\nrunner: local\nvars:\n  who: {desc: who}\n  room: {desc: room}\n  text: {desc: text}\n---\nim-ran {{who}} in {{room}}: {{text}}\n"
	if err := os.WriteFile(filepath.Join(hostDir, ".gofer", "templates", "run-im.md"), []byte(tpl), 0o600); err != nil {
		t.Fatal(err)
	}
	eng := trigger.NewServerEngine(cr.Config, cr.Jobs, s.CallerHasPermission)
	eng.SetClock(func() time.Time { return time.Unix(triggerHTTPNow, 0) })
	s.SetTriggerEngine(eng)
	grantUsersSubmit(s, true)
	return s, cr, eng
}

func ddHTTPHeaders(secret string, at time.Time) map[string]string {
	ts := strconv.FormatInt(at.UnixMilli(), 10)
	return map[string]string{trigger.DingTalkTimestampHeader: ts, trigger.DingTalkSignHeader: trigger.SignDingTalk(secret, ts)}
}

func fsHTTPHeaders(key, body string, at time.Time) map[string]string {
	ts := strconv.FormatInt(at.Unix(), 10)
	return map[string]string{trigger.FeishuTimestampHeader: ts, trigger.FeishuNonceHeader: "nonce-demo",
		trigger.FeishuSignatureHeader: trigger.SignFeishu(key, ts, "nonce-demo", []byte(body))}
}

func fsEncryptedBody(plain string) string {
	return `{"encrypt":"` + trigger.FeishuEncrypt(triggerTestSecret, []byte(plain), []byte("0123456789abcdef")) + `"}`
}

var imNow = time.Unix(triggerHTTPNow, 0)

// TestDingTalkTriggerStartsARealJob: a signed text message over HTTP makes a job whose prompt
// holds the whitelisted envelope fields and nothing else of the callback; the platform's
// retry is a 200 duplicate.
func TestDingTalkTriggerStartsARealJob(t *testing.T) {
	s, cr, _ := newIMHTTPServer(t)
	body := imFixture(t, "dingtalk", "text_message.json")
	resp, raw := invokeTrigger(t, s, "dd", body, ddHTTPHeaders(triggerTestSecret, imNow))
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("delivery = %d %s", resp.StatusCode, raw)
	}
	var out triggerInvokeResponse
	decodeBytes(t, raw, &out)
	rec, ok, err := cr.Jobs.Meta().GetJob(out.JobID)
	if err != nil || !ok {
		t.Fatalf("job %q: ok=%v err=%v", out.JobID, ok, err)
	}
	if rec.Agent != "echoer" || rec.OriginCaller != "web-op" || rec.Channel != "trigger" || rec.CallerID != "" {
		t.Fatalf("job agent=%q origin=%q channel=%q caller=%q", rec.Agent, rec.OriginCaller, rec.Channel, rec.CallerID)
	}
	if !strings.Contains(rec.RequestJSON, "im-ran Alice Example in cid-demo-room: build the widget") {
		t.Fatalf("prompt not rendered from the envelope: %s", rec.RequestJSON)
	}
	for _, leak := range []string{"PLACEHOLDER-SESSION-WEBHOOK", triggerTestSecret, "staff-alice"} {
		if strings.Contains(rec.RequestJSON, leak) {
			t.Fatalf("job request leaks %q", leak)
		}
	}
	if got, ok := cr.Jobs.WaitFor(out.JobID, wait.Timeout(t, 30*time.Second)); !ok || got.Status != "done" {
		t.Fatalf("job result = %+v ok=%v, want done", got, ok)
	}
	resp, raw = invokeTrigger(t, s, "dd", body, ddHTTPHeaders(triggerTestSecret, imNow))
	var dup triggerInvokeResponse
	decodeBytes(t, raw, &dup)
	if resp.StatusCode != http.StatusOK || !dup.Duplicate || dup.JobID != out.JobID {
		t.Fatalf("retry = %d %s", resp.StatusCode, raw)
	}
}

func TestDingTalkTriggerStatusTable(t *testing.T) {
	s, _, _ := newIMHTTPServer(t)
	cases := []struct {
		name    string
		body    string
		headers map[string]string
		status  int
		code    string
	}{
		{"wrong secret", imFixture(t, "dingtalk", "text_message.json"), ddHTTPHeaders("other", imNow), 401, "trigger_unauthenticated"},
		{"no headers", imFixture(t, "dingtalk", "text_message.json"), nil, 401, "trigger_unauthenticated"},
		{"stale", imFixture(t, "dingtalk", "text_message.json"), ddHTTPHeaders(triggerTestSecret, imNow.Add(-time.Hour)), 401, "trigger_replay_window"},
		{"not json", "text=hi", ddHTTPHeaders(triggerTestSecret, imNow), 400, "trigger_body_not_json"},
		{"picture", imFixture(t, "dingtalk", "picture_message.json"), ddHTTPHeaders(triggerTestSecret, imNow), 200, trigger.IgnoredNotText},
		{"bot itself", imFixture(t, "dingtalk", "bot_self_message.json"), ddHTTPHeaders(triggerTestSecret, imNow), 200, trigger.IgnoredFromBot},
		{"prefix mismatch", strings.Replace(imFixture(t, "dingtalk", "text_message.json"), "/run", "/x", 1), ddHTTPHeaders(triggerTestSecret, imNow), 200, trigger.IgnoredPrefixMismatch},
	}
	for _, tc := range cases {
		resp, raw := invokeTrigger(t, s, "dd", tc.body, tc.headers)
		if resp.StatusCode != tc.status {
			t.Errorf("%s: %d %s, want %d", tc.name, resp.StatusCode, raw, tc.status)
			continue
		}
		if tc.status == 200 {
			var out triggerInvokeResponse
			decodeBytes(t, raw, &out)
			if !out.Ignored || out.Reason != tc.code || out.JobID != "" {
				t.Errorf("%s: answer = %s", tc.name, raw)
			}
		} else if errCode(t, raw) != tc.code {
			t.Errorf("%s: code = %s, want %s", tc.name, raw, tc.code)
		}
	}
}

// TestFeishuTriggerHandshakeAndEvent: the url_verification handshake echoes the challenge,
// an encrypted signed message starts a job, and an image / bot message is ignored.
func TestFeishuTriggerHandshakeAndEvent(t *testing.T) {
	s, cr, _ := newIMHTTPServer(t)
	resp, raw := invokeTrigger(t, s, "fs", imFixture(t, "feishu", "url_verification.json"), nil)
	var hs map[string]string
	decodeBytes(t, raw, &hs)
	if resp.StatusCode != http.StatusOK || len(hs) != 1 || hs["challenge"] != "challenge-demo-123" {
		t.Fatalf("handshake = %d %q", resp.StatusCode, raw)
	}
	bad := strings.Replace(imFixture(t, "feishu", "url_verification.json"), imHTTPToken, "bad-token", 1)
	if resp, raw := invokeTrigger(t, s, "fs", bad, nil); resp.StatusCode != http.StatusUnauthorized || errCode(t, raw) != "trigger_unauthenticated" {
		t.Fatalf("handshake with a wrong token = %d %s", resp.StatusCode, raw)
	}

	body := fsEncryptedBody(imFixture(t, "feishu", "message_text.json"))
	resp, raw = invokeTrigger(t, s, "fs", body, fsHTTPHeaders(triggerTestSecret, body, imNow))
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("event = %d %s", resp.StatusCode, raw)
	}
	var out triggerInvokeResponse
	decodeBytes(t, raw, &out)
	rec, ok, err := cr.Jobs.Meta().GetJob(out.JobID)
	if err != nil || !ok || !strings.Contains(rec.RequestJSON, "im-ran ou_alice_demo in oc_demo_room: build the widget with @Bob Example") {
		t.Fatalf("job ok=%v err=%v request=%s", ok, err, rec.RequestJSON)
	}
	if got, ok := cr.Jobs.WaitFor(out.JobID, wait.Timeout(t, 30*time.Second)); !ok || got.Status != "done" {
		t.Fatalf("job result = %+v ok=%v, want done", got, ok)
	}
	for name, plain := range map[string]string{"image": imFixture(t, "feishu", "message_image.json"), "app sender": imFixture(t, "feishu", "message_from_app.json")} {
		b := fsEncryptedBody(plain)
		resp, raw := invokeTrigger(t, s, "fs", b, fsHTTPHeaders(triggerTestSecret, b, imNow))
		var ig triggerInvokeResponse
		decodeBytes(t, raw, &ig)
		if resp.StatusCode != http.StatusOK || !ig.Ignored || ig.JobID != "" {
			t.Errorf("%s: %d %s", name, resp.StatusCode, raw)
		}
	}
	if resp, raw := invokeTrigger(t, s, "fs", body, fsHTTPHeaders("other-key", body, imNow)); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong key = %d %s", resp.StatusCode, raw)
	}
}

// TestIMTriggersLeakNothing: responses, logs and audit carry neither the secrets, the
// signatures, the message ids nor any message text.
func TestIMTriggersLeakNothing(t *testing.T) {
	logs := captureSlog(t)
	s, _, eng := newIMHTTPServer(t)
	var mu sync.Mutex
	var audit []jobstore.TriggerAuditEvent
	eng.SetAudit(func(ev jobstore.TriggerAuditEvent) error {
		mu.Lock()
		audit = append(audit, ev)
		mu.Unlock()
		return nil
	})
	dd := imFixture(t, "dingtalk", "text_message.json")
	fsBody := fsEncryptedBody(imFixture(t, "feishu", "message_text.json"))
	good := ddHTTPHeaders(triggerTestSecret, imNow)
	var answers []string
	for _, c := range []struct {
		name, body string
		h          map[string]string
	}{
		{"dd", dd, good}, {"dd", dd, good}, {"dd", dd, ddHTTPHeaders("other", imNow)},
		{"fs", fsBody, fsHTTPHeaders(triggerTestSecret, fsBody, imNow)}, {"fs", fsBody, fsHTTPHeaders("other", fsBody, imNow)},
		{"fs", imFixture(t, "feishu", "url_verification.json"), nil},
	} {
		_, raw := invokeTrigger(t, s, c.name, c.body, c.h)
		answers = append(answers, raw)
	}
	view, ok := eng.Show("fs")
	if !ok || view.Provider != "feishu" || view.IM == nil || !view.Auth.VerificationTokenConfigured {
		t.Fatalf("view = %+v", view)
	}
	mu.Lock()
	auditText := ""
	for _, ev := range audit {
		auditText += ev.Kind + "|" + ev.Provider + "|" + ev.Code + "|" + ev.EventHash + "|" + ev.Remote + "\n"
	}
	mu.Unlock()
	for where, text := range map[string]string{"responses": strings.Join(answers[:5], "\n"), "logs": logs.String(), "audit": auditText} {
		for _, leak := range []string{triggerTestSecret, imHTTPToken, good[trigger.DingTalkSignHeader], "msg-dd-0001", "evt-fs-0001", "build the widget", "Alice Example", "PLACEHOLDER-SESSION-WEBHOOK", "cid-demo-room"} {
			if strings.Contains(text, leak) {
				t.Errorf("%s contain %q", where, leak)
			}
		}
	}
}
