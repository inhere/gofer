package job

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/testcmd"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// fakeChecker answers the delivery check from a script: each call pops the next
// answer (a JSON document or an error); an exhausted script answers pass.
type fakeChecker struct {
	mu    sync.Mutex
	outs  []fakeCheckAnswer
	calls []DeliveryCheckRequest
}

type fakeCheckAnswer struct {
	out string
	err error
}

func (f *fakeChecker) RunCheck(_ context.Context, req DeliveryCheckRequest) (DeliveryCheckOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, req)
	if len(f.calls) > len(f.outs) {
		return DeliveryCheckOutput{Output: []byte(checkPassJSON), JobID: "chk-fake", SessionID: "chk-sess"}, nil
	}
	a := f.outs[len(f.calls)-1]
	if a.err != nil {
		return DeliveryCheckOutput{}, a.err
	}
	return DeliveryCheckOutput{Output: []byte(a.out), JobID: "chk-fake", SessionID: "chk-sess"}, nil
}

func (f *fakeChecker) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

const checkPassJSON = `{"schema":"job-check-result/v1","verdict":"pass","items":[{"id":"c1","requirement":"two commits","passed":true,"evidence":"found 2"}],"summary":"all good"}`

const checkFailJSON = `{"schema":"job-check-result/v1","verdict":"fail","items":[` +
	`{"id":"c1","source":"explicit","requirement":"two commits","passed":false,"evidence":"found 1","action":"commit the rest"},` +
	`{"id":"a1","source":"acceptance","requirement":"tests added","passed":false,"evidence":"none","action":"add tests"},` +
	`{"id":"ok","source":"prompt_block","requirement":"report lists open items","passed":true,"evidence":"yes"}],"summary":"two unmet"}`

type checkSvcOpts struct {
	dc     config.DeliveryCheckConfig
	answer []fakeCheckAnswer
}

// newCheckService is a service whose "codex" cli-agent is resumable and ends 0, with
// the delivery check configured and its checker replaced by a scripted fake.
func newCheckService(t *testing.T, opts checkSvcOpts) (*Service, *fakeChecker) {
	t.Helper()
	root := t.TempDir()
	bin := testcmd.Path(t)
	cfg := &config.Config{
		Server:  config.ServerConfig{DeliveryCheck: opts.dc},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath: root, AllowedAgents: []string{"codex", "checker", "exec"},
				AllowedRunners: []string{"local"}, AllowExec: true,
			},
		},
		Agents: map[string]config.AgentConfig{
			"codex": {
				Type: agent.TypeCLIAgent, Command: bin,
				Args:          []string{"stderr-exit", "0", "", "{{prompt}}"},
				SessionResume: []string{"stderr-exit", "0", "resumed {{prompt}}"},
			},
		},
	}
	s := newServiceFromCfg(t, root, cfg)
	fake := &fakeChecker{outs: opts.answer}
	s.SetDeliveryChecker(fake)
	return s, fake
}

func enabledCheck(maxResumes int) config.DeliveryCheckConfig {
	return config.DeliveryCheckConfig{Enabled: true, Agent: "checker", MaxAutoResumes: &maxResumes}
}

func checkReq(mutate func(*JobRequest)) JobRequest {
	req := JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local", Prompt: "do the thing", Cwd: ".",
		TimeoutSec: 30, SessionID: "sess-check", CheckPrompt: "at least two commits",
	}
	if mutate != nil {
		mutate(&req)
	}
	return req
}

// settled waits until the job has fully ended (finish returned) and returns its stored row.
func settled(t *testing.T, s *Service, id string) JobResult {
	t.Helper()
	if _, ok := s.Wait(id); !ok {
		t.Fatalf("job %s not found", id)
	}
	got, ok := s.Get(id)
	if !ok {
		t.Fatalf("job %s vanished", id)
	}
	return got
}

// nextInChain waits for the continuation a failed check started and settles it.
func nextInChain(t *testing.T, s *Service, id string) JobResult {
	t.Helper()
	var next string
	wait.For(t, 10*time.Second, "continuation of "+id, func() (bool, any) {
		got, _ := s.Get(id)
		next = got.AutoResumedBy
		return next != "", got.Status
	})
	return settled(t, s, next)
}

