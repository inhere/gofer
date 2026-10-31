package commands

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/trigger"
)

// tool_trigger_test.go drives `gofer tool trigger` against an httptest stub (G053): the
// redacted ls / show output, and invoke's signing, secret sources and no-token contract.

type triggerStubRequest struct {
	Method, Path, Query, Body string
	Header                    http.Header
}

type triggerStub struct {
	mu       sync.Mutex
	requests []triggerStubRequest
	routes   map[string]string // "METHOD path" -> body
	status   map[string]int
}

func (s *triggerStub) last(t *testing.T) triggerStubRequest {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == 0 {
		t.Fatal("no request reached the stub")
	}
	return s.requests[len(s.requests)-1]
}

func newTriggerStub(t *testing.T, routes map[string]string) *triggerStub {
	t.Helper()
	s := &triggerStub{routes: routes, status: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		key := r.Method + " " + r.URL.Path
		s.mu.Lock()
		s.requests = append(s.requests, triggerStubRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: string(data), Header: r.Header.Clone()})
		body, ok := s.routes[key]
		status := s.status[key]
		s.mu.Unlock()
		if !ok {
			http.Error(w, `{"error":"unknown route"}`, http.StatusNotFound)
			return
		}
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	jobConnOpts.server = srv.URL
	jobConnOpts.token = "tkn-operator"
	t.Cleanup(func() { jobConnOpts.server, jobConnOpts.token = "", "" })
	return s
}

func bindToolTrigger(t *testing.T, sub string) *gcli.Command {
	t.Helper()
	for _, def := range NewToolTriggerCmd().Subs {
		if def.Name == sub {
			c := gcli.NewCommand(def.Name, def.Desc, nil)
			def.Config(c)
			return c
		}
	}
	t.Fatalf("no tool trigger subcommand %q", sub)
	return nil
}

const triggerViewJSON = `{"name":"source-update","enabled":true,"owner_caller":"automation","project":"demo","template":"tpl","provider":"generic",
"auth":{"kind":"hmac-sha256","secret_env":"GOFER_TRIGGER_SRC","secret_configured":true,"timestamp_header":"X-Gofer-Trigger-Timestamp","signature_header":"X-Gofer-Trigger-Signature","replay_window_sec":300},
"dedup":{"key":"header.X-Delivery","ttl_sec":86400},"rate":{"per_minute":10,"burst":2,"max_inflight":2},"limits":{"max_body_bytes":262144,"max_var_bytes":32768},
"available":true,"last_delivery":{"event_hash":"0123456789abcdef","status":"submitted","job_id":"job-9","received_at":1700000000}}`

func TestToolTriggerLsAndShowPrintTheRedactedView(t *testing.T) {
	t.Setenv("GOFER_LANG", "en")
	stub := newTriggerStub(t, map[string]string{
		"GET /v1/triggers":                          `{"triggers":[` + triggerViewJSON + `,{"name":"idle","enabled":false,"owner_caller":"automation","project":"demo","template":"tpl","provider":"generic","auth":{},"dedup":{},"rate":{},"limits":{},"available":false}]}`,
		"GET /v1/triggers/source-update":            triggerViewJSON,
		"GET /v1/triggers/source-update/deliveries": `{"trigger":"source-update","deliveries":[{"event_hash":"0123456789abcdef","status":"submitted","job_id":"job-9","received_at":1700000000}]}`,
	})
	out := captureOutput(t, func() {
		if err := runToolTriggerLs(bindToolTrigger(t, "ls"), nil); err != nil {
			t.Fatalf("ls: %v", err)
		}
	})
	for _, want := range []string{"NAME", "source-update", "ready", "idle", "disabled", "submitted", "0123456789abcdef"} {
		if !strings.Contains(out, want) {
			t.Errorf("ls output lacks %q:\n%s", want, out)
		}
	}
	if got := stub.last(t); got.Header.Get("Authorization") != "Bearer tkn-operator" {
		t.Errorf("ls must use the operator token, got %q", got.Header.Get("Authorization"))
	}

	toolTriggerShowOpts.deliveries = 5
	out = captureOutput(t, func() {
		if err := runToolTriggerShow(bindToolTrigger(t, "show"), []string{"source-update"}); err != nil {
			t.Fatalf("show: %v", err)
		}
	})
	for _, want := range []string{"source-update", "automation", "GOFER_TRIGGER_SRC", "set", "header.X-Delivery", "10/min", "job=job-9", "0123456789abcdef"} {
		if !strings.Contains(out, want) {
			t.Errorf("show output lacks %q:\n%s", want, out)
		}
	}
	if han.MatchString(out) {
		t.Errorf("English output carries CJK:\n%s", out)
	}
}

