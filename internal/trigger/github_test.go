package trigger

import (
	"context"
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

const ghSecret = "gh-test-secret"

var ghNow = time.Date(2026, 10, 30, 14, 0, 0, 0, time.UTC)

func ghFixtureBody(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "github", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func ghCfg(mut func(*config.TriggerConfig)) *config.Config {
	tc := config.TriggerConfig{Name: "gh", Enabled: true, Provider: "github", OwnerCaller: "automation", Project: "demo", Template: "review",
		Auth: config.TriggerAuthConfig{SecretEnv: "GOFER_TRIGGER_GH"},
		Input: config.TriggerInputConfig{Vars: map[string]string{
			"repo": "github.repository", "number": "github.number", "title": "github.title", "url": "github.url",
			"who": "github.sender", "branch": "github.branch", "event": "github.event", "action": "github.action",
		}},
		GitHub: config.TriggerGitHubConfig{
			Events:     []string{"pull_request.opened", "issue_comment.created", "check_run.completed"},
			Conditions: []config.TriggerGitHubCondition{{Event: "check_run.completed", Field: "check_run.conclusion", Equals: []string{"failure"}}},
		}}
	if mut != nil {
		mut(&tc)
	}
	return &config.Config{Server: config.ServerConfig{Callers: []config.CallerConfig{{ID: "automation", Token: "x"}}, Triggers: []config.TriggerConfig{tc}}}
}

type ghFix struct {
	eng    *Engine
	mu     sync.Mutex
	reqs   []job.JobRequest
	audits []jobstore.TriggerAuditEvent
	now    time.Time
}

func newGHFix(t *testing.T, mut func(*config.TriggerConfig)) *ghFix {
	t.Helper()
	t.Setenv("GOFER_TRIGGER_GH", ghSecret)
	f := &ghFix{now: ghNow}
	f.eng = NewEngine(ghCfg(mut), openStore(t), func(_ context.Context, r job.JobRequest) (string, error) {
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

func ghReq(event, delivery, body string) TriggerRequest {
	return ghReqSigned(event, delivery, body, ghSecret)
}

func ghReqSigned(event, delivery, body, secret string) TriggerRequest {
	h := map[string]string{GitHubSignatureHeader: SignGitHub(secret, []byte(body)), GitHubEventHeader: event}
	if delivery != "" {
		h[GitHubDeliveryHeader] = delivery
	}
	return TriggerRequest{TriggerName: "gh", RawBody: []byte(body), Headers: h}
}

func (f *ghFix) submitted() []job.JobRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]job.JobRequest(nil), f.reqs...)
}

func (f *ghFix) kinds() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, a := range f.audits {
		out = append(out, a.Kind+"/"+a.Provider)
	}
	return out
}

func TestGitHubSignature(t *testing.T) {
	f := newGHFix(t, nil)
	body := ghFixtureBody(t, "pull_request_opened.json")
	ctx := context.Background()

	if res, err := f.eng.Invoke(ctx, ghReq("pull_request", "d-1", body)); err != nil || res.JobID == "" {
		t.Fatalf("valid signature: res=%+v err=%v", res, err)
	}
	bad := map[string]TriggerRequest{
		"wrong secret": ghReqSigned("pull_request", "d-2", body, "other-secret"),
		"tampered body": func() TriggerRequest {
			r := ghReq("pull_request", "d-3", body)
			r.RawBody = append(r.RawBody, ' ')
			return r
		}(),
		"missing header": func() TriggerRequest {
			r := ghReq("pull_request", "d-4", body)
			delete(r.Headers, GitHubSignatureHeader)
			return r
		}(),
		"no sha256= prefix": func() TriggerRequest {
			r := ghReq("pull_request", "d-5", body)
			r.Headers[GitHubSignatureHeader] = strings.TrimPrefix(r.Headers[GitHubSignatureHeader], "sha256=")
			return r
		}(),
		"sha1 scheme": func() TriggerRequest {
			r := ghReq("pull_request", "d-6", body)
			r.Headers[GitHubSignatureHeader] = "sha1=" + strings.TrimPrefix(r.Headers[GitHubSignatureHeader], "sha256=")
			return r
		}(),
		"not hex": func() TriggerRequest {
			r := ghReq("pull_request", "d-7", body)
			r.Headers[GitHubSignatureHeader] = "sha256=zz"
			return r
		}(),
		// the generic scheme's headers do not authenticate a github trigger
		"generic headers": func() TriggerRequest {
			r := ghReq("pull_request", "d-8", body)
			delete(r.Headers, GitHubSignatureHeader)
			for k, v := range sign(ghSecret, ghNow.Unix(), []byte(body)) {
				r.Headers[k] = v
			}
			return r
		}(),
	}
	for name, r := range bad {
		if _, err := f.eng.Invoke(ctx, r); !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("%s: err = %v, want ErrUnauthenticated", name, err)
		}
	}
	if n := len(f.submitted()); n != 1 {
		t.Fatalf("submissions = %d, want 1", n)
	}
	t.Setenv("GOFER_TRIGGER_GH", "")
	if _, err := f.eng.Invoke(ctx, ghReq("pull_request", "d-9", body)); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("unset secret err = %v", err)
	}
	for _, k := range f.kinds() {
		if !strings.HasSuffix(k, "/github") {
			t.Errorf("audit %q must carry provider github", k)
		}
	}
}