func jobComments(t *testing.T, s *Service, id string) []string {
	t.Helper()
	list, err := s.meta.ListComments(jobstore.CommentScopeJob, id)
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}
	out := make([]string, 0, len(list))
	for _, c := range list {
		out = append(out, c.Body)
	}
	return out
}

func TestExtractCheckBlocks(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"single", "intro\n<delivery-check>\n- a\n- b\n</delivery-check>\nouter", []string{"- a\n- b"}},
		{"multiple in order", "<delivery-check>\none\n</delivery-check>\ntext\n<delivery-check>\ntwo\n</delivery-check>", []string{"one", "two"}},
		{"fenced marker ignored", "```\n<delivery-check>\nhidden\n</delivery-check>\n```\n", nil},
		{"tilde fence ignored", "~~~md\n<delivery-check>\nhidden\n</delivery-check>\n~~~", nil},
		{"fence closes then block counts", "```\nx\n```\n<delivery-check>\nreal\n</delivery-check>", []string{"real"}},
		{"not alone on its line", "see <delivery-check> inline </delivery-check>\n<delivery-check>x</delivery-check>", nil},
		{"indented tags count", "  <delivery-check>\n  body\n  </delivery-check>  ", []string{"body"}},
		{"unclosed dropped", "<delivery-check>\nnever closed", nil},
		{"empty block skipped", "<delivery-check>\n\n</delivery-check>", nil},
		{"crlf", "<delivery-check>\r\nwin\r\n</delivery-check>\r\n", []string{"win"}},
	}
	for _, tc := range cases {
		got := ExtractCheckBlocks(tc.in)
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

func TestApplyPromptCheckBlocksKeepsPromptAndIsIdempotent(t *testing.T) {
	t.Parallel()
	prompt := "task\n<delivery-check>\n- two commits\n</delivery-check>\n"
	req := JobRequest{Prompt: prompt}
	applyPromptCheckBlocks(&req)
	if req.CheckPrompt != "- two commits" || req.CheckPromptSource != CheckPromptSourcePromptBlock {
		t.Fatalf("block only: %q / %q", req.CheckPrompt, req.CheckPromptSource)
	}
	if req.Prompt != prompt {
		t.Fatalf("the block must stay in the prompt: %q", req.Prompt)
	}
	applyPromptCheckBlocks(&req)
	if req.CheckPrompt != "- two commits" {
		t.Fatalf("second pass must not duplicate: %q", req.CheckPrompt)
	}

	both := JobRequest{Prompt: prompt, CheckPrompt: "report open items", CheckPromptSource: CheckPromptSourceExplicit}
	applyPromptCheckBlocks(&both)
	if both.CheckPromptSource != CheckPromptSourceMerged ||
		!strings.Contains(both.CheckPrompt, "### explicit\nreport open items") ||
		!strings.Contains(both.CheckPrompt, "### prompt_block\n- two commits") {
		t.Fatalf("merged = %q / %q", both.CheckPrompt, both.CheckPromptSource)
	}
	before := both.CheckPrompt
	applyPromptCheckBlocks(&both)
	if both.CheckPrompt != before {
		t.Fatalf("merged requirement changed on re-entry: %q", both.CheckPrompt)
	}
}

func TestSubmitMergesCheckSourcesAndBoundsThem(t *testing.T) {
	t.Parallel()
	s, _ := newCheckService(t, checkSvcOpts{})
	prompt := "do it\n<delivery-check>\n- from the book\n</delivery-check>\n"
	res := settled(t, s, mustSubmit(t, s, checkReq(func(r *JobRequest) { r.Prompt = prompt })).ID)
	if res.CheckPromptSource != CheckPromptSourceMerged ||
		!strings.Contains(res.CheckPrompt, "at least two commits") || !strings.Contains(res.CheckPrompt, "- from the book") {
		t.Fatalf("merged requirement = %q (%s)", res.CheckPrompt, res.CheckPromptSource)
	}
	if !strings.Contains(res.RequestJSON, `\u003cdelivery-check\u003e`) {
		t.Fatalf("the block must stay in the prompt sent to the agent: %s", res.RequestJSON)
	}

	// The limit applies to the merged text as a whole.
	big := strings.Repeat("x", CheckPromptMaxBytes-100)
	_, err := s.Submit(checkReq(func(r *JobRequest) {
		r.CheckPrompt = big
		r.Prompt = "<delivery-check>\n" + strings.Repeat("y", 400) + "\n</delivery-check>"
	}))
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("merged over the limit: err = %v, want ErrInvalidRequest", err)
	}
}