func TestToolTriggerShowFollowsLanguage(t *testing.T) {
	t.Setenv("GOFER_LANG", "zh-CN")
	newTriggerStub(t, map[string]string{
		"GET /v1/triggers/source-update":            triggerViewJSON,
		"GET /v1/triggers/source-update/deliveries": `{"deliveries":[]}`,
	})
	toolTriggerShowOpts.deliveries = 5
	out := captureOutput(t, func() {
		if err := runToolTriggerShow(bindToolTrigger(t, "show"), []string{"source-update"}); err != nil {
			t.Fatalf("show: %v", err)
		}
	})
	if !han.MatchString(out) || !strings.Contains(out, "密钥变量") {
		t.Errorf("zh-CN output is not Chinese:\n%s", out)
	}
}

func TestToolTriggerShowRefusalIsReported(t *testing.T) {
	stub := newTriggerStub(t, map[string]string{"GET /v1/triggers/x": `{"error":"permission denied","detail":"missing trigger.read"}`})
	stub.status["GET /v1/triggers/x"] = http.StatusForbidden
	err := runToolTriggerShow(bindToolTrigger(t, "show"), []string{"x"})
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("a 403 must fail the command, got %v", err)
	}
}

func writeTriggerBody(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func resetToolTriggerInvoke(t *testing.T) {
	t.Helper()
	reset := func() {
		toolTriggerInvokeOpts.body, toolTriggerInvokeOpts.id, toolTriggerInvokeOpts.secretEnv = "", "", ""
		toolTriggerInvokeOpts.secretStdin, toolTriggerInvokeOpts.headers = false, nil
		toolTriggerInvokeOpts.timestampHeader, toolTriggerInvokeOpts.signatureHeader = "", ""
	}
	reset()
	t.Cleanup(reset)
}

func TestToolTriggerInvokeSignsAndSendsNoToken(t *testing.T) {
	t.Setenv("GOFER_LANG", "en")
	resetToolTriggerInvoke(t)
	const secret = "cli-test-secret"
	t.Setenv("GOFER_CLI_TRIGGER_SECRET", secret)
	stub := newTriggerStub(t, map[string]string{"POST /v1/triggers/source-update/invoke": `{"trigger":"source-update","request_id":"trigger-abc","job_id":"job-7","duplicate":false}`})
	body := `{"ref":"main"}`
	inv := bindToolTrigger(t, "invoke")
	toolTriggerInvokeOpts.body = writeTriggerBody(t, body)
	toolTriggerInvokeOpts.id = "evt-1"
	toolTriggerInvokeOpts.secretEnv = "GOFER_CLI_TRIGGER_SECRET"
	toolTriggerInvokeOpts.headers = gcli.Strings{"X-Delivery=d-1"}

	out := captureOutput(t, func() {
		if err := runToolTriggerInvoke(inv, []string{"source-update"}); err != nil {
			t.Fatalf("invoke: %v", err)
		}
	})
	req := stub.last(t)
	if req.Body != body {
		t.Errorf("body = %q, want it verbatim", req.Body)
	}
	if req.Header.Get("Authorization") != "" {
		t.Errorf("invoke must not send a gofer token, got %q", req.Header.Get("Authorization"))
	}
	stamp := req.Header.Get(defaultTriggerTimestampHeader)
	if stamp == "" || req.Header.Get(defaultTriggerSignatureHeader) != trigger.Sign(secret, stamp, []byte(body)) {
		t.Errorf("signature headers do not verify: ts=%q sig=%q", stamp, req.Header.Get(defaultTriggerSignatureHeader))
	}
	if req.Header.Get("Idempotency-Key") != "evt-1" || req.Header.Get("X-Delivery") != "d-1" {
		t.Errorf("id / header not sent: %v", req.Header)
	}
	if !strings.Contains(out, "job-7") || !strings.Contains(out, "trigger-abc") {
		t.Errorf("output lacks the job / request ids: %q", out)
	}
	for _, leak := range []string{secret, req.Header.Get(defaultTriggerSignatureHeader)} {
		if strings.Contains(out, leak) {
			t.Errorf("output echoes %q", leak)
		}
	}
}

func TestToolTriggerInvokeSecretFromStdinAndDuplicate(t *testing.T) {
	t.Setenv("GOFER_LANG", "en")
	resetToolTriggerInvoke(t)
	stub := newTriggerStub(t, map[string]string{"POST /v1/triggers/t/invoke": `{"trigger":"t","request_id":"r1","job_id":"job-1","duplicate":true,"status":"submitted"}`})
	stub.status["POST /v1/triggers/t/invoke"] = http.StatusOK
	prev := toolStdin
	toolStdin = strings.NewReader("stdin-secret\n")
	t.Cleanup(func() { toolStdin = prev })
	inv := bindToolTrigger(t, "invoke")
	toolTriggerInvokeOpts.body = writeTriggerBody(t, `{}`)
	toolTriggerInvokeOpts.secretStdin = true
	toolTriggerInvokeOpts.timestampHeader = "X-Ts"
	toolTriggerInvokeOpts.signatureHeader = "X-Sig"
	out := captureOutput(t, func() {
		if err := runToolTriggerInvoke(inv, []string{"t"}); err != nil {
			t.Fatalf("invoke: %v", err)
		}
	})
	req := stub.last(t)
	if req.Header.Get("X-Sig") != trigger.Sign("stdin-secret", req.Header.Get("X-Ts"), []byte(`{}`)) {
		t.Error("the stdin secret did not sign the request under the custom headers")
	}
	if !strings.Contains(out, "duplicate") || !strings.Contains(out, "job-1") {
		t.Errorf("duplicate output = %q", out)
	}
}

func TestToolTriggerInvokeRejectsBadSecretSources(t *testing.T) {
	t.Setenv("GOFER_LANG", "en")
	body := writeTriggerBody(t, `{}`)
	t.Setenv("GOFER_CLI_TRIGGER_SECRET", "s")
	t.Setenv("GOFER_CLI_TRIGGER_EMPTY", "")
	cases := []struct {
		name  string
		setup func()
		want  string
	}{
		{"no secret source", func() {}, "exactly one source"},
		{"both sources", func() {
			toolTriggerInvokeOpts.secretEnv, toolTriggerInvokeOpts.secretStdin = "GOFER_CLI_TRIGGER_SECRET", true
		}, "exactly one source"},
		{"empty variable", func() { toolTriggerInvokeOpts.secretEnv = "GOFER_CLI_TRIGGER_EMPTY" }, "secret is empty"},
		{"stdin carries both", func() { toolTriggerInvokeOpts.secretStdin, toolTriggerInvokeOpts.body = true, "-" }, "cannot carry both"},
		{"no body", func() { toolTriggerInvokeOpts.secretEnv, toolTriggerInvokeOpts.body = "GOFER_CLI_TRIGGER_SECRET", "" }, "--body is required"},
		{"bad header", func() {
			toolTriggerInvokeOpts.secretEnv, toolTriggerInvokeOpts.headers = "GOFER_CLI_TRIGGER_SECRET", gcli.Strings{"nokey"}
		}, "Name=Value"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetToolTriggerInvoke(t)
			stub := newTriggerStub(t, map[string]string{})
			inv := bindToolTrigger(t, "invoke")
			toolTriggerInvokeOpts.body = body
			tc.setup()
			err := runToolTriggerInvoke(inv, []string{"t"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
			stub.mu.Lock()
			n := len(stub.requests)
			stub.mu.Unlock()
			if n != 0 {
				t.Errorf("a refused invocation still sent %d request(s)", n)
			}
		})
	}
}

func TestToolTriggerInvokeServerRefusalFailsWithoutSecret(t *testing.T) {
	t.Setenv("GOFER_LANG", "en")
	resetToolTriggerInvoke(t)
	t.Setenv("GOFER_CLI_TRIGGER_SECRET", "refusal-secret")
	stub := newTriggerStub(t, map[string]string{"POST /v1/triggers/t/invoke": `{"error":"trigger authentication failed","detail":"bad signature","code":"trigger_unauthenticated"}`})
	stub.status["POST /v1/triggers/t/invoke"] = http.StatusUnauthorized
	inv := bindToolTrigger(t, "invoke")
	toolTriggerInvokeOpts.body = writeTriggerBody(t, `{}`)
	toolTriggerInvokeOpts.secretEnv = "GOFER_CLI_TRIGGER_SECRET"
	err := runToolTriggerInvoke(inv, []string{"t"})
	if err == nil || !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "refusal-secret") {
		t.Fatalf("err = %v", err)
	}
}
