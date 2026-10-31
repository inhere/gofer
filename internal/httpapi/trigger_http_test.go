package httpapi

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/core"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/wait"
	"github.com/inhere/gofer/internal/trigger"
)

const (
	triggerHTTPNow     = 1700000000
	triggerHTTPPayload = "payload-marker-7731"
)

// triggerHTTPFixture is the config-write fixture plus the triggers of this file: "push" (the
// default shape), "slow" (burst 1), "small" (64 byte body), "off" (disabled) and "runner"
// (an echo task book, to see a real job run).
func triggerHTTPFixture(t *testing.T) string {
	t.Helper()
	yamlText, hostDir := triggerFixtureYAML(t)
	const extra = `    - name: slow
      enabled: true
      owner_caller: web-op
      project: seed
      template: tpl
      auth: {secret_env: GOFER_TRIGGER_HTTP_TEST}
      rate: {per_minute: 1, burst: 1}
    - name: small
      enabled: true
      owner_caller: web-op
      project: seed
      template: tpl
      auth: {secret_env: GOFER_TRIGGER_HTTP_TEST}
      limits: {max_body_bytes: 64}
    - name: off
      enabled: false
      owner_caller: web-op
      project: seed
      template: tpl
      auth: {secret_env: GOFER_TRIGGER_HTTP_TEST}
    - name: runner
      enabled: true
      owner_caller: web-op
      project: seed
      template: run-exec
      auth: {secret_env: GOFER_TRIGGER_HTTP_TEST}
      input: {vars: {ref: body.ref}}
`
	yamlText = strings.Replace(yamlText, "  triggers:\n", "  triggers:\n"+extra, 1)
	yamlText = strings.Replace(yamlText, "      - claude\n      - mytool\n", "      - claude\n      - mytool\n      - echoer\n", 1)
	// An exec agent needs a cmd, which a trigger can never supply; a cli-agent that just
	// echoes its prompt gives a job that really runs.
	yamlText = strings.Replace(yamlText, "\nagents:\n", "\nagents:\n  echoer:\n    type: cli-agent\n    command: echo\n    args:\n      - \"{{prompt}}\"\n", 1)
	if !strings.Contains(yamlText, "name: runner") || !strings.Contains(yamlText, "echoer:") {
		t.Fatalf("fixture injection failed:\n%s", yamlText)
	}
	if err := os.WriteFile(filepath.Join(hostDir, ".gofer", "templates", "run-exec.md"), []byte("---\nagent: echoer\nrunner: local\nvars:\n  ref: {desc: git ref}\n---\ntrigger-ran {{ref}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return yamlText
}

func newTriggerHTTPServer(t *testing.T) (*Server, *core.Core, *trigger.Engine) {
	t.Helper()
	t.Setenv("GOFER_TRIGGER_HTTP_TEST", triggerTestSecret)
	s, cr, _ := newConfigWriteTestServer(t, triggerHTTPFixture(t), agent.NoopDetector{})
	eng := trigger.NewServerEngine(cr.Config, cr.Jobs, s.CallerHasPermission)
	eng.SetClock(func() time.Time { return time.Unix(triggerHTTPNow, 0) })
	s.SetTriggerEngine(eng)
	grantUsersSubmit(s, true)
	return s, cr, eng
}

// invokeTrigger sends one unauthenticated (no gofer bearer) invoke request.
func invokeTrigger(t *testing.T, s *Server, name, body string, headers map[string]string) (*http.Response, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/triggers/"+name+"/invoke", strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	resp := rec.Result()
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, string(raw)
}

func signedHeaders(secret string, ts int64, body string, extra map[string]string) map[string]string {
	stamp := strconv.FormatInt(ts, 10)
	h := map[string]string{"X-Gofer-Trigger-Timestamp": stamp, "X-Gofer-Trigger-Signature": trigger.Sign(secret, stamp, []byte(body))}
	for k, v := range extra {
		h[k] = v
	}
	return h
}

func errCode(t *testing.T, raw string) string {
	t.Helper()
	var b errorBody
	decodeBytes(t, raw, &b)
	return b.Code
}

func decodeBytes(t *testing.T, raw string, out any) {
	t.Helper()
	decode(t, &http.Response{Body: io.NopCloser(strings.NewReader(raw))}, out)
}

// captureSlog routes the default logger into a buffer for the test and puts it back.
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func captureSlog(t *testing.T) *lockedBuf {
	t.Helper()
	buf := &lockedBuf{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// TestTriggerInvokeStatusTable walks the status table of design 3.3: accepted 202, a
// duplicate 200, bad signature / stale stamp / missing headers 401, same id with another
// body 409, oversize and non-JSON bodies 400, rate limit 429 with Retry-After, a revoked
// owner 403, and an unknown and a disabled trigger the very same 404.
func TestTriggerInvokeStatusTable(t *testing.T) {
	s, _, _ := newTriggerHTTPServer(t)
	body := `{"ref":"main","note":"` + triggerHTTPPayload + `"}`
	delivery := map[string]string{"X-Delivery": "evt-1"}

	resp, raw := invokeTrigger(t, s, "push", body, signedHeaders(triggerTestSecret, triggerHTTPNow, body, delivery))
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("first delivery = %d %s", resp.StatusCode, raw)
	}
	var first triggerInvokeResponse
	decodeBytes(t, raw, &first)
	if first.Trigger != "push" || first.JobID == "" || first.RequestID == "" || first.Duplicate {
		t.Fatalf("first answer = %+v", first)
	}

	// Same id, same body: the known result, 200 and the original job.
	resp, raw = invokeTrigger(t, s, "push", body, signedHeaders(triggerTestSecret, triggerHTTPNow, body, delivery))
	var dup triggerInvokeResponse
	decodeBytes(t, raw, &dup)
	if resp.StatusCode != http.StatusOK || !dup.Duplicate || dup.JobID != first.JobID {
		t.Fatalf("duplicate = %d %+v, want 200 with job %s", resp.StatusCode, dup, first.JobID)
	}

	// Same id, another body: the id was reused.
	other := `{"ref":"dev"}`
	resp, raw = invokeTrigger(t, s, "push", other, signedHeaders(triggerTestSecret, triggerHTTPNow, other, delivery))
	if resp.StatusCode != http.StatusConflict || errCode(t, raw) != "trigger_event_conflict" {
		t.Fatalf("id reuse = %d %s", resp.StatusCode, raw)
	}

	cases := []struct {
		name    string
		trigger string
		body    string
		headers map[string]string
		status  int
		code    string
	}{
		{"wrong secret", "push", body, signedHeaders("another-secret", triggerHTTPNow, body, nil), 401, "trigger_unauthenticated"},
		{"tampered body", "push", body + " ", signedHeaders(triggerTestSecret, triggerHTTPNow, body, nil), 401, "trigger_unauthenticated"},
		{"no signature headers", "push", body, nil, 401, "trigger_unauthenticated"},
		{"stale timestamp", "push", body, signedHeaders(triggerTestSecret, triggerHTTPNow-3600, body, nil), 401, "trigger_replay_window"},
		{"future timestamp", "push", body, signedHeaders(triggerTestSecret, triggerHTTPNow+3600, body, nil), 401, "trigger_replay_window"},
		{"not a json object", "push", `[1,2]`, signedHeaders(triggerTestSecret, triggerHTTPNow, `[1,2]`, nil), 400, "trigger_body_not_json"},
		{"missing declared input", "push", `{"other":1}`, signedHeaders(triggerTestSecret, triggerHTTPNow, `{"other":1}`, nil), 400, "trigger_input_invalid"},
		{"body over the limit", "small", strings.Repeat("x", 200), signedHeaders(triggerTestSecret, triggerHTTPNow, strings.Repeat("x", 200), nil), 400, "trigger_body_too_large"},
		{"header over the limit", "push", body, signedHeaders(triggerTestSecret, triggerHTTPNow, body, map[string]string{"X-Delivery": strings.Repeat("d", trigger.MaxHeaderValueBytes+1)}), 400, "trigger_header_too_large"},
		{"unknown trigger", "nope", body, signedHeaders(triggerTestSecret, triggerHTTPNow, body, nil), 404, "trigger_not_found"},
		{"disabled trigger", "off", body, signedHeaders(triggerTestSecret, triggerHTTPNow, body, nil), 404, "trigger_not_found"},
	}
	var notFound []string
	for _, tc := range cases {
		resp, raw := invokeTrigger(t, s, tc.trigger, tc.body, tc.headers)
		if resp.StatusCode != tc.status || errCode(t, raw) != tc.code {
			t.Errorf("%s: %d %s, want %d %s", tc.name, resp.StatusCode, raw, tc.status, tc.code)
		}
		if tc.status == 404 {
			notFound = append(notFound, raw)
		}
	}
	if len(notFound) != 2 || notFound[0] != notFound[1] {
		t.Errorf("an unknown and a disabled trigger must answer byte-identically, got %q", notFound)
	}

	// An unsigned request for a disabled trigger is a 404, not a 401: the signature is not
	// even looked at, so the answer cannot tell whether the trigger exists.
	if resp, _ := invokeTrigger(t, s, "off", body, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unsigned request to a disabled trigger = %d, want 404", resp.StatusCode)
	}
}

func TestTriggerInvokeRateLimitSetsRetryAfter(t *testing.T) {
	s, _, _ := newTriggerHTTPServer(t)
	body := `{"n":1}`
	h := signedHeaders(triggerTestSecret, triggerHTTPNow, body, nil)
	if resp, raw := invokeTrigger(t, s, "slow", body, h); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("first = %d %s", resp.StatusCode, raw)
	}
	resp, raw := invokeTrigger(t, s, "slow", body, h)
	if resp.StatusCode != http.StatusTooManyRequests || errCode(t, raw) != "trigger_rate_limited" {
		t.Fatalf("second = %d %s", resp.StatusCode, raw)
	}
	if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err != nil || secs < 1 {
		t.Errorf("Retry-After = %q, want a positive number of seconds", resp.Header.Get("Retry-After"))
	}
}

// TestTriggerInvokeOwnerRevocation: the owner's permission is judged on every call, so a
// revoked owner gets 403 and no job.
func TestTriggerInvokeOwnerRevocation(t *testing.T) {
	s, cr, _ := newTriggerHTTPServer(t)
	body := `{"ref":"main"}`
	grantUsersSubmit(s, false)
	before := cr.Jobs.Stats().InFlight
	resp, raw := invokeTrigger(t, s, "push", body, signedHeaders(triggerTestSecret, triggerHTTPNow, body, nil))
	if resp.StatusCode != http.StatusForbidden || errCode(t, raw) != "trigger_owner_denied" {
		t.Fatalf("revoked owner = %d %s", resp.StatusCode, raw)
	}
	if cr.Jobs.Stats().InFlight > before {
		t.Fatal("a job was created for a revoked owner")
	}
}

// TestTriggerInvokeStartsARealJob is the end to end case: an enabled trigger with an exec
// task book, invoked over HTTP, produces a job that runs under the owner's ceiling with the
// trigger channel, and no gofer bearer was needed.
func TestTriggerInvokeStartsARealJob(t *testing.T) {
	s, cr, _ := newTriggerHTTPServer(t)
	body := `{"ref":"main"}`
	resp, raw := invokeTrigger(t, s, "runner", body, signedHeaders(triggerTestSecret, triggerHTTPNow, body, nil))
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("invoke = %d %s", resp.StatusCode, raw)
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
	got, ok := cr.Jobs.WaitFor(out.JobID, wait.Timeout(t, 30*time.Second))
	if !ok || got.Status != "done" {
		t.Fatalf("job result = %+v ok=%v, want done", got, ok)
	}
}

// TestTriggerResponsesAndLogsLeakNothing: across every kind of answer, neither the
// response, the log, nor the audit trail carries the secret, a payload value, the
// signature, or the rendered prompt.
func TestTriggerResponsesAndLogsLeakNothing(t *testing.T) {
	logs := captureSlog(t)
	s, _, eng := newTriggerHTTPServer(t)
	var mu sync.Mutex
	var audit []jobstore.TriggerAuditEvent
	eng.SetAudit(func(ev jobstore.TriggerAuditEvent) error {
		mu.Lock()
		audit = append(audit, ev)
		mu.Unlock()
		return nil
	})

	body := `{"ref":"` + triggerHTTPPayload + `"}`
	good := signedHeaders(triggerTestSecret, triggerHTTPNow, body, map[string]string{"X-Delivery": "evt-secret-id"})
	bad := signedHeaders("another-secret", triggerHTTPNow, body, nil)
	stale := signedHeaders(triggerTestSecret, triggerHTTPNow-3600, body, nil)
	var answers []string
	for _, h := range []map[string]string{good, good, bad, stale, nil} {
		_, raw := invokeTrigger(t, s, "push", body, h)
		answers = append(answers, raw)
	}
	_, raw := invokeTrigger(t, s, "nope", body, good)
	answers = append(answers, raw)

	mu.Lock()
	auditText := ""
	for _, ev := range audit {
		auditText += ev.Kind + "|" + ev.Code + "|" + ev.EventHash + "|" + ev.Remote + "|" + ev.RequestID + "\n"
	}
	mu.Unlock()
	if auditText == "" {
		t.Fatal("no audit events were recorded")
	}
	haystacks := map[string]string{"responses": strings.Join(answers, "\n"), "logs": logs.String(), "audit": auditText}
	for where, text := range haystacks {
		for _, secret := range []string{triggerTestSecret, triggerHTTPPayload, good["X-Gofer-Trigger-Signature"], strings.TrimPrefix(good["X-Gofer-Trigger-Signature"], "sha256="), "evt-secret-id", "rebuild "} {
			if strings.Contains(text, secret) {
				t.Errorf("%s contain %q", where, secret)
			}
		}
	}
}

// TestTriggerReadRoutes: the redacted views need trigger.read, show the env key and
// "configured" instead of the secret, list deliveries by hash only, and refuse a job
// credential.
func TestTriggerReadRoutes(t *testing.T) {
	s, cr, _ := newTriggerHTTPServer(t)
	body := `{"ref":"` + triggerHTTPPayload + `"}`
	resp, raw := invokeTrigger(t, s, "push", body, signedHeaders(triggerTestSecret, triggerHTTPNow, body, map[string]string{"X-Delivery": "evt-read"}))
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("seed delivery = %d %s", resp.StatusCode, raw)
	}
	var seeded triggerInvokeResponse
	decodeBytes(t, raw, &seeded)

	paths := []string{"/v1/triggers", "/v1/triggers/push", "/v1/triggers/push/deliveries"}
	for _, p := range paths {
		if r := do(t, s, http.MethodGet, p, "", nil); r.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s without a token = %d, want 401", p, r.StatusCode)
		}
		// opToken is a plain caller: the seeded users grant does not include trigger.read.
		if r := do(t, s, http.MethodGet, p, opToken, nil); r.StatusCode != http.StatusForbidden {
			t.Errorf("GET %s without trigger.read = %d, want 403", p, r.StatusCode)
		}
	}
	s.setDBGrants(grantsFor([]string{authz.PermTriggerRead}))
	for _, p := range paths {
		if r := do(t, s, http.MethodGet, p, opToken, nil); r.StatusCode != http.StatusOK {
			t.Errorf("GET %s with trigger.read = %d, want 200", p, r.StatusCode)
		}
	}

	var list struct {
		Triggers []trigger.View `json:"triggers"`
	}
	decode(t, do(t, s, http.MethodGet, "/v1/triggers", opToken, nil), &list)
	names := map[string]trigger.View{}
	for _, v := range list.Triggers {
		names[v.Name] = v
	}
	if len(names) != 6 || names["off"].Enabled || !names["push"].Enabled {
		t.Fatalf("list = %+v", names)
	}
	push := names["push"]
	if push.Owner != "web-op" || push.Project != "seed" || push.Template != "tpl" || push.Provider != "generic" ||
		push.Auth.SecretEnv != "GOFER_TRIGGER_HTTP_TEST" || !push.Auth.SecretConfigured || push.Dedup.Key != "header.X-Delivery" ||
		push.Last == nil || push.Last.Status != jobstore.TriggerReceiptSubmitted || push.Last.JobID != seeded.JobID {
		t.Fatalf("push view = %+v", push)
	}
	if names["slow"].Rate.PerMinute != 1 || names["small"].Limits.MaxBodyBytes != 64 {
		t.Fatalf("policies not shown: slow=%+v small=%+v", names["slow"].Rate, names["small"].Limits)
	}

	var deliveries struct {
		Trigger    string                 `json:"trigger"`
		Deliveries []trigger.DeliveryView `json:"deliveries"`
	}
	decode(t, do(t, s, http.MethodGet, "/v1/triggers/push/deliveries", opToken, nil), &deliveries)
	if len(deliveries.Deliveries) != 1 || len(deliveries.Deliveries[0].EventHash) != 16 || deliveries.Deliveries[0].JobID != seeded.JobID {
		t.Fatalf("deliveries = %+v", deliveries)
	}

	// Nothing of the secret, the payload or the raw event id is in any read answer.
	for _, p := range paths {
		text := bodyText(t, do(t, s, http.MethodGet, p, opToken, nil))
		for _, leak := range []string{triggerTestSecret, triggerHTTPPayload, "evt-read"} {
			if strings.Contains(text, leak) {
				t.Errorf("GET %s leaks %q: %s", p, leak, text)
			}
		}
	}

	if r := do(t, s, http.MethodGet, "/v1/triggers/nope", opToken, nil); r.StatusCode != http.StatusNotFound {
		t.Errorf("unknown trigger view = %d, want 404", r.StatusCode)
	}
	if r := do(t, s, http.MethodGet, "/v1/triggers/nope/deliveries", opToken, nil); r.StatusCode != http.StatusNotFound {
		t.Errorf("unknown trigger deliveries = %d, want 404", r.StatusCode)
	}
	// A disabled trigger is still described to an operator.
	if r := do(t, s, http.MethodGet, "/v1/triggers/off", opToken, nil); r.StatusCode != http.StatusOK {
		t.Errorf("disabled trigger view = %d, want 200", r.StatusCode)
	}

	// The root administrator holds every permission.
	if r := do(t, s, http.MethodGet, "/v1/triggers", adminToken, nil); r.StatusCode != http.StatusOK {
		t.Errorf("root read = %d, want 200", r.StatusCode)
	}

	// A job credential may read the whole GET surface — except this one.
	jobTok := seedJobToken(t, s, seeded.JobID, jobstore.JobCredentialMember, "")
	for _, p := range paths {
		r := do(t, s, http.MethodGet, p, jobTok, nil)
		var eb errorBody
		decode(t, r, &eb)
		if r.StatusCode != http.StatusForbidden || eb.Code != "trigger_read_job_credential" {
			t.Errorf("GET %s with a job credential = %d %+v, want 403 trigger_read_job_credential", p, r.StatusCode, eb)
		}
	}
	_ = cr
}