func TestCheckRequirementsSections(t *testing.T) {
	t.Parallel()
	snap := JobResult{CheckPrompt: "be explicit", CheckPromptSource: CheckPromptSourceExplicit, Acceptance: "- ok"}
	got := checkRequirements(snap, true)
	if !strings.Contains(got, "### explicit\nbe explicit") || !strings.Contains(got, "### acceptance\n- ok") {
		t.Fatalf("explicit + acceptance = %q", got)
	}
	if strings.Contains(checkRequirements(snap, false), "acceptance") {
		t.Fatalf("include_acceptance=false must drop the acceptance section")
	}
	if checkRequirements(JobResult{Acceptance: "- ok"}, false) != "" {
		t.Fatalf("acceptance alone with include_acceptance=false has nothing to check")
	}
	block := JobResult{CheckPrompt: "from book", CheckPromptSource: CheckPromptSourcePromptBlock}
	if got := checkRequirements(block, true); got != "### prompt_block\nfrom book" {
		t.Fatalf("prompt_block = %q", got)
	}
	merged := JobResult{CheckPrompt: "### explicit\na\n\n### prompt_block\nb", CheckPromptSource: CheckPromptSourceMerged}
	if got := checkRequirements(merged, true); got != merged.CheckPrompt {
		t.Fatalf("merged text must pass through, got %q", got)
	}
	huge := JobResult{CheckPrompt: strings.Repeat("z", CheckPromptMaxBytes), Acceptance: strings.Repeat("a", 1000)}
	if got := checkRequirements(huge, true); len(got) > CheckPromptMaxBytes {
		t.Fatalf("requirements must fit the limit, got %d bytes", len(got))
	}
}

func TestBuildCheckInputEvidence(t *testing.T) {
	t.Parallel()
	s, _ := newCheckService(t, checkSvcOpts{})
	snap := JobResult{
		ID: "job-no-log", DiffSummary: " a.go | 3 ++-\n 2 files changed, 10 insertions(+), 4 deletions(-)",
		Commits:          []Commit{{SHA: "abc", Subject: "feat: x"}},
		UncommittedFiles: []string{"dirty.go"}, UncommittedCount: 3, BaseSHA: "base1", WorktreeHeadSHA: "head1",
	}
	in, _ := s.buildCheckInput(snap, "### explicit\nr", config.DeliveryCheckConfig{})
	if in.ReportTail != checkReportMissing {
		t.Fatalf("a missing report must be marked unavailable, got %q", in.ReportTail)
	}
	if in.DiffSummary != (CheckDiffSummary{Files: 2, Insertions: 10, Deletions: 4}) {
		t.Fatalf("diff = %+v", in.DiffSummary)
	}
	if in.Uncommitted.Count != 3 || !in.Uncommitted.Truncated || len(in.Uncommitted.Paths) != 1 {
		t.Fatalf("uncommitted = %+v", in.Uncommitted)
	}
	if in.Worktree.Base != "base1" || in.Worktree.Head != "head1" || len(in.Commits) != 1 {
		t.Fatalf("worktree/commits = %+v / %+v", in.Worktree, in.Commits)
	}
	if err := in.Validate(); err != nil {
		t.Fatalf("evidence must validate: %v", err)
	}
	many := JobResult{ID: "x"}
	for i := 0; i < 80; i++ {
		many.Commits = append(many.Commits, Commit{SHA: "s", Subject: "c"})
	}
	in, truncated := s.buildCheckInput(many, "r", config.DeliveryCheckConfig{})
	if len(in.Commits) != checkMaxCommits || !truncated {
		t.Fatalf("commits = %d truncated=%v, want %d / true", len(in.Commits), truncated, checkMaxCommits)
	}
}

