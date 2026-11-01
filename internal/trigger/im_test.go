package trigger

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

const (
	imSecret = "im-test-secret"
	imToken  = "verify-token-demo"
)

var imNow = time.Unix(1700000000, 0)

func imFixtureBody(t *testing.T, provider, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", provider, name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(raw))
}

func imCfg(provider string, mut func(*config.TriggerConfig)) *config.Config {
	tc := config.TriggerConfig{Name: "im", Enabled: true, Provider: provider, OwnerCaller: "automation", Project: "demo", Template: "chat",
		Auth: config.TriggerAuthConfig{SecretEnv: "GOFER_TRIGGER_IM"},
		Input: config.TriggerInputConfig{Vars: map[string]string{
			"who": "im.sender_name", "uid": "im.sender_id", "room": "im.conversation_id", "text": "im.text", "p": "im.provider", "at": "im.timestamp",
		}},
		IM: config.TriggerIMConfig{CommandPrefix: "/run"}}
	if provider == "feishu" {
		tc.Auth.VerificationTokenEnv = "GOFER_TRIGGER_IM_TOKEN"
	}
	if mut != nil {
		mut(&tc)
	}
	return &config.Config{Server: config.ServerConfig{Callers: []config.CallerConfig{{ID: "automation", Token: "x"}}, Triggers: []config.TriggerConfig{tc}}}
}

type imFix struct {
	eng    *Engine
	mu     sync.Mutex
	reqs   []job.JobRequest
	audits []jobstore.TriggerAuditEvent
	now    time.Time
}

func newIMFix(t *testing.T, provider string, mut func(*config.TriggerConfig)) *imFix {
	t.Helper()
	t.Setenv("GOFER_TRIGGER_IM", imSecret)
	t.Setenv("GOFER_TRIGGER_IM_TOKEN", imToken)
	f := &imFix{now: imNow}
	f.eng = NewEngine(imCfg(provider, mut), openStore(t), func(_ context.Context, r job.JobRequest) (string, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.reqs = append(f.reqs, r)
		return "job-" + strconv.Itoa(len(f.reqs)), nil
	})
	f.eng.SetClock(func() time.Time { return f.now })
	f.eng.SetAudit(func(ev jobstore.TriggerAuditEvent) error {
		f.mu.Lock()
		f.audits = append(f.audits, ev)
		f.mu.Unlock()
		return nil
	})
	return f
}

func (f *imFix) submitted() []job.JobRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]job.JobRequest(nil), f.reqs...)
}

func ddReqAt(body, secret string, at time.Time) TriggerRequest {
	ts := strconv.FormatInt(at.UnixMilli(), 10)
	return TriggerRequest{TriggerName: "im", RawBody: []byte(body), Headers: map[string]string{DingTalkTimestampHeader: ts, DingTalkSignHeader: SignDingTalk(secret, ts)}}
}

func ddReq(body string) TriggerRequest { return ddReqAt(body, imSecret, imNow) }

func fsReqAt(body, key string, at time.Time) TriggerRequest {
	ts, nonce := strconv.FormatInt(at.Unix(), 10), "nonce-demo"
	return TriggerRequest{TriggerName: "im", RawBody: []byte(body), Headers: map[string]string{
		FeishuTimestampHeader: ts, FeishuNonceHeader: nonce, FeishuSignatureHeader: SignFeishu(key, ts, nonce, []byte(body))}}
}

func fsReq(body string) TriggerRequest { return fsReqAt(body, imSecret, imNow) }

// fsEncrypted wraps a plain event the way the platform does when an Encrypt Key is set.
func fsEncrypted(plain string) string {
	iv := []byte("0123456789abcdef")
	return `{"encrypt":"` + FeishuEncrypt(imSecret, []byte(plain), iv) + `"}`
}