func TestGitHubDeliveryDedup(t *testing.T) {
	f := newGHFix(t, nil)
	ctx := context.Background()
	body := ghFixtureBody(t, "pull_request_opened.json")

	first, err := f.eng.Invoke(ctx, ghReq("pull_request", "dlv-1", body))
	if err != nil || first.JobID == "" || first.Duplicate {
		t.Fatalf("first = %+v err=%v", first, err)
	}
	dup, err := f.eng.Invoke(ctx, ghReq("pull_request", "dlv-1", body))
	if err != nil || !dup.Duplicate || dup.JobID != first.JobID {
		t.Fatalf("redelivery = %+v err=%v, want duplicate of %s", dup, err, first.JobID)
	}
	other := strings.Replace(body, "Add widget cache", "Another title", 1)
	if _, err := f.eng.Invoke(ctx, ghReq("pull_request", "dlv-1", other)); !errors.Is(err, jobstore.ErrTriggerReceiptConflict) {
		t.Fatalf("same delivery, other body err = %v, want conflict", err)
	}
	if _, err := f.eng.Invoke(ctx, ghReq("pull_request", "dlv-2", body)); err != nil {
		t.Fatalf("new delivery id: %v", err)
	}
	if n := len(f.submitted()); n != 2 {
		t.Fatalf("submissions = %d, want 2", n)
	}
	// No delivery id: refused (a captured signed request could be replayed forever).
	if _, err := f.eng.Invoke(ctx, ghReq("pull_request", "", body)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("missing delivery err = %v", err)
	}
	if _, err := f.eng.Invoke(ctx, ghReq("pull_request", strings.Repeat("d", maxEventIDBytes+1), body)); !errors.Is(err, ErrEventIDTooLong) {
		t.Fatalf("long delivery err = %v", err)
	}
	// The raw delivery id never reaches the audit trail.
	for _, a := range f.audits {
		if strings.Contains(a.EventHash, "dlv-1") {
			t.Fatalf("audit carries the raw delivery id: %+v", a)
		}
	}
}

func TestGitHubEventFilter(t *testing.T) {
	f := newGHFix(t, nil)
	ctx := context.Background()
	cases := []struct {
		name, event, fixture string
		mut                  func(string) string
		ignored              string // "" = dispatched
	}{
		{"listed event+action", "pull_request", "pull_request_opened.json", nil, ""},
		{"listed comment", "issue_comment", "issue_comment_created.json", nil, ""},
		{"condition holds", "check_run", "check_run_completed_failure.json", nil, ""},
		{"condition fails", "check_run", "check_run_completed_success.json", nil, IgnoredConditionUnmet},
		{"unlisted action", "pull_request", "pull_request_opened.json", func(s string) string { return strings.Replace(s, `"action": "opened"`, `"action": "closed"`, 1) }, IgnoredEventNotListed},
		{"unlisted event", "push", "push.json", nil, IgnoredEventNotListed},
		{"ping", "ping", "ping.json", nil, IgnoredPing},
	}
	for i, tc := range cases {
		body := ghFixtureBody(t, tc.fixture)
		if tc.mut != nil {
			body = tc.mut(body)
		}
		before := len(f.submitted())
		res, err := f.eng.Invoke(ctx, ghReq(tc.event, "flt-"+strconv.Itoa(i), body))
		if err != nil {
			t.Errorf("%s: err = %v", tc.name, err)
			continue
		}
		if tc.ignored == "" {
			if res.Ignored || res.JobID == "" {
				t.Errorf("%s: res = %+v, want dispatched", tc.name, res)
			}
			continue
		}
		if !res.Ignored || res.Reason != tc.ignored || res.JobID != "" || res.RequestID == "" || len(f.submitted()) != before {
			t.Errorf("%s: res = %+v (submissions %d->%d), want ignored %q", tc.name, res, before, len(f.submitted()), tc.ignored)
		}
	}
	// Ignored deliveries are audited, not given a receipt: a later redelivery of the same
	// id after the config adds the event must still dispatch.
	foundIgnored := false
	for _, k := range f.kinds() {
		foundIgnored = foundIgnored || k == "trigger.ignored/github"
	}
	if !foundIgnored {
		t.Fatalf("no trigger.ignored audit in %v", f.kinds())
	}
	if _, err := f.eng.Invoke(ctx, ghReq("", "flt-x", "{}")); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("missing event header err = %v", err)
	}
}