func TestDeliveryCheckPassKeepsOutcome(t *testing.T) {
	t.Parallel()
	s, fake := newCheckService(t, checkSvcOpts{dc: enabledCheck(2), answer: []fakeCheckAnswer{{out: checkPassJSON}}})
	final := settled(t, s, mustSubmit(t, s, checkReq(nil)).ID)
	if final.Status != StatusDone {
		t.Fatalf("status = %s, want done (err=%s)", final.Status, final.Error)
	}
	if fake.count() != 1 {
		t.Fatalf("checker calls = %d, want 1", fake.count())
	}
	sc, ok := DecodeStoredCheck(final.CheckJSON)
	if !ok || sc.Verdict != "pass" || sc.Action != checkActionPass || sc.Round != 1 || sc.CheckJobID != "chk-fake" || sc.Source != CheckPromptSourceExplicit {
		t.Fatalf("stored check = %+v ok=%v", sc, ok)
	}
	types := eventTypes(t, s, final.ID)
	if !hasSubsequence(types, []string{EventDeliveryCheckStarted, EventDeliveryCheckPassed, EventJobTerminal}) {
		t.Fatalf("events = %v", types)
	}
	if c := jobComments(t, s, final.ID); len(c) != 1 || !strings.Contains(c[0], "合格") {
		t.Fatalf("comments = %v", c)
	}
	// The request the checker got is a bounded, tool-less one-shot carrying the evidence.
	call := fake.calls[0]
	if call.Agent != "checker" || call.SourceJobID != final.ID || !strings.Contains(call.Prompt, "job-check/v1") || !strings.Contains(call.Prompt, "at least two commits") {
		t.Fatalf("check request = %+v", call)
	}
}

func TestDeliveryCheckPassThenReview(t *testing.T) {
	t.Parallel()
	s, _ := newCheckService(t, checkSvcOpts{dc: enabledCheck(2)})
	final := settled(t, s, mustSubmit(t, s, checkReq(func(r *JobRequest) { r.Review = true })).ID)
	if final.Status != StatusNeedsReview {
		t.Fatalf("a passing check must keep the review path, status = %s", final.Status)
	}
	if sc, _ := DecodeStoredCheck(final.CheckJSON); sc.Verdict != "pass" {
		t.Fatalf("check not recorded before the review state: %q", final.CheckJSON)
	}
}

func TestDeliveryCheckFailResumesWithItemsAndSources(t *testing.T) {
	t.Parallel()
	s, fake := newCheckService(t, checkSvcOpts{dc: enabledCheck(2),
		answer: []fakeCheckAnswer{{out: checkFailJSON}, {out: checkPassJSON}}})
	src := settled(t, s, mustSubmit(t, s, checkReq(func(r *JobRequest) { r.Acceptance = "- tests added" })).ID)
	if src.Status != StatusFailed || !strings.Contains(src.Error, "delivery check failed") {
		t.Fatalf("source status = %s err=%q, want a failed-type intermediate", src.Status, src.Error)
	}
	if src.CheckResumeCount != 1 {
		t.Fatalf("source check_resume_count = %d, want 1", src.CheckResumeCount)
	}
	cont := nextInChain(t, s, src.ID)
	if cont.Status != StatusDone || cont.ResumedFrom != src.ID || cont.CheckResumeCount != 1 {
		t.Fatalf("continuation = %s resumedFrom=%s count=%d", cont.Status, cont.ResumedFrom, cont.CheckResumeCount)
	}
	if cont.AutoResumeAttempt != 0 {
		t.Fatalf("the check resume must not spend the transient budget: attempt=%d", cont.AutoResumeAttempt)
	}
	if fake.count() != 2 {
		t.Fatalf("checker calls = %d, want 2 (one per round)", fake.count())
	}
	// The continuation's prompt names the unmet items with their sources and drops the met one.
	rec, _, _ := s.meta.GetJob(cont.ID)
	for _, want := range []string{"[explicit] two commits", "commit the rest", "[acceptance] tests added", "add tests"} {
		if !strings.Contains(rec.RequestJSON, want) && !strings.Contains(rec.RenderedCommand, want) {
			t.Fatalf("continuation misses %q\nrequest: %s\ncommand: %s", want, rec.RequestJSON, rec.RenderedCommand)
		}
	}
	if strings.Contains(rec.RequestJSON, "report lists open items") {
		t.Fatalf("a met item must not be in the continuation prompt: %s", rec.RequestJSON)
	}
	if !hasSubsequence(eventTypes(t, s, src.ID), []string{EventDeliveryCheckStarted, EventDeliveryCheckFailed, EventDeliveryCheckAutoResumed}) {
		t.Fatalf("source events = %v", eventTypes(t, s, src.ID))
	}
	for _, ty := range eventTypes(t, s, src.ID) {
		if ty == EventJobTerminal {
			t.Fatalf("a continued source must not announce job.terminal: %v", eventTypes(t, s, src.ID))
		}
	}
	if sc, _ := DecodeStoredCheck(cont.CheckJSON); sc.Verdict != "pass" || sc.Round != 2 {
		t.Fatalf("continuation check = %+v", sc)
	}
	if c := jobComments(t, s, src.ID); len(c) != 1 || !strings.Contains(c[0], "已自动续接 "+cont.ID) {
		t.Fatalf("source comments = %v", c)
	}
}

