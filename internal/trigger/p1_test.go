package trigger

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

func TestSignMatchesTheDocumentedAlgorithm(t *testing.T) {
	body := []byte(`{"ref":"main"}`)
	want := sign(testSecret, 1000, body)["X-Gofer-Trigger-Signature"]
	if got := Sign(testSecret, "1000", body); got != want {
		t.Fatalf("Sign = %q, want %q", got, want)
	}
	if Sign("other", "1000", body) == want || Sign(testSecret, "1001", body) == want || Sign(testSecret, "1000", append(body, ' ')) == want {
		t.Fatal("the signature must depend on the secret, the timestamp and every body byte")
	}
	if !strings.HasPrefix(want, "sha256=") {
		t.Fatalf("signature %q lacks the sha256= prefix", want)
	}
}

func TestEndpointListsWhatTheEngineReads(t *testing.T) {
	cfg := testCfg(func(tc *config.TriggerConfig) {
		tc.Limits.MaxBodyBytes = 1024
		tc.Dedup.Key = "header.X-Delivery"
		tc.Input.Vars = map[string]string{"ref": "body.ref", "who": "header.X-Sender"}
		tc.Auth.SignatureHeader = "X-Custom-Sig"
	})
	cfg.Server.Triggers = append(cfg.Server.Triggers, config.TriggerConfig{Name: "off", Enabled: false, OwnerCaller: "automation", Project: "demo", Template: "t", Auth: config.TriggerAuthConfig{SecretEnv: "GOFER_TRIGGER_P0"}})
	eng := NewEngine(cfg, nil, nil)

	ep, ok := eng.Endpoint("push")
	if !ok || ep.MaxBodyBytes != 1024 {
		t.Fatalf("endpoint = %+v ok=%v", ep, ok)
	}
	for _, want := range []string{"X-Gofer-Trigger-Timestamp", "X-Custom-Sig", "Idempotency-Key", "X-Delivery", "X-Sender"} {
		found := false
		for _, h := range ep.Headers {
			found = found || strings.EqualFold(h, want)
		}
		if !found {
			t.Errorf("header %s missing from %v", want, ep.Headers)
		}
	}
	for _, name := range []string{"off", "nope", ""} {
		if _, ok := eng.Endpoint(name); ok {
			t.Errorf("Endpoint(%q) must not exist (disabled or unknown)", name)
		}
	}
	var nilEng *Engine
	if _, ok := nilEng.Endpoint("push"); ok {
		t.Error("a nil engine has no endpoints")
	}
	if ep, _ := NewEngine(testCfg(nil), nil, nil).Endpoint("push"); ep.MaxBodyBytes != config.DefaultTriggerMaxBodyBytes {
		t.Errorf("default body limit = %d", ep.MaxBodyBytes)
	}
}

func TestCollectHeadersKeepsOnlyTheEndpointsAndBoundsThem(t *testing.T) {
	ep := Endpoint{Headers: []string{"X-A", "X-B"}}
	h := http.Header{}
	h.Set("x-a", "1")
	h.Set("X-Other", "secret-cookie")
	got, err := ep.CollectHeaders(h.Get)
	if err != nil || len(got) != 1 || got["X-A"] != "1" {
		t.Fatalf("collected = %v err=%v", got, err)
	}
	h.Set("X-B", strings.Repeat("b", MaxHeaderValueBytes+1))
	if _, err := ep.CollectHeaders(h.Get); !errors.Is(err, ErrHeaderTooLarge) {
		t.Fatalf("oversize header err = %v", err)
	}
	h.Set("X-B", strings.Repeat("b", MaxHeaderValueBytes))
	if _, err := ep.CollectHeaders(h.Get); err != nil {
		t.Fatalf("header at the limit: %v", err)
	}
}

func TestInvokeErrorsAreClassifiable(t *testing.T) {
	f := newFixture(t, testCfg(nil))
	for _, tc := range []struct {
		body string
		want error
	}{
		{`[1]`, ErrBodyNotJSON},
		{`not json`, ErrBodyNotJSON},
		{`{"other":1}`, ErrInvalidInput},
	} {
		if _, err := f.eng.Invoke(context.Background(), f.req(tc.body, nil)); !errors.Is(err, tc.want) {
			t.Errorf("body %q: err = %v, want %v", tc.body, err, tc.want)
		}
	}
	if f.subs.Load() != 0 {
		t.Fatal("a refused request submitted a job")
	}
}

func TestRequestIDsAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := newRequestID()
		if seen[id] || !strings.HasPrefix(id, "trigger-") {
			t.Fatalf("request id %q repeated or malformed", id)
		}
		seen[id] = true
	}
}

// TestReadViewsAreRedacted: the views name the secret variable and whether it is set, show
// the newest deliveries by truncated hash, and carry neither the secret nor any payload.
func TestReadViewsAreRedacted(t *testing.T) {
	cfg := testCfg(func(tc *config.TriggerConfig) { tc.Dedup.Key = "header.X-Delivery" })
	f := newFixture(t, cfg)
	const marker = "payload-marker-4412"
	body := `{"ref":"` + marker + `"}`
	if _, err := f.eng.Invoke(context.Background(), f.req(body, map[string]string{"X-Delivery": "evt-view-1"})); err != nil {
		t.Fatal(err)
	}
	f.advance(1)
	if _, err := f.eng.Invoke(context.Background(), f.req(`{"ref":"b"}`, map[string]string{"X-Delivery": "evt-view-2"})); err != nil {
		t.Fatal(err)
	}

	views := f.eng.List()
	if len(views) != 1 {
		t.Fatalf("views = %+v", views)
	}
	v := views[0]
	if v.Name != "push" || !v.Enabled || v.Owner != "automation" || v.Project != "demo" || v.Template != "source-update" || v.Provider != ProviderGeneric {
		t.Fatalf("view = %+v", v)
	}
	if v.Auth.SecretEnv != "GOFER_TRIGGER_P0" || !v.Auth.SecretConfigured || v.Auth.ReplayWindowSec != config.DefaultTriggerReplayWindowSec ||
		v.Dedup.TTLSec != config.DefaultTriggerDedupTTLSec || v.Limits.MaxBodyBytes != config.DefaultTriggerMaxBodyBytes {
		t.Fatalf("policies = %+v %+v %+v", v.Auth, v.Dedup, v.Limits)
	}
	// The fixture has no task-book file: the only reason it is unavailable is the template.
	if v.Available || len(v.Issues) != 1 || !hasIssue(v, config.TriggerIssueTemplateNotFound) {
		t.Fatalf("availability = %v issues=%v", v.Available, v.Issues)
	}
	if v.Last == nil || v.Last.Status != jobstore.TriggerReceiptSubmitted || v.Last.JobID != "job-2" {
		t.Fatalf("last delivery = %+v", v.Last)
	}

	rows, known, err := f.eng.Deliveries("push", 0)
	if err != nil || !known || len(rows) != 2 || rows[0].JobID != "job-2" || rows[1].JobID != "job-1" {
		t.Fatalf("deliveries = %+v known=%v err=%v", rows, known, err)
	}
	for _, r := range rows {
		if len(r.EventHash) != 16 {
			t.Errorf("event hash %q is not truncated to 16", r.EventHash)
		}
	}
	if rows, _, _ := f.eng.Deliveries("push", 1); len(rows) != 1 {
		t.Errorf("limit 1 returned %d rows", len(rows))
	}
	if _, known, _ := f.eng.Deliveries("nope", 0); known {
		t.Error("an undeclared trigger must be unknown")
	}

	all := ""
	for _, view := range f.eng.List() {
		all += jsonText(t, view)
	}
	all += jsonText(t, rows)
	for _, leak := range []string{testSecret, marker, "evt-view-1", "evt-view-2"} {
		if strings.Contains(all, leak) {
			t.Errorf("a read view leaks %q: %s", leak, all)
		}
	}

	t.Setenv("GOFER_TRIGGER_P0", "")
	if v, _ := f.eng.Show("push"); v.Auth.SecretConfigured || v.Available || !hasIssue(v, config.TriggerIssueSecretEnvUnset) {
		t.Errorf("a trigger without its secret variable: %+v", v)
	}
}

func hasIssue(v View, code string) bool {
	for _, is := range v.Issues {
		if is.Code == code {
			return true
		}
	}
	return false
}

func jsonText(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