func TestDingTalkSignature(t *testing.T) {
	f := newIMFix(t, "dingtalk", nil)
	body := imFixtureBody(t, "dingtalk", "text_message.json")
	ctx := context.Background()
	if res, err := f.eng.Invoke(ctx, ddReq(body)); err != nil || res.JobID == "" {
		t.Fatalf("valid signature: res=%+v err=%v", res, err)
	}
	cases := map[string]TriggerRequest{
		"wrong secret": ddReqAt(strings.Replace(body, "msg-dd-0001", "m-2", 1), "other-secret", imNow),
		"missing sign": func() TriggerRequest {
			r := ddReq(strings.Replace(body, "msg-dd-0001", "m-3", 1))
			delete(r.Headers, DingTalkSignHeader)
			return r
		}(),
		"missing timestamp": func() TriggerRequest {
			r := ddReq(strings.Replace(body, "msg-dd-0001", "m-4", 1))
			delete(r.Headers, DingTalkTimestampHeader)
			return r
		}(),
		"sign of another timestamp": func() TriggerRequest {
			r := ddReq(strings.Replace(body, "msg-dd-0001", "m-5", 1))
			r.Headers[DingTalkTimestampHeader] = strconv.FormatInt(imNow.UnixMilli()+1, 10)
			return r
		}(),
		"not base64": func() TriggerRequest {
			r := ddReq(strings.Replace(body, "msg-dd-0001", "m-6", 1))
			r.Headers[DingTalkSignHeader] = "%%%"
			return r
		}(),
	}
	for name, r := range cases {
		if _, err := f.eng.Invoke(ctx, r); !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("%s: err = %v, want ErrUnauthenticated", name, err)
		}
	}
	if n := len(f.submitted()); n != 1 {
		t.Fatalf("jobs = %d, a failed signature must not dispatch", n)
	}
}

func TestDingTalkTimeWindow(t *testing.T) {
	f := newIMFix(t, "dingtalk", nil)
	body := imFixtureBody(t, "dingtalk", "text_message.json")
	ctx := context.Background()
	// the default window is 300s, both directions
	for _, tc := range []struct {
		name   string
		at     time.Time
		replay bool
	}{
		{"edge past", imNow.Add(-300 * time.Second), false},
		{"edge future", imNow.Add(300 * time.Second), false},
		{"past", imNow.Add(-301 * time.Second), true},
		{"future", imNow.Add(301 * time.Second), true},
	} {
		b := strings.Replace(body, "msg-dd-0001", "m-"+tc.name, 1)
		_, err := f.eng.Invoke(ctx, ddReqAt(b, imSecret, tc.at))
		if tc.replay != errors.Is(err, ErrReplay) || (!tc.replay && err != nil) {
			t.Errorf("%s: err = %v, replay=%v", tc.name, err, tc.replay)
		}
	}
	f.eng2window(t, body)
}

// eng2window: a narrower replay_window_sec applies.
func (f *imFix) eng2window(t *testing.T, body string) {
	t.Helper()
	g := newIMFix(t, "dingtalk", func(tc *config.TriggerConfig) { tc.Auth.ReplayWindowSec = 10 })
	if _, err := g.eng.Invoke(context.Background(), ddReqAt(body, imSecret, imNow.Add(-11*time.Second))); !errors.Is(err, ErrReplay) {
		t.Fatalf("11s with a 10s window: %v", err)
	}
}

func TestDingTalkEnvelopeAndWhitelistedVars(t *testing.T) {
	f := newIMFix(t, "dingtalk", nil)
	body := imFixtureBody(t, "dingtalk", "text_message.json")
	res, err := f.eng.Invoke(context.Background(), ddReq(body))
	if err != nil || res.JobID == "" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	reqs := f.submitted()
	want := map[string]string{"who": "Alice Example", "uid": "staff-alice", "room": "cid-demo-room", "text": "build the widget", "p": "dingtalk", "at": "2023-11-14T22:13:20Z"}
	for k, v := range want {
		if reqs[0].TemplateVars[k] != v {
			t.Errorf("var %s = %q, want %q", k, reqs[0].TemplateVars[k], v)
		}
	}
	if len(reqs[0].TemplateVars) != len(want) {
		t.Errorf("vars = %v", reqs[0].TemplateVars)
	}
	for _, v := range reqs[0].TemplateVars {
		if strings.Contains(v, "PLACEHOLDER-SESSION-WEBHOOK") {
			t.Fatalf("a non-whitelisted field reached the variables: %q", v)
		}
	}
	// the envelope itself
	var tc config.TriggerConfig
	tc = f.eng.config().Server.Triggers[0]
	req := ddReq(body)
	req.Payload = mustPayload(t, body)
	env, ignore, err := imEnvelope(dingtalkNormalize, tc, req)
	if err != nil || ignore != "" || env.EventID != "msg-dd-0001" || env.BodyHash != bodyHash([]byte(body)) || env.Provider != "dingtalk" {
		t.Fatalf("envelope = %+v ignore=%q err=%v", env, ignore, err)
	}
}