func TestDeliveryCheckStopsAtMaxResumesIntoReview(t *testing.T) {
	t.Parallel()
	s, fake := newCheckService(t, checkSvcOpts{dc: enabledCheck(2),
		answer: []fakeCheckAnswer{{out: checkFailJSON}, {out: checkFailJSON}, {out: checkFailJSON}}})
	first := settled(t, s, mustSubmit(t, s, checkReq(nil)).ID)
	second := nextInChain(t, s, first.ID)
	third := nextInChain(t, s, second.ID)
	if first.Status != StatusFailed || second.Status != StatusFailed {
		t.Fatalf("intermediate statuses = %s / %s, want failed / failed", first.Status, second.Status)
	}
	if third.Status != StatusNeedsReview || third.CheckResumeCount != 2 {
		t.Fatalf("last = %s count=%d, want needs_review / 2", third.Status, third.CheckResumeCount)
	}
	if fake.count() != 3 {
		t.Fatalf("checker calls = %d, want 3", fake.count())
	}
	if got, _ := s.Get(third.ID); got.AutoResumedBy != "" {
		t.Fatalf("no fourth round may start: %s", got.AutoResumedBy)
	}
	if sc, _ := DecodeStoredCheck(third.CheckJSON); sc.Action != checkActionReview || sc.Verdict != "fail" {
		t.Fatalf("final check = %+v", sc)
	}
	if c := jobComments(t, s, third.ID); len(c) != 1 || !strings.Contains(c[0], "转入待验收") {
		t.Fatalf("final comments = %v", c)
	}
}

func TestDeliveryCheckZeroMaxResumesGoesStraightToReview(t *testing.T) {
	t.Parallel()
	s, fake := newCheckService(t, checkSvcOpts{dc: enabledCheck(0), answer: []fakeCheckAnswer{{out: checkFailJSON}}})
	final := settled(t, s, mustSubmit(t, s, checkReq(nil)).ID)
	if final.Status != StatusNeedsReview || fake.count() != 1 || final.AutoResumedBy != "" {
		t.Fatalf("status=%s calls=%d resumedBy=%q, want needs_review / 1 / none", final.Status, fake.count(), final.AutoResumedBy)
	}
	for _, ty := range eventTypes(t, s, final.ID) {
		if ty == EventDeliveryCheckAutoResumed {
			t.Fatalf("max_auto_resumes=0 must not resume")
		}
	}
}

func TestDeliveryCheckFailWithoutSessionGoesToReview(t *testing.T) {
	t.Parallel()
	s, _ := newCheckService(t, checkSvcOpts{dc: enabledCheck(2), answer: []fakeCheckAnswer{{out: checkFailJSON}}})
	// An exec job has no agent session to continue.
	final := settled(t, s, mustSubmit(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: testcmd.Cmd(t, "exit", "0"), Cwd: ".",
		TimeoutSec: 30, CheckPrompt: "something",
	}).ID)
	if final.Status != StatusNeedsReview {
		t.Fatalf("status = %s, want needs_review when the job cannot be continued", final.Status)
	}
}