// TestTriggerWithoutEngineIsNotFound: a server that has no trigger engine answers invoke
// with the same 404 and lists nothing.
func TestTriggerWithoutEngineIsNotFound(t *testing.T) {
	s, _, _ := newConfigWriteTestServer(t, triggerHTTPFixture(t), agent.NoopDetector{})
	resp, raw := invokeTrigger(t, s, "push", `{}`, nil)
	if resp.StatusCode != http.StatusNotFound || errCode(t, raw) != "trigger_not_found" {
		t.Fatalf("invoke without an engine = %d %s", resp.StatusCode, raw)
	}
	var list struct {
		Triggers []trigger.View `json:"triggers"`
	}
	decode(t, do(t, s, http.MethodGet, "/v1/triggers", adminToken, nil), &list)
	if len(list.Triggers) != 0 {
		t.Fatalf("list = %+v", list)
	}
}

// TestTriggerErrorTextFollowsLanguage: the detail is the catalog text in the request
// language, and the code stays the same.
func TestTriggerErrorTextFollowsLanguage(t *testing.T) {
	s, _, _ := newTriggerHTTPServer(t)
	for lang, wantHan := range map[string]bool{"zh-CN": true, "en": false} {
		req := httptest.NewRequest(http.MethodPost, "/v1/triggers/nope/invoke", strings.NewReader("{}"))
		req.Header.Set("Accept-Language", lang)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		var eb errorBody
		decodeBytes(t, rec.Body.String(), &eb)
		if rec.Code != 404 || eb.Code != "trigger_not_found" || errKeyCJK.MatchString(eb.Detail) != wantHan || eb.Detail == "" {
			t.Errorf("%s: %d %+v", lang, rec.Code, eb)
		}
	}
}
