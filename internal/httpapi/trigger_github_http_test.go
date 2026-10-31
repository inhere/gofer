package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
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

const ghPRBody = `{"action":"opened","number":42,"pull_request":{"number":42,"title":"Add widget cache","html_url":"https://github.example/acme-org/widgets/pull/42","body":"SECRET-PR-BODY-MARKER","head":{"ref":"feature/cache","sha":"1111"},"base":{"ref":"main"}},"repository":{"full_name":"acme-org/widgets"},"sender":{"login":"octo-user"}}`

// newGitHubHTTPServer is newTriggerHTTPServer plus one github trigger "gh" whose task book
// runs an echoing cli-agent, so the rendered prompt can be read back from the job.
func newGitHubHTTPServer(t *testing.T) (*Server, *core.Core, *trigger.Engine) {
	t.Helper()
	t.Setenv("GOFER_TRIGGER_HTTP_TEST", triggerTestSecret)
	yamlText := triggerHTTPFixture(t)
	const gh = `    - name: gh
      enabled: true
      provider: github
      owner_caller: web-op
      project: seed
      template: run-gh
      auth: {secret_env: GOFER_TRIGGER_HTTP_TEST}
      github:
        events: [pull_request.opened, check_run.completed]
        conditions:
          - {event: check_run.completed, field: check_run.conclusion, equals: [failure]}
      input:
        vars: {repo: github.repository, number: github.number, title: github.title, branch: github.branch}
`
	yamlText = strings.Replace(yamlText, "  triggers:\n", "  triggers:\n"+gh, 1)
	s, cr, _ := newConfigWriteTestServer(t, yamlText, agent.NoopDetector{})
	hostDir := ""
	if proj, ok := cr.Config().Projects["seed"]; ok {
		hostDir = cr.Config().ExecPath(proj)
	}
	tpl := "---\nagent: echoer\nrunner: local\nvars:\n  repo: {desc: repo}\n  number: {desc: number}\n  title: {desc: title}\n  branch: {desc: branch}\n---\ngithub-ran {{repo}} #{{number}} {{title}} on {{branch}}\n"
	if err := os.WriteFile(filepath.Join(hostDir, ".gofer", "templates", "run-gh.md"), []byte(tpl), 0o600); err != nil {
		t.Fatal(err)
	}
	eng := trigger.NewServerEngine(cr.Config, cr.Jobs, s.CallerHasPermission)
	eng.SetClock(func() time.Time { return time.Unix(triggerHTTPNow, 0) })
	s.SetTriggerEngine(eng)
	grantUsersSubmit(s, true)
	return s, cr, eng
}

func ghHeaders(secret, event, delivery, body string) map[string]string {
	h := map[string]string{trigger.GitHubSignatureHeader: trigger.SignGitHub(secret, []byte(body)), trigger.GitHubEventHeader: event}
	if delivery != "" {
		h[trigger.GitHubDeliveryHeader] = delivery
	}
	return h
}