func TestDeliveryCheckErrorsAreFailOpen(t *testing.T) {
	t.Parallel()
	for name, ans := range map[string]fakeCheckAnswer{
		"agent error":   {err: errors.New("checker agent exploded")},
		"not json":      {out: "I think it is fine"},
		"extra text":    {out: checkPassJSON + "\nthanks"},
		"bad verdict":   {out: `{"schema":"job-check-result/v1","verdict":"maybe","items":[],"summary":""}`},
		"fail w/o item": {out: `{"schema":"job-check-result/v1","verdict":"fail","items":[],"summary":"x"}`},
	} {
		s, fake := newCheckService(t, checkSvcOpts{dc: enabledCheck(2), answer: []fakeCheckAnswer{ans}})
		final := settled(t, s, mustSubmit(t, s, checkReq(nil)).ID)
		if final.Status != StatusDone || final.AutoResumedBy != "" {
			t.Fatalf("%s: status=%s resumedBy=%q, want done and no resume", name, final.Status, final.AutoResumedBy)
		}
		if final.CheckError == "" || final.CheckJSON != "" || fake.count() != 1 {
			t.Fatalf("%s: check_error=%q check_json=%q calls=%d", name, final.CheckError, final.CheckJSON, fake.count())
		}
		types := eventTypes(t, s, final.ID)
		if !hasSubsequence(types, []string{EventDeliveryCheckStarted, EventDeliveryCheckError, EventJobTerminal}) {
			t.Fatalf("%s: events = %v", name, types)
		}
		if c := jobComments(t, s, final.ID); len(c) != 1 || !strings.Contains(c[0], "交付核验未完成") {
			t.Fatalf("%s: comments = %v", name, c)
		}
	}
}

func TestDeliveryCheckSkipRules(t *testing.T) {
	t.Parallel()
	off := false
	cases := []struct {
		name  string
		dc    config.DeliveryCheckConfig
		mut   func(*JobRequest)
		want  int // checker calls
		state string
	}{
		{"disabled", config.DeliveryCheckConfig{Agent: "checker"}, nil, 0, StatusDone},
		{"no requirement", enabledCheck(2), func(r *JobRequest) { r.CheckPrompt = "" }, 0, StatusDone},
		{"acceptance only, included", enabledCheck(2), func(r *JobRequest) { r.CheckPrompt, r.Acceptance = "", "- a" }, 1, StatusDone},
		{"acceptance only, excluded", config.DeliveryCheckConfig{Enabled: true, Agent: "checker", IncludeAcceptance: &off},
			func(r *JobRequest) { r.CheckPrompt, r.Acceptance = "", "- a" }, 0, StatusDone},
		{"prompt block only", enabledCheck(2), func(r *JobRequest) {
			r.CheckPrompt, r.Prompt = "", "x\n<delivery-check>\n- y\n</delivery-check>"
		}, 1, StatusDone},
		{"verify failed", enabledCheck(2), func(r *JobRequest) { r.Verify = testcmd.Cmd(t, "exit", "1") }, 0, StatusFailed},
		{"verify passed", enabledCheck(2), func(r *JobRequest) { r.Verify = testcmd.Cmd(t, "exit", "0") }, 1, StatusDone},
	}
	for _, tc := range cases {
		s, fake := newCheckService(t, checkSvcOpts{dc: tc.dc})
		final := settled(t, s, mustSubmit(t, s, checkReq(tc.mut)).ID)
		if fake.count() != tc.want {
			t.Fatalf("%s: checker calls = %d, want %d", tc.name, fake.count(), tc.want)
		}
		if final.Status != tc.state && !(tc.state == StatusFailed && final.Status == StatusNeedsReview) {
			t.Fatalf("%s: status = %s, want %s (err=%s)", tc.name, final.Status, tc.state, final.Error)
		}
		if tc.want == 0 {
			for _, ty := range eventTypes(t, s, final.ID) {
				if strings.HasPrefix(ty, "job.delivery_check.") {
					t.Fatalf("%s: unexpected event %s", tc.name, ty)
				}
			}
		}
	}
}

