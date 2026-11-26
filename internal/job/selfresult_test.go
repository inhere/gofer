package job

import (
	"errors"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/i18n"
)

// TestSubmitSelfResult: the report is stored and shown (live and after a re-read from the
// store), never changes the job's status, a second report is ErrSelfResultExists, and bad
// input is ErrInvalidRequest.
func TestSubmitSelfResult(t *testing.T) {
	t.Parallel()
	s := newTestService(t, t.TempDir())
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("status = %s, want done", final.Status)
	}
	for _, bad := range []SelfResultInput{
		{Decision: "maybe", Reason: "x"},
		{Decision: ResultPass, Reason: "  "},
	} {
		if _, err := s.SubmitSelfResult(final.ID, bad, final.ID); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("SubmitSelfResult(%+v) err = %v, want ErrInvalidRequest", bad, err)
		}
	}
	got, err := s.SubmitSelfResult(final.ID, SelfResultInput{Decision: ResultFail, Reason: "could not finish", ActionRequired: "look", Findings: []string{"a"}}, final.ID)
	if err != nil {
		t.Fatalf("SubmitSelfResult: %v", err)
	}
	if got.ResultDecision != ResultFail || got.Status != StatusDone {
		t.Fatalf("result = %q status = %s, want fail recorded and status unchanged (done)", got.ResultDecision, got.Status)
	}
	if _, err := s.SubmitSelfResult(final.ID, SelfResultInput{Decision: ResultPass, Reason: "again"}, final.ID); !errors.Is(err, ErrSelfResultExists) {
		t.Fatalf("second report err = %v, want ErrSelfResultExists", err)
	}
	rec, ok, err := s.meta.GetJob(final.ID)
	if err != nil || !ok {
		t.Fatalf("GetJob: %v %v", ok, err)
	}
	if r := fromRecord(rec); r.ResultDecision != ResultFail || r.ResultReason != "could not finish" || r.ResultAttempt < 1 {
		t.Fatalf("stored result = %+v", r)
	}
	var in SelfResultInput
	if !DecodeSelfResultReport(got.ResultReportJSON, &in) || in.ActionRequired != "look" || len(in.Findings) != 1 {
		t.Fatalf("report document = %q", got.ResultReportJSON)
	}
	if _, err := s.SubmitSelfResult("job-missing", SelfResultInput{Decision: ResultPass, Reason: "x"}, ""); err == nil {
		t.Fatal("unknown job accepted a report")
	}
}

// TestRulesSectionCarriesResultHint: the job rules section (the job-only injection) tells
// the agent about gofer_submit_result, in both languages.
func TestRulesSectionCarriesResultHint(t *testing.T) {
	t.Parallel()
	refs := []RuleRef{{Name: "r"}}
	bodies := map[string]string{"r": "body"}
	for _, lang := range []i18n.Lang{i18n.EN, i18n.ZhCN} {
		if got := renderRulesSectionIn(lang, refs, bodies); !strings.Contains(got, "gofer_submit_result") {
			t.Fatalf("%s rules section lacks the hint:\n%s", lang, got)
		}
	}
}

// TestResultHintNamesTheCLI (gofer-4qsq): the hint must always name the CLI subcommand
// that works without gofer's MCP (suga, codex, an exec job), in both languages; the MCP
// form is named only where it exists.
func TestResultHintNamesTheCLI(t *testing.T) {
	t.Parallel()
	for _, lang := range []i18n.Lang{i18n.EN, i18n.ZhCN} {
		got := renderRulesSectionHinted(lang, selfResultHintBoth, nil, nil)
		if !strings.Contains(got, "gofer job result --decision") {
			t.Fatalf("%s rules section lacks the CLI form:\n%s", lang, got)
		}
		if !strings.Contains(got, "--reason") {
			t.Fatalf("%s CLI form lacks --reason:\n%s", lang, got)
		}
		mcpOnly := renderRulesSectionHinted(lang, selfResultHintMCP, nil, nil)
		if !strings.Contains(mcpOnly, "gofer_submit_result") || strings.Contains(mcpOnly, "gofer job result") {
			t.Fatalf("%s must name the tool and not the CLI:\n%s", lang, mcpOnly)
		}
		none := renderRulesSectionHinted(lang, "", nil, nil)
		if strings.Contains(none, "gofer_submit_result") || strings.Contains(none, "gofer job result") {
			t.Fatalf("%s must be able to leave the hint out entirely:\n%s", lang, none)
		}
	}
}

// TestSelfResultHintKey: which form a job is told follows what gofer actually gave its
// agent — the steward has neither way, a resident session and the mailbox get the tool,
// and every other job is told both (gofer injects no gofer MCP into an ordinary job, but
// the agent's own config may carry one, and the CLI works either way).
func TestSelfResultHintKey(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		req  JobRequest
		want string
	}{
		{"steward", JobRequest{Steward: true}, ""},
		{"mailbox session", JobRequest{MailResident: true, Session: true}, selfResultHintMCP},
		{"resident session", JobRequest{Session: true}, selfResultHintMCP},
		{"leader round", JobRequest{Session: true, LeaderOfPlan: "p1"}, selfResultHintBoth},
		{"cli agent", JobRequest{}, selfResultHintBoth},
		{"one-shot job", JobRequest{Prompt: "x"}, selfResultHintBoth},
	}
	for _, tc := range cases {
		if got := selfResultHintKey(tc.req); got != tc.want {
			t.Errorf("%s: hint key = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestJobPromptNamesTheCLIReport (gofer-4qsq): the real Submit path injects the hint into
// the prompt the AGENT receives, and that hint names the CLI form — the whole point of
// the issue is a suga / codex job whose session has no gofer MCP to call.
func TestJobPromptNamesTheCLIReport(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s, _ := newRuleService(t, root, nil, map[string]string{"house-rules": "HOUSE-RULE"})

	cli := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "ok", Runner: "local",
		Cwd: ".", Prompt: "x", TimeoutSec: 30, Rules: []string{"house-rules"},
	})
	if cli.Status != StatusDone {
		t.Fatalf("cli job status = %s (err=%s)", cli.Status, cli.Error)
	}
	prompt := executedPromptOf(t, cli)
	for _, want := range []string{"gofer_submit_result", "gofer job result --decision", "--reason"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("the job's prompt must be told %q:\n%s", want, prompt)
		}
	}
}