// TestGitHubTriggerStartsARealJob: a signed pull_request.opened delivery over HTTP makes
// a job whose prompt holds the whitelisted fields and none of the other payload text; the
// redelivery is a 200 duplicate, a reused delivery id with another body a 409.
func TestGitHubTriggerStartsARealJob(t *testing.T) {
	s, cr, _ := newGitHubHTTPServer(t)
	resp, raw := invokeTrigger(t, s, "gh", ghPRBody, ghHeaders(triggerTestSecret, "pull_request", "dlv-http-1", ghPRBody))
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("delivery = %d %s", resp.StatusCode, raw)
	}
	var out triggerInvokeResponse
	decodeBytes(t, raw, &out)
	if out.JobID == "" || out.Ignored || out.Duplicate {
		t.Fatalf("answer = %+v", out)
	}
	rec, ok, err := cr.Jobs.Meta().GetJob(out.JobID)
	if err != nil || !ok {
		t.Fatalf("job %q: ok=%v err=%v", out.JobID, ok, err)
	}
	if rec.Agent != "echoer" || rec.OriginCaller != "web-op" || rec.Channel != "trigger" || rec.CallerID != "" {
		t.Fatalf("job agent=%q origin=%q channel=%q caller=%q", rec.Agent, rec.OriginCaller, rec.Channel, rec.CallerID)
	}
	if !strings.Contains(rec.RequestJSON, "github-ran acme-org/widgets #42 Add widget cache on feature/cache") {
		t.Fatalf("prompt not rendered from the whitelisted fields: %s", rec.RequestJSON)
	}
	for _, leak := range []string{"SECRET-PR-BODY-MARKER", triggerTestSecret} {
		if strings.Contains(rec.RequestJSON, leak) {
			t.Fatalf("job request leaks %q", leak)
		}
	}
	if got, ok := cr.Jobs.WaitFor(out.JobID, wait.Timeout(t, 30*time.Second)); !ok || got.Status != "done" {
		t.Fatalf("job result = %+v ok=%v, want done", got, ok)
	}

	resp, raw = invokeTrigger(t, s, "gh", ghPRBody, ghHeaders(triggerTestSecret, "pull_request", "dlv-http-1", ghPRBody))
	var dup triggerInvokeResponse
	decodeBytes(t, raw, &dup)
	if resp.StatusCode != http.StatusOK || !dup.Duplicate || dup.JobID != out.JobID {
		t.Fatalf("redelivery = %d %s", resp.StatusCode, raw)
	}
	other := strings.Replace(ghPRBody, "Add widget cache", "Other title", 1)
	resp, raw = invokeTrigger(t, s, "gh", other, ghHeaders(triggerTestSecret, "pull_request", "dlv-http-1", other))
	if resp.StatusCode != http.StatusConflict || errCode(t, raw) != "trigger_event_conflict" {
		t.Fatalf("reused delivery id = %d %s", resp.StatusCode, raw)
	}
}

// TestGitHubTriggerStatusTable: signature failures are 401, an ignored event or a ping is
// a 200 {"ignored":true,"reason":...} with no job, and a missing header is a 400.
func TestGitHubTriggerStatusTable(t *testing.T) {
	s, _, _ := newGitHubHTTPServer(t)
	failing := `{"action":"completed","check_run":{"name":"unit","conclusion":"failure","head_sha":"3333"},"repository":{"full_name":"acme-org/widgets"},"sender":{"login":"ci-bot"}}`
	passing := strings.Replace(failing, "failure", "success", 1)
	missingDelivery := ghHeaders(triggerTestSecret, "pull_request", "", ghPRBody)
	noEvent := ghHeaders(triggerTestSecret, "", "dlv-x", ghPRBody)

	cases := []struct {
		name    string
		body    string
		headers map[string]string
		status  int
		code    string // error code, or ignore reason when status is 200
	}{
		{"wrong secret", ghPRBody, ghHeaders("another-secret", "pull_request", "d1", ghPRBody), 401, "trigger_unauthenticated"},
		{"no signature", ghPRBody, map[string]string{trigger.GitHubEventHeader: "pull_request", trigger.GitHubDeliveryHeader: "d2"}, 401, "trigger_unauthenticated"},
		{"generic headers only", ghPRBody, signedHeaders(triggerTestSecret, triggerHTTPNow, ghPRBody, map[string]string{trigger.GitHubEventHeader: "pull_request", trigger.GitHubDeliveryHeader: "d3"}), 401, "trigger_unauthenticated"},
		{"tampered body", ghPRBody + " ", ghHeaders(triggerTestSecret, "pull_request", "d4", ghPRBody), 401, "trigger_unauthenticated"},
		{"missing delivery id", ghPRBody, missingDelivery, 400, "trigger_input_invalid"},
		{"missing event header", ghPRBody, noEvent, 400, "trigger_input_invalid"},
		{"form encoded body", "payload=%7B%7D", ghHeaders(triggerTestSecret, "pull_request", "d5", "payload=%7B%7D"), 400, "trigger_body_not_json"},
		{"ping", `{"zen":"x","hook_id":1}`, ghHeaders(triggerTestSecret, "ping", "d6", `{"zen":"x","hook_id":1}`), 200, trigger.IgnoredPing},
		{"unlisted event", `{"ref":"refs/heads/main"}`, ghHeaders(triggerTestSecret, "push", "d7", `{"ref":"refs/heads/main"}`), 200, trigger.IgnoredEventNotListed},
		{"unlisted action", strings.Replace(ghPRBody, "opened", "closed", 1), ghHeaders(triggerTestSecret, "pull_request", "d8", strings.Replace(ghPRBody, "opened", "closed", 1)), 200, trigger.IgnoredEventNotListed},
		{"condition unmet", passing, ghHeaders(triggerTestSecret, "check_run", "d9", passing), 200, trigger.IgnoredConditionUnmet},
	}
	for _, tc := range cases {
		resp, raw := invokeTrigger(t, s, "gh", tc.body, tc.headers)
		if resp.StatusCode != tc.status {
			t.Errorf("%s: %d %s, want %d", tc.name, resp.StatusCode, raw, tc.status)
			continue
		}
		if tc.status == 200 {
			var out triggerInvokeResponse
			decodeBytes(t, raw, &out)
			if !out.Ignored || out.Reason != tc.code || out.JobID != "" || out.RequestID == "" || out.Duplicate {
				t.Errorf("%s: answer = %s, want ignored %q", tc.name, raw, tc.code)
			}
		} else if errCode(t, raw) != tc.code {
			t.Errorf("%s: code = %s, want %s", tc.name, raw, tc.code)
		}
	}
	// An ignored delivery does not hold a dedup slot: the same id with a body that now
	// matches dispatches.
	if resp, raw := invokeTrigger(t, s, "gh", failing, ghHeaders(triggerTestSecret, "check_run", "d9", failing)); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("failing check after the ignored one = %d %s", resp.StatusCode, raw)
	}
}