func TestDingTalkIgnoredDeliveries(t *testing.T) {
	text := imFixtureBody(t, "dingtalk", "text_message.json")
	cases := []struct {
		name   string
		body   string
		mut    func(*config.TriggerConfig)
		reason string
	}{
		{"picture", imFixtureBody(t, "dingtalk", "picture_message.json"), nil, IgnoredNotText},
		{"bot itself", imFixtureBody(t, "dingtalk", "bot_self_message.json"), nil, IgnoredFromBot},
		{"prefix mismatch", strings.Replace(text, "/run", "/other", 1), nil, IgnoredPrefixMismatch},
		{"empty after prefix", strings.Replace(text, " /run build the widget", "/run", 1), nil, IgnoredEmptyText},
		{"conversation not allowed", text, func(tc *config.TriggerConfig) { tc.IM.AllowConversations = []string{"cid-other"} }, IgnoredNotAllowed},
		{"sender not allowed", text, func(tc *config.TriggerConfig) { tc.IM.AllowSenders = []string{"someone-else"} }, IgnoredNotAllowed},
	}
	for _, c := range cases {
		f := newIMFix(t, "dingtalk", c.mut)
		res, err := f.eng.Invoke(context.Background(), ddReq(c.body))
		if err != nil || !res.Ignored || res.Reason != c.reason || res.JobID != "" {
			t.Errorf("%s: res=%+v err=%v, want ignored %q", c.name, res, err, c.reason)
		}
		if n := len(f.submitted()); n != 0 {
			t.Errorf("%s: %d jobs", c.name, n)
		}
	}
	// allowlists that match dispatch
	f := newIMFix(t, "dingtalk", func(tc *config.TriggerConfig) {
		tc.IM.AllowConversations = []string{"cid-demo-room"}
		tc.IM.AllowSenders = []string{"staff-alice"}
	})
	if res, err := f.eng.Invoke(context.Background(), ddReq(text)); err != nil || res.JobID == "" {
		t.Fatalf("allowed: res=%+v err=%v", res, err)
	}
}

func TestDingTalkDedupAndVarLimit(t *testing.T) {
	f := newIMFix(t, "dingtalk", nil)
	body := imFixtureBody(t, "dingtalk", "text_message.json")
	ctx := context.Background()
	first, err := f.eng.Invoke(ctx, ddReq(body))
	if err != nil || first.JobID == "" {
		t.Fatal(first, err)
	}
	dup, err := f.eng.Invoke(ctx, ddReq(body))
	if err != nil || !dup.Duplicate || dup.JobID != first.JobID {
		t.Fatalf("redelivery = %+v err=%v", dup, err)
	}
	if n := len(f.submitted()); n != 1 {
		t.Fatalf("jobs = %d", n)
	}
	// same msgId, another body: conflict
	if _, err := f.eng.Invoke(ctx, ddReq(strings.Replace(body, "build", "BUILD", 1))); !errors.Is(err, jobstore.ErrTriggerReceiptConflict) {
		t.Fatalf("same id other body: %v", err)
	}
	g := newIMFix(t, "dingtalk", func(tc *config.TriggerConfig) { tc.Limits.MaxVarBytes = 8 })
	if _, err := g.eng.Invoke(ctx, ddReq(body)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("oversize text: %v", err)
	}
}