func TestGitHubIgnoredDeliveryDoesNotClaimOrRateLimit(t *testing.T) {
	f := newGHFix(t, func(tc *config.TriggerConfig) { tc.Rate = config.TriggerRateConfig{PerMinute: 1, Burst: 1} })
	ctx := context.Background()
	push := ghFixtureBody(t, "push.json")
	for i := 0; i < 5; i++ {
		res, err := f.eng.Invoke(ctx, ghReq("push", "ign-"+strconv.Itoa(i), push))
		if err != nil || !res.Ignored {
			t.Fatalf("ignored #%d: res=%+v err=%v", i, res, err)
		}
	}
	// The single rate token is still there for a dispatching delivery.
	if res, err := f.eng.Invoke(ctx, ghReq("pull_request", "ign-pr", ghFixtureBody(t, "pull_request_opened.json"))); err != nil || res.JobID == "" {
		t.Fatalf("dispatch after ignores: res=%+v err=%v", res, err)
	}
	if rows, _ := f.eng.store.ListTriggerReceipts("gh", 10); len(rows) != 1 {
		t.Fatalf("receipts = %d, want only the dispatched one", len(rows))
	}
}

func TestGitHubWhitelistedVars(t *testing.T) {
	f := newGHFix(t, nil)
	ctx := context.Background()
	check := func(event, fixture string, want map[string]string) {
		t.Helper()
		before := len(f.submitted())
		if _, err := f.eng.Invoke(ctx, ghReq(event, "var-"+fixture, ghFixtureBody(t, fixture))); err != nil {
			t.Fatalf("%s: %v", fixture, err)
		}
		got := f.submitted()
		if len(got) != before+1 {
			t.Fatalf("%s: not dispatched", fixture)
		}
		vars := got[len(got)-1].TemplateVars
		if len(vars) != len(want) {
			t.Errorf("%s: vars = %v, want exactly %v", fixture, vars, want)
		}
		for k, v := range want {
			if vars[k] != v {
				t.Errorf("%s: var %s = %q, want %q", fixture, k, vars[k], v)
			}
		}
		for k, v := range vars {
			for _, marker := range []string{"SECRET-", "ignore previous", ghSecret} {
				if strings.Contains(v, marker) {
					t.Errorf("%s: var %s leaks payload content %q", fixture, k, marker)
				}
			}
		}
	}
	check("pull_request", "pull_request_opened.json", map[string]string{
		"repo": "acme-org/widgets", "number": "42", "title": "Add widget cache", "url": "https://github.example/acme-org/widgets/pull/42",
		"who": "octo-user", "branch": "feature/cache", "event": "pull_request", "action": "opened"})
	check("issue_comment", "issue_comment_created.json", map[string]string{
		"repo": "acme-org/widgets", "number": "17", "title": "Widget flakes on restart", "url": "https://github.example/acme-org/widgets/issues/17#issuecomment-5001",
		"who": "octo-reviewer", "branch": "", "event": "issue_comment", "action": "created"})
	check("check_run", "check_run_completed_failure.json", map[string]string{
		"repo": "acme-org/widgets", "number": "42", "title": "unit-tests", "url": "https://github.example/acme-org/widgets/runs/3001",
		"who": "ci-bot", "branch": "feature/cache", "event": "check_run", "action": "completed"})
}