func TestDeliveryCheckAcceptanceReachesChecker(t *testing.T) {
	t.Parallel()
	s, fake := newCheckService(t, checkSvcOpts{dc: enabledCheck(2)})
	settled(t, s, mustSubmit(t, s, checkReq(func(r *JobRequest) { r.Acceptance = "- docs updated" })).ID)
	if fake.count() != 1 {
		t.Fatalf("calls = %d", fake.count())
	}
	p := fake.calls[0].Prompt
	// The evidence JSON is HTML-escaped; the section markers and criteria survive.
	if !strings.Contains(p, "### explicit") || !strings.Contains(p, "### acceptance") || !strings.Contains(p, "docs updated") {
		t.Fatalf("checker prompt lacks the sections:\n%s", p)
	}
}

func TestDeliveryCheckDailyLimitFailsOpen(t *testing.T) {
	t.Parallel()
	dc := enabledCheck(2)
	dc.DailyLimit = 1
	s, fake := newCheckService(t, checkSvcOpts{dc: dc})
	first := settled(t, s, mustSubmit(t, s, checkReq(nil)).ID)
	second := settled(t, s, mustSubmit(t, s, checkReq(nil)).ID)
	if fake.count() != 1 || first.CheckError != "" || second.Status != StatusDone || !strings.Contains(second.CheckError, "daily_limit") {
		t.Fatalf("calls=%d first.err=%q second=%s/%q", fake.count(), first.CheckError, second.Status, second.CheckError)
	}
}

func TestStoredCheckRoundTrips(t *testing.T) {
	t.Parallel()
	raw, _ := json.Marshal(StoredCheck{CheckResult: CheckResult{Schema: "job-check-result/v1", Verdict: "pass", Summary: "s"}, Round: 3, Action: checkActionPass})
	sc, ok := DecodeStoredCheck(string(raw))
	if !ok || sc.Round != 3 || sc.Verdict != "pass" {
		t.Fatalf("decoded = %+v ok=%v", sc, ok)
	}
	if _, ok := DecodeStoredCheck("not json"); ok {
		t.Fatalf("garbage must not decode")
	}
}

// TestDefaultCheckerRunsOneShotJob drives the production adapter end to end with a
// testcmd "agent" that prints a fixed answer: the checker is a hidden read-only job, and
// a project capped at one running job must not deadlock on the slot the checked job holds.
func TestDefaultCheckerRunsOneShotJob(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bin := testcmd.Path(t)
	cfg := &config.Config{
		Server:  config.ServerConfig{DeliveryCheck: config.DeliveryCheckConfig{Enabled: true, Agent: "checker", TimeoutSec: 30}},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {HostPath: root, AllowedAgents: []string{"codex", "checker"}, AllowedRunners: []string{"local"}, MaxConcurrentJobs: 1},
		},
		Agents: map[string]config.AgentConfig{
			"codex": {Type: agent.TypeCLIAgent, Command: bin, Args: []string{"stderr-exit", "0", "", "{{prompt}}"}},
			"checker": {Type: agent.TypeCLIAgent, Command: bin, Args: []string{"printf", checkPassJSON, "{{prompt}}"},
				ReadOnlyArgs: []string{"ro"}},
		},
	}
	s := newServiceFromCfg(t, root, cfg)
	final := settled(t, s, mustSubmit(t, s, checkReq(nil)).ID)
	if final.Status != StatusDone || final.CheckError != "" {
		t.Fatalf("status=%s check_error=%q err=%q", final.Status, final.CheckError, final.Error)
	}
	sc, ok := DecodeStoredCheck(final.CheckJSON)
	if !ok || sc.Verdict != "pass" || sc.CheckJobID == "" {
		t.Fatalf("stored = %+v ok=%v", sc, ok)
	}
	chk, found := s.Get(sc.CheckJobID)
	if !found || !chk.ReadOnly || !hiddenJobTag(chk.Tags) {
		t.Fatalf("checker job = %+v found=%v, want a hidden read-only job", chk, found)
	}
}