func TestFeishuSignatureAndEncryption(t *testing.T) {
	f := newIMFix(t, "feishu", nil)
	ctx := context.Background()
	enc := fsEncrypted(imFixtureBody(t, "feishu", "message_text.json"))
	res, err := f.eng.Invoke(ctx, fsReq(enc))
	if err != nil || res.JobID == "" {
		t.Fatalf("valid encrypted event: res=%+v err=%v", res, err)
	}
	reqs := f.submitted()
	want := map[string]string{"who": "", "uid": "ou_alice_demo", "room": "oc_demo_room", "text": "build the widget with @Bob Example", "p": "feishu", "at": "2023-11-14T22:13:20Z"}
	for k, v := range want {
		if reqs[0].TemplateVars[k] != v {
			t.Errorf("var %s = %q, want %q", k, reqs[0].TemplateVars[k], v)
		}
	}
	other := func(plain, id string) string { return fsEncrypted(strings.Replace(plain, "evt-fs-0001", id, 1)) }
	plain := imFixtureBody(t, "feishu", "message_text.json")
	cases := map[string]TriggerRequest{
		"wrong key": fsReqAt(other(plain, "e-2"), "other-key", imNow),
		"tampered body": func() TriggerRequest {
			r := fsReq(other(plain, "e-3"))
			r.RawBody = append(r.RawBody, ' ')
			return r
		}(),
		"missing signature": func() TriggerRequest {
			r := fsReq(other(plain, "e-4"))
			delete(r.Headers, FeishuSignatureHeader)
			return r
		}(),
		"missing nonce": func() TriggerRequest {
			r := fsReq(other(plain, "e-5"))
			delete(r.Headers, FeishuNonceHeader)
			return r
		}(),
		"wrong verification token": fsReq(fsEncrypted(strings.Replace(strings.Replace(plain, "evt-fs-0001", "e-6", 1), imToken, "bad-token", 1))),
	}
	for name, r := range cases {
		if _, err := f.eng.Invoke(ctx, r); !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("%s: err = %v, want ErrUnauthenticated", name, err)
		}
	}
	if _, err := f.eng.Invoke(ctx, fsReqAt(other(plain, "e-7"), imSecret, imNow.Add(-301*time.Second))); !errors.Is(err, ErrReplay) {
		t.Errorf("stale: %v", err)
	}
	if _, err := f.eng.Invoke(ctx, fsReqAt(other(plain, "e-8"), imSecret, imNow.Add(300*time.Second))); err != nil {
		t.Errorf("edge of the window: %v", err)
	}
	// a garbage ciphertext behind a valid signature is a bad body, not a crash
	if _, err := f.eng.Invoke(ctx, fsReq(`{"encrypt":"AAAA"}`)); !errors.Is(err, ErrBodyNotJSON) {
		t.Errorf("garbage ciphertext: %v", err)
	}
	if n := len(f.submitted()); n != 2 {
		t.Fatalf("jobs = %d, want the valid event and the window edge only", n)
	}
}

func TestFeishuPlainEventNeedsStillSignatureAndToken(t *testing.T) {
	f := newIMFix(t, "feishu", nil)
	plain := imFixtureBody(t, "feishu", "message_text.json")
	if res, err := f.eng.Invoke(context.Background(), fsReq(plain)); err != nil || res.JobID == "" {
		t.Fatalf("plain event: res=%+v err=%v", res, err)
	}
	if _, err := f.eng.Invoke(context.Background(), TriggerRequest{TriggerName: "im", RawBody: []byte(plain), Headers: map[string]string{}}); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("unsigned plain event: %v", err)
	}
}

func TestFeishuURLVerificationHandshake(t *testing.T) {
	f := newIMFix(t, "feishu", nil)
	ctx := context.Background()
	body := imFixtureBody(t, "feishu", "url_verification.json")
	for name, raw := range map[string]string{"plain": body, "encrypted": fsEncrypted(body)} {
		res, err := f.eng.Invoke(ctx, TriggerRequest{TriggerName: "im", RawBody: []byte(raw)})
		if err != nil || res.Challenge != "challenge-demo-123" || !res.Ignored || res.Reason != IgnoredHandshake || res.JobID != "" {
			t.Errorf("%s: res=%+v err=%v", name, res, err)
		}
	}
	bad := strings.Replace(body, imToken, "bad-token", 1)
	for name, raw := range map[string]string{"plain": bad, "encrypted": fsEncrypted(bad)} {
		res, err := f.eng.Invoke(ctx, TriggerRequest{TriggerName: "im", RawBody: []byte(raw)})
		// plain: refused by the handshake; encrypted: falls to the signed path, which has no signature
		if !errors.Is(err, ErrUnauthenticated) || res.Challenge != "" {
			t.Errorf("%s bad token: res=%+v err=%v", name, res, err)
		}
	}
	long := strings.Replace(body, "challenge-demo-123", strings.Repeat("c", maxFeishuChallengeBytes+1), 1)
	if res, err := f.eng.Invoke(ctx, TriggerRequest{TriggerName: "im", RawBody: []byte(long)}); !errors.Is(err, ErrInvalidInput) || res.Challenge != "" {
		t.Errorf("long challenge: res=%+v err=%v", res, err)
	}
	if n := len(f.submitted()); n != 0 {
		t.Fatalf("jobs = %d", n)
	}
}