// TestGitHubTriggerLeaksNothing: responses, logs, audit and the read views carry neither
// the secret, the signature, the delivery id nor any payload text.
func TestGitHubTriggerLeaksNothing(t *testing.T) {
	logs := captureSlog(t)
	s, _, eng := newGitHubHTTPServer(t)
	var mu sync.Mutex
	var audit []jobstore.TriggerAuditEvent
	eng.SetAudit(func(ev jobstore.TriggerAuditEvent) error {
		mu.Lock()
		audit = append(audit, ev)
		mu.Unlock()
		return nil
	})
	good := ghHeaders(triggerTestSecret, "pull_request", "evt-gh-secret-id", ghPRBody)
	var answers []string
	for _, c := range []struct {
		event string
		h     map[string]string
	}{
		{"pull_request", good}, {"pull_request", good},
		{"pull_request", ghHeaders("another-secret", "pull_request", "evt-gh-bad", ghPRBody)},
		{"ping", ghHeaders(triggerTestSecret, "ping", "evt-gh-ping", ghPRBody)},
	} {
		_, raw := invokeTrigger(t, s, "gh", ghPRBody, c.h)
		answers = append(answers, raw)
	}
	view, ok := eng.Show("gh")
	if !ok || view.Provider != "github" || view.GitHub == nil || len(view.GitHub.Events) != 2 {
		t.Fatalf("view = %+v", view)
	}
	mu.Lock()
	auditText := ""
	for _, ev := range audit {
		auditText += ev.Kind + "|" + ev.Provider + "|" + ev.Code + "|" + ev.EventHash + "|" + ev.Remote + "\n"
		if ev.Provider != "github" {
			t.Errorf("audit provider = %q", ev.Provider)
		}
	}
	mu.Unlock()
	if !strings.Contains(auditText, "trigger.ignored|github|ignored: ping") {
		t.Fatalf("ignored ping not audited:\n%s", auditText)
	}
	for where, text := range map[string]string{"responses": strings.Join(answers, "\n"), "logs": logs.String(), "audit": auditText} {
		for _, leak := range []string{triggerTestSecret, good[trigger.GitHubSignatureHeader], strings.TrimPrefix(good[trigger.GitHubSignatureHeader], "sha256="), "evt-gh-secret-id", "SECRET-PR-BODY-MARKER", "Add widget cache", "octo-user"} {
			if strings.Contains(text, leak) {
				t.Errorf("%s contain %q", where, leak)
			}
		}
	}
}