func TestGitHubVarTooLongAndPushBranch(t *testing.T) {
	f := newGHFix(t, func(tc *config.TriggerConfig) {
		tc.GitHub.Events = []string{"push"}
		tc.Limits.MaxVarBytes = 8
		tc.Input.Vars = map[string]string{"sha": "github.sha"}
	})
	if _, err := f.eng.Invoke(context.Background(), ghReq("push", "p-1", ghFixtureBody(t, "push.json"))); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("oversize var err = %v", err)
	}
	f2 := newGHFix(t, func(tc *config.TriggerConfig) {
		tc.GitHub.Events = []string{"push"}
		tc.Input.Vars = map[string]string{"branch": "github.branch", "sha": "github.sha"}
	})
	if _, err := f2.eng.Invoke(context.Background(), ghReq("push", "p-2", ghFixtureBody(t, "push.json"))); err != nil {
		t.Fatal(err)
	}
	v := f2.submitted()[0].TemplateVars
	if v["branch"] != "main" || v["sha"] != strings.Repeat("4", 40) {
		t.Fatalf("push vars = %v", v)
	}
}

func TestGitHubMaxAge(t *testing.T) {
	body := ghFixtureBody(t, "check_run_completed_failure.json") // completed_at 2026-10-30T12:00:00Z
	f := newGHFix(t, func(tc *config.TriggerConfig) { tc.GitHub.MaxAgeSec = 3600 })
	if _, err := f.eng.Invoke(context.Background(), ghReq("check_run", "age-1", body)); !errors.Is(err, ErrReplay) {
		t.Fatalf("2h old event err = %v, want ErrReplay", err)
	}
	if got := f.kinds(); got[len(got)-1] != "trigger.replay_rejected/github" {
		t.Fatalf("audit = %v", got)
	}
	f.now = time.Date(2026, 10, 30, 12, 30, 0, 0, time.UTC)
	if res, err := f.eng.Invoke(context.Background(), ghReq("check_run", "age-2", body)); err != nil || res.JobID == "" {
		t.Fatalf("fresh event: res=%+v err=%v", res, err)
	}
	// A stale event never consumed a dedup slot: it is not "age-1 already seen".
	if rows, _ := f.eng.store.ListTriggerReceipts("gh", 10); len(rows) != 1 {
		t.Fatalf("receipts = %d, want 1", len(rows))
	}
	// Without max_age_sec the same old event dispatches.
	g := newGHFix(t, nil)
	if res, err := g.eng.Invoke(context.Background(), ghReq("check_run", "age-3", body)); err != nil || res.JobID == "" {
		t.Fatalf("no max age: res=%+v err=%v", res, err)
	}
}

func TestGitHubEndpointHeadersAndView(t *testing.T) {
	f := newGHFix(t, func(tc *config.TriggerConfig) { tc.GitHub.MaxAgeSec = 600 })
	ep, ok := f.eng.Endpoint("gh")
	if !ok {
		t.Fatal("no endpoint")
	}
	for _, want := range []string{GitHubSignatureHeader, GitHubDeliveryHeader, GitHubEventHeader} {
		found := false
		for _, h := range ep.Headers {
			found = found || strings.EqualFold(h, want)
		}
		if !found {
			t.Errorf("header %s missing from %v", want, ep.Headers)
		}
	}
	for _, h := range ep.Headers {
		if strings.EqualFold(h, "X-Gofer-Trigger-Signature") {
			t.Errorf("generic header %s must not be collected for github", h)
		}
	}
	v, ok := f.eng.Show("gh")
	if !ok || v.Provider != "github" || v.GitHub == nil || len(v.GitHub.Events) != 3 || len(v.GitHub.Conditions) != 1 || v.GitHub.MaxAgeSec != 600 {
		t.Fatalf("view = %+v", v)
	}
	if v.Auth.SignatureHeader != GitHubSignatureHeader || v.Auth.SecretEnv != "GOFER_TRIGGER_GH" || !v.Auth.SecretConfigured {
		t.Fatalf("auth view = %+v", v.Auth)
	}
	if strings.Contains(strings.Join(v.GitHub.Events, ","), ghSecret) {
		t.Fatal("view leaks the secret")
	}
	// A generic trigger keeps the old view.
	g := NewEngine(testCfg(nil), nil, nil)
	if gv, _ := g.Show("push"); gv.Provider != "generic" || gv.GitHub != nil {
		t.Fatalf("generic view = %+v", gv)
	}
}

func TestUnknownProviderIsRefused(t *testing.T) {
	t.Setenv("GOFER_TRIGGER_P0", testSecret)
	eng := NewEngine(testCfg(func(tc *config.TriggerConfig) { tc.Provider = "nope" }), nil, nil)
	body := `{"ref":"main"}`
	if _, err := eng.Invoke(context.Background(), TriggerRequest{TriggerName: "push", RawBody: []byte(body), Headers: sign(testSecret, 100000, []byte(body))}); !errors.Is(err, ErrUnknownTrigger) {
		t.Fatalf("err = %v", err)
	}
}