func TestFeishuIgnoredDeliveries(t *testing.T) {
	text := imFixtureBody(t, "feishu", "message_text.json")
	cases := []struct {
		name, body, reason string
		mut                func(*config.TriggerConfig)
	}{
		{"image", imFixtureBody(t, "feishu", "message_image.json"), IgnoredNotText, nil},
		{"app sender", imFixtureBody(t, "feishu", "message_from_app.json"), IgnoredFromBot, nil},
		{"other event type", imFixtureBody(t, "feishu", "other_event.json"), IgnoredUnsupported, nil},
		{"prefix mismatch", strings.Replace(text, "/run", "/other", 1), IgnoredPrefixMismatch, nil},
		{"conversation not allowed", text, IgnoredNotAllowed, func(tc *config.TriggerConfig) { tc.IM.AllowConversations = []string{"oc_other"} }},
		{"sender not allowed", text, IgnoredNotAllowed, func(tc *config.TriggerConfig) { tc.IM.AllowSenders = []string{"ou_other"} }},
	}
	for _, c := range cases {
		f := newIMFix(t, "feishu", c.mut)
		res, err := f.eng.Invoke(context.Background(), fsReq(fsEncrypted(c.body)))
		if err != nil || !res.Ignored || res.Reason != c.reason || res.JobID != "" || len(f.submitted()) != 0 {
			t.Errorf("%s: res=%+v err=%v", c.name, res, err)
		}
	}
}

func TestFeishuDedupByEventID(t *testing.T) {
	f := newIMFix(t, "feishu", nil)
	enc := fsEncrypted(imFixtureBody(t, "feishu", "message_text.json"))
	first, err := f.eng.Invoke(context.Background(), fsReq(enc))
	if err != nil || first.JobID == "" {
		t.Fatal(first, err)
	}
	// the platform retries the same event with a fresh signature timestamp
	f.now = imNow.Add(5 * time.Second)
	dup, err := f.eng.Invoke(context.Background(), fsReqAt(enc, imSecret, f.now))
	if err != nil || !dup.Duplicate || dup.JobID != first.JobID || len(f.submitted()) != 1 {
		t.Fatalf("retry = %+v err=%v jobs=%d", dup, err, len(f.submitted()))
	}
}

func TestIMAuditCarriesNoSecretOrText(t *testing.T) {
	f := newIMFix(t, "dingtalk", nil)
	body := imFixtureBody(t, "dingtalk", "text_message.json")
	_, _ = f.eng.Invoke(context.Background(), ddReq(body))
	_, _ = f.eng.Invoke(context.Background(), ddReqAt(strings.Replace(body, "msg-dd-0001", "m-x", 1), "other", imNow))
	_, _ = f.eng.Invoke(context.Background(), ddReq(imFixtureBody(t, "dingtalk", "bot_self_message.json")))
	f.mu.Lock()
	defer f.mu.Unlock()
	var all strings.Builder
	for _, a := range f.audits {
		all.WriteString(a.Kind + "|" + a.Provider + "|" + a.Code + "|" + a.EventHash + "|" + a.Trigger + "\n")
		if a.Provider != "dingtalk" {
			t.Errorf("audit provider = %q", a.Provider)
		}
	}
	for _, leak := range []string{imSecret, "build the widget", "Alice Example", "msg-dd-0001", "cid-demo-room"} {
		if strings.Contains(all.String(), leak) {
			t.Errorf("audit leaks %q:\n%s", leak, all.String())
		}
	}
}

func TestIMEndpointHeaders(t *testing.T) {
	t.Setenv("GOFER_TRIGGER_IM", imSecret)
	for provider, want := range map[string][]string{"dingtalk": {"timestamp", "sign"}, "feishu": {FeishuTimestampHeader, FeishuNonceHeader, FeishuSignatureHeader}} {
		ep, ok := NewEngine(imCfg(provider, nil), nil, nil).Endpoint("im")
		if !ok || len(ep.Headers) != len(want) {
			t.Fatalf("%s endpoint = %+v ok=%v", provider, ep, ok)
		}
	}
}

func TestIMViewShowsProviderNotSecrets(t *testing.T) {
	t.Setenv("GOFER_TRIGGER_IM", imSecret)
	t.Setenv("GOFER_TRIGGER_IM_TOKEN", imToken)
	eng := NewEngine(imCfg("feishu", func(tc *config.TriggerConfig) { tc.IM.AllowSenders = []string{"ou_a", "ou_b"} }), nil, nil)
	v, ok := eng.Show("im")
	if !ok || v.Provider != "feishu" || v.IM == nil || v.IM.AllowedSenders != 2 || v.IM.CommandPrefix != "/run" || !v.Auth.VerificationTokenConfigured || v.Auth.SignatureHeader != FeishuSignatureHeader {
		t.Fatalf("view = %+v", v)
	}
}

func mustPayload(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatal(err)
	}
	return m
}
