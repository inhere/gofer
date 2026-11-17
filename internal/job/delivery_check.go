package job

// Delivery check (交付核验): after an agent job finished normally and its --verify step
// (if any) did not fail, the server judges the delivery against the job's requirements
// with a read-only, tool-less, single-turn checker agent, using evidence it prepared
// itself. A pass changes nothing; a fail continues the job (a bounded number of times)
// with the unmet items, then parks it in needs_review; a checker that itself breaks is
// fail-open (the job keeps its outcome, the error is recorded).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/bgagent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/i18n/prompts"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/oneshot"
	"github.com/inhere/gofer/internal/store"
)

// The requirement sections, also the values of CheckItem.Source.
const (
	checkSectionExplicit    = "explicit"
	checkSectionPromptBlock = "prompt_block"
	checkSectionAcceptance  = "acceptance"
)

const (
	checkMaxCommits     = 50
	checkMaxPaths       = 50
	checkReportMissing  = "unavailable"
	checkTruncatedMark  = "\n…[truncated]"
	checkErrorMaxBytes  = 512
	checkCommentSummary = 400
)

// DeliveryCheckRequest is what a DeliveryChecker is asked to run. The adapter fixes the
// safety shape (read-only, no tools, one turn); the caller supplies only the text.
type DeliveryCheckRequest struct {
	SourceJobID  string
	ProjectKey   string
	OriginCaller string
	Prompt       string
	Agent        string
	Model        string
	Args         []string
	Timeout      time.Duration
	// DailyLimit is server.delivery_check.daily_limit (<= 0 = none): the checker that
	// owns its run log (the default one) enforces it from background_agent_runs.
	DailyLimit int
}

// DeliveryCheckOutput is the raw answer of one checker run.
type DeliveryCheckOutput struct {
	Output    []byte
	JobID     string
	SessionID string
}

// DeliveryChecker is the seam to the checker agent. Tests inject a fake; production
// uses the one-shot job runner below.
type DeliveryChecker interface {
	RunCheck(ctx context.Context, req DeliveryCheckRequest) (DeliveryCheckOutput, error)
}

// SetDeliveryChecker replaces the checker (nil restores the default). Call it before
// jobs start.
func (s *Service) SetDeliveryChecker(c DeliveryChecker) { s.deliveryChecker = c }

// StoredCheck is the document kept in jobs.check_json: the checker's result plus how
// the check went. The prompt is not stored.
type StoredCheck struct {
	CheckResult
	Round          int    `json:"round"`
	Action         string `json:"action"` // pass | resume | review
	CheckedAt      int64  `json:"checked_at"`
	Agent          string `json:"agent,omitempty"`
	Model          string `json:"model,omitempty"`
	SessionID      string `json:"session_id,omitempty"`
	CheckJobID     string `json:"check_job_id,omitempty"`
	Source         string `json:"source,omitempty"`
	InputTruncated bool   `json:"input_truncated,omitempty"`
}

// Stored actions of a check.
const (
	checkActionPass   = "pass"
	checkActionResume = "resume"
	checkActionReview = "review"
)

// DecodeStoredCheck parses a jobs.check_json value; ok is false for "" or garbage.
func DecodeStoredCheck(raw string) (StoredCheck, bool) {
	if strings.TrimSpace(raw) == "" {
		return StoredCheck{}, false
	}
	var c StoredCheck
	if json.Unmarshal([]byte(raw), &c) != nil {
		return StoredCheck{}, false
	}
	return c, true
}

// checkDecision is what finish does with a check.
type checkDecision struct {
	ran    bool
	action string // "" (no change) | resume | review
	reason string // error text of the failed intermediate job (resume)
	stored StoredCheck
	err    string // checker infrastructure error ("" = none)
	// resumeErr is why the automatic continuation could not be submitted ("" = it was).
	resumeErr string
	max       int
}

// checkRequirements assembles the requirement text the checker judges: the explicit
// and prompt-block parts the request carries, then the acceptance criteria when the
// configuration asks for them. "" means the job has nothing to check.
func checkRequirements(snap JobResult, includeAcceptance bool) string {
	var parts []string
	cp := strings.TrimSpace(snap.CheckPrompt)
	switch {
	case cp == "":
	case snap.CheckPromptSource == CheckPromptSourceMerged:
		parts = append(parts, cp)
	case snap.CheckPromptSource == CheckPromptSourcePromptBlock:
		parts = append(parts, "### "+checkSectionPromptBlock+"\n"+cp)
	default:
		parts = append(parts, "### "+checkSectionExplicit+"\n"+cp)
	}
	if acc := strings.TrimSpace(snap.Acceptance); acc != "" && includeAcceptance {
		parts = append(parts, "### "+checkSectionAcceptance+"\n"+acc)
	}
	return truncateUTF8(strings.Join(parts, "\n\n"), CheckPromptMaxBytes)
}

// truncateUTF8 cuts s to at most max bytes on a rune boundary, marking the cut.
func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	keep := max - len(checkTruncatedMark)
	if keep < 0 {
		keep = 0
	}
	cut := s[:keep]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + checkTruncatedMark
}

var diffStatRe = map[string]*regexp.Regexp{
	"files":      regexp.MustCompile(`(\d+) files? changed`),
	"insertions": regexp.MustCompile(`(\d+) insertions?\(\+\)`),
	"deletions":  regexp.MustCompile(`(\d+) deletions?\(-\)`),
}

// parseDiffStat reads the trailing "N files changed, X insertions(+), Y deletions(-)"
// line of a `git diff --stat` summary; anything unparsable counts as zero.
func parseDiffStat(summary string) CheckDiffSummary {
	var out CheckDiffSummary
	get := func(key string) int {
		m := diffStatRe[key].FindStringSubmatch(summary)
		if m == nil {
			return 0
		}
		n, _ := strconv.Atoi(m[1])
		return n
	}
	out.Files, out.Insertions, out.Deletions = get("files"), get("insertions"), get("deletions")
	return out
}

// buildCheckInput prepares the bounded evidence of one check. truncated reports that
// the report tail or a list was cut to fit.
func (s *Service) buildCheckInput(snap JobResult, requirements string, dc config.DeliveryCheckConfig) (in CheckInput, truncated bool) {
	tailMax := dc.EffectiveReportTailBytes()
	if tailMax > CheckTextMaxBytes {
		tailMax = CheckTextMaxBytes
	}
	in = CheckInput{Schema: "job-check/v1", Requirements: requirements, Verify: snap.Verify}
	tail, err := s.TailLog(snap.ID, store.StreamStdout, int64(tailMax))
	switch {
	case err != nil || len(tail) == 0:
		in.ReportTail = checkReportMissing
	default:
		in.ReportTail = strings.ToValidUTF8(string(tail), "")
		truncated = len(tail) >= tailMax
	}
	commits := snap.Commits
	if len(commits) > checkMaxCommits {
		commits, truncated = commits[:checkMaxCommits], true
	}
	in.Commits = append([]Commit(nil), commits...)
	in.DiffSummary = parseDiffStat(snap.DiffSummary)
	paths := snap.UncommittedFiles
	if len(paths) > checkMaxPaths {
		paths = paths[:checkMaxPaths]
	}
	in.Uncommitted = CheckUncommitted{Count: snap.UncommittedCount, Paths: append([]string(nil), paths...), Truncated: snap.UncommittedCount > len(paths)}
	in.Worktree = CheckWorktree{Base: snap.BaseSHA, Head: snap.WorktreeHeadSHA}
	if snap.WorktreeBaseSHA != "" {
		in.Worktree.Base = snap.WorktreeBaseSHA
	}
	// Keep the whole document under input_max_bytes: shrink the report tail first.
	limit := dc.EffectiveInputMaxBytes()
	for {
		raw, merr := json.Marshal(in)
		if merr != nil || len(raw) <= limit || len(in.ReportTail) <= len(checkReportMissing) {
			break
		}
		over := len(raw) - limit
		keep := len(in.ReportTail) - over - 64
		if keep < 0 {
			keep = 0
		}
		// The tail is the END of the report: keep its last bytes.
		in.ReportTail = strings.ToValidUTF8(in.ReportTail[len(in.ReportTail)-keep:], "")
		truncated = true
		if keep == 0 {
			in.ReportTail = checkReportMissing
			break
		}
	}
	return in, truncated
}

// deliveryCheckApplies decides whether a finished job is checked and returns the
// requirement text. Checks run on the hub only (a worker service leaves transitions
// to it), for agent jobs that ended normally.
func (s *Service) deliveryCheckApplies(snap JobResult) (config.DeliveryCheckConfig, string, bool) {
	cfg := s.config()
	if cfg == nil || s.uncommittedDecisionOnly {
		return config.DeliveryCheckConfig{}, "", false
	}
	dc := cfg.Server.DeliveryCheck
	if !dc.Enabled {
		return dc, "", false
	}
	req := checkRequirements(snap, dc.EffectiveIncludeAcceptance())
	return dc, req, req != ""
}

// checkResumable says whether a failed check can be continued: the job has a session
// and an agent that can resume it (the same gate the other automatic continuations use).
func (s *Service) checkResumable(snap JobResult) bool {
	if snap.SessionID == "" {
		return false
	}
	if snap.Agent == agent.ExecAgentKey && snap.ResumedFrom != "" {
		return true
	}
	ac, ok := s.agents.Get(snap.Agent)
	return ok && resumable(ac)
}

// allowCheckToday gates one check run against server.delivery_check.daily_limit (a
// negative value disables the limit, see EffectiveDailyLimit). The count is per UTC day
// and lives in background_agent_runs (kind job_check), so it survives a restart. The
// default checker enforces the limit itself (oneshot.Invoker: guard and run log in one
// place); a checker injected with SetDeliveryChecker is gated here, and its run is
// recorded by recordInjectedCheckRun.
func (s *Service) allowCheckToday(dc config.DeliveryCheckConfig, project string) bool {
	limit := dc.EffectiveDailyLimit()
	if limit <= 0 || s.deliveryChecker == nil {
		return true
	}
	err := s.checkRunner().Guard(bgagent.Key{Kind: oneshot.KindJobCheck, Project: project}, bgagent.Limits{DailyRuns: limit})
	return err == nil
}

// checkRunner is the background-agent runner over the metadata store (the daily count,
// the run log and the hard-stop audit of the delivery check).
func (s *Service) checkRunner() *bgagent.Runner {
	return bgagent.New(s.meta, bgagent.Options{Now: s.nowFn})
}

// recordInjectedCheckRun logs one run of an injected checker so the daily count sees it
// (the default checker records its own runs through bgagent.Run).
func (s *Service) recordInjectedCheckRun(project, jobID string, started time.Time, runErr error) {
	status := jobstore.BackgroundRunOK
	errText := ""
	if runErr != nil {
		status, errText = jobstore.BackgroundRunFailed, truncateUTF8(runErr.Error(), checkErrorMaxBytes)
	}
	if _, err := s.meta.InsertBackgroundRun(jobstore.BackgroundRun{
		Kind: oneshot.KindJobCheck, Project: project, JobID: jobID, StartedAt: started.Unix(), EndedAt: s.nowFn().Unix(),
		Status: status, Mode: string(bgagent.ModeFresh), Error: errText,
	}); err != nil {
		slog.Warn("delivery check run log failed", "job_id", jobID, "err", err)
	}
}

// deliveryCheck runs the check of a job that ended normally and records its outcome on
// the entry (check_json / check_error / check_resume_count) and in events, BEFORE the
// job's terminal state becomes observable. It never fails the job: any problem of the
// checker itself returns a decision with action "" and err set (fail-open).
func (s *Service) deliveryCheck(entry *jobEntry, pre JobResult) checkDecision {
	dc, requirements, ok := s.deliveryCheckApplies(pre)
	if !ok {
		return checkDecision{}
	}
	d := checkDecision{ran: true, max: dc.EffectiveMaxAutoResumes()}
	round := pre.CheckResumeCount + 1
	detail := DeliveryCheckEventDetail{Round: round, MaxResumes: d.max, ResumeCount: pre.CheckResumeCount,
		Agent: dc.Agent, Model: dc.Model, Source: pre.CheckPromptSource}
	// The checker is a job of its own: hand back the concurrency slots this job still
	// holds, or a project limited to one running job would never start it.
	entry.mu.Lock()
	release := entry.releaseGates
	entry.mu.Unlock()
	if release != nil {
		release()
	}
	s.recordEvent(pre.ID, EventDeliveryCheckStarted, detail)

	stored, err := s.runCheckAgent(pre, dc, requirements)
	if err != nil {
		d.err = truncateUTF8(err.Error(), checkErrorMaxBytes)
		detail.Error = d.err
		s.recordEvent(pre.ID, EventDeliveryCheckError, detail)
		entry.mu.Lock()
		entry.result.CheckError = d.err
		entry.mu.Unlock()
		slog.Warn("delivery check failed to run", "job_id", pre.ID, "err", err)
		return d
	}
	stored.Round = round
	detail.Verdict, detail.SessionID = stored.Verdict, stored.SessionID
	failed := 0
	for _, it := range stored.Items {
		if !it.Passed {
			failed++
		}
	}
	detail.FailedItems = failed
	if stored.Verdict == "pass" {
		stored.Action = checkActionPass
		s.recordEvent(pre.ID, EventDeliveryCheckPassed, detail)
	} else {
		switch {
		case pre.CheckResumeCount < d.max && s.checkResumable(pre):
			stored.Action = checkActionResume
			d.action = checkActionResume
			d.reason = "delivery check failed (round " + strconv.Itoa(round) + "): " + truncateUTF8(stored.Summary, checkCommentSummary)
		default:
			stored.Action = checkActionReview
			d.action = checkActionReview
		}
		if stored.Action == checkActionResume {
			detail.ResumeCount = pre.CheckResumeCount + 1
		}
		s.recordEvent(pre.ID, EventDeliveryCheckFailed, detail)
	}
	d.stored = stored
	raw, merr := json.Marshal(stored)
	entry.mu.Lock()
	if merr == nil {
		entry.result.CheckJSON = string(raw)
	}
	entry.result.CheckError = ""
	if d.action == checkActionResume {
		entry.result.CheckResumeCount = pre.CheckResumeCount + 1
	}
	entry.mu.Unlock()
	return d
}

// runCheckAgent prepares the evidence, asks the checker and parses its answer.
func (s *Service) runCheckAgent(pre JobResult, dc config.DeliveryCheckConfig, requirements string) (StoredCheck, error) {
	if strings.TrimSpace(dc.Agent) == "" {
		return StoredCheck{}, errors.New("server.delivery_check.agent is not configured")
	}
	if !s.allowCheckToday(dc, pre.ProjectKey) {
		return StoredCheck{}, fmt.Errorf("server.delivery_check.daily_limit (%d) reached", dc.EffectiveDailyLimit())
	}
	in, truncated := s.buildCheckInput(pre, requirements, dc)
	if err := in.Validate(); err != nil {
		return StoredCheck{}, err
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return StoredCheck{}, fmt.Errorf("marshal check input: %w", err)
	}
	prompt, err := prompts.Render(jobPromptLanguage(s.config(), pre), prompts.NameDeliveryCheckRequest,
		prompts.DeliveryCheckRequestData{Evidence: string(raw)})
	if err != nil {
		return StoredCheck{}, err
	}
	timeout := time.Duration(dc.EffectiveTimeoutSec()) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout+30*time.Second)
	defer cancel()
	go func() {
		select {
		case <-s.bg.closingCh():
			cancel()
		case <-ctx.Done():
		}
	}()
	checker := s.deliveryChecker
	injected := checker != nil
	if checker == nil {
		checker = serviceChecker{s: s}
	}
	args, err := s.checkAgentArgs(dc)
	if err != nil {
		return StoredCheck{}, err
	}
	started := s.nowFn()
	out, err := checker.RunCheck(ctx, DeliveryCheckRequest{
		SourceJobID: pre.ID, ProjectKey: pre.ProjectKey, OriginCaller: pre.OriginCaller, Prompt: prompt,
		Agent: dc.Agent, Model: dc.Model, Args: args, Timeout: timeout, DailyLimit: dc.EffectiveDailyLimit(),
	})
	if injected {
		s.recordInjectedCheckRun(pre.ProjectKey, out.JobID, started, err)
	}
	if err != nil {
		return StoredCheck{}, err
	}
	result, err := ParseCheckResult([]byte(strings.TrimSpace(string(out.Output))))
	if err != nil {
		return StoredCheck{}, err
	}
	return StoredCheck{
		CheckResult: result, CheckedAt: s.nowFn().Unix(), Agent: dc.Agent, Model: dc.Model,
		SessionID: out.SessionID, CheckJobID: out.JobID, Source: pre.CheckPromptSource, InputTruncated: truncated,
	}, nil
}

// checkAgentArgs is the operator's server.delivery_check.args followed by the tool-less
// argv the server forces for the agent type (the rule lives in oneshot.NoToolsArgs). An
// agent type without a known no-tools argv is refused, so the checker never runs with
// tools by omission.
func (s *Service) checkAgentArgs(dc config.DeliveryCheckConfig) ([]string, error) {
	return oneshot.NoToolsArgs(s.agents, dc.Agent, dc.Args)
}

// serviceChecker is the default DeliveryChecker: a oneshot.Invoker (kind job_check: the
// persisted daily limit, the run log and the hard-stop audit) over the job service.
type serviceChecker struct{ s *Service }

func (c serviceChecker) RunCheck(ctx context.Context, r DeliveryCheckRequest) (DeliveryCheckOutput, error) {
	inv := oneshot.NewInvoker(c.s.checkRunner(), oneShotJobs{s: c.s}, c.s.agents)
	res, err := inv.Run(ctx, oneshot.Request{
		Kind: oneshot.KindJobCheck, Project: r.ProjectKey, Agent: r.Agent, Model: r.Model, Prompt: r.Prompt, Args: r.Args,
		Title: "delivery check of " + r.SourceJobID, SourceJobID: r.SourceJobID, Origin: r.OriginCaller,
		Limits: bgagent.Limits{DailyRuns: r.DailyLimit},
	})
	out := DeliveryCheckOutput{Output: res.Output, JobID: res.JobID, SessionID: res.SessionID}
	var stop *bgagent.HardStopError
	if errors.As(err, &stop) && stop.Reason == bgagent.StopDailyLimit {
		return out, fmt.Errorf("server.delivery_check.daily_limit (%d) reached", r.DailyLimit)
	}
	return out, err
}

// oneShotJobs implements oneshot.JobRunner over the job service: a one-shot, read-only
// job of the configured cli-agent on the server's own runner, hidden from ordinary lists.
type oneShotJobs struct{ s *Service }

func (c oneShotJobs) RunReadOnly(ctx context.Context, r oneshot.JobSpec) (oneshot.JobOutput, error) {
	res, err := c.s.Submit(JobRequest{
		ProjectKey: r.Project, Agent: r.Agent, Runner: config.BuiltinLocalRunner, Prompt: r.Prompt,
		Model: r.Model, AgentArgs: r.Args, ReadOnly: true, NoRules: true,
		Title: r.Title, Tags: []string{DeliveryCheckJobTag},
		TimeoutSec: int(r.Timeout / time.Second), EnvDenyExtra: []string{"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT"},
		OriginCaller: r.Origin, InternalExec: InternalExecDeliveryCheck,
	})
	if err != nil {
		return oneshot.JobOutput{}, err
	}
	out := oneshot.JobOutput{JobID: res.ID}
	type waited struct {
		final JobResult
		ok    bool
	}
	ch := make(chan waited, 1)
	go func() {
		final, ok := c.s.WaitFor(res.ID, r.Timeout+30*time.Second)
		ch <- waited{final, ok}
	}()
	select {
	case <-ctx.Done():
		_ = c.s.Cancel(res.ID)
		return out, ctx.Err()
	case w := <-ch:
		if !w.ok || !IsTerminal(w.final.Status) {
			_ = c.s.Cancel(res.ID)
			return out, errors.New("delivery-check job timed out")
		}
		if w.final.Status != StatusDone {
			msg := w.final.Error
			if msg == "" {
				msg = w.final.Status
			}
			return out, fmt.Errorf("delivery-check job did not succeed (%s)", msg)
		}
		stdout, _ := c.s.TailLog(res.ID, store.StreamStdout, CheckResultMaxBytes+1)
		out.Output = stdout
		out.SessionID = w.final.SessionID
		out.Usage = w.final.Usage
		return out, nil
	}
}

// resumeAfterCheck submits the continuation of a job whose delivery check failed. The
// source row records it (AutoResumedBy) like the other automatic continuations.
func (s *Service) resumeAfterCheck(snap JobResult, d checkDecision) error {
	items := make([]prompts.DeliveryCheckResumeItem, 0, len(d.stored.Items))
	for _, it := range d.stored.Items {
		if it.Passed {
			continue
		}
		src := it.Source
		if src == "" {
			src = "check"
		}
		items = append(items, prompts.DeliveryCheckResumeItem{Source: src, Requirement: it.Requirement,
			Evidence: truncateUTF8(it.Evidence, checkCommentSummary), Action: truncateUTF8(it.Action, checkCommentSummary)})
	}
	prompt := prompts.MustRender(jobPromptLanguage(s.config(), snap), prompts.NameDeliveryCheckResume,
		prompts.DeliveryCheckResumeData{Round: d.stored.Round, Items: items})
	res, err := s.resumeJob(snap.ID, prompt, snap.Runner, snap.CallerID, snap.AutoResumeAttempt, nil,
		ResumeOptions{Internal: true, checkRound: snap.CheckResumeCount})
	if err != nil {
		if errors.Is(err, ErrCeilingDenied) {
			s.recordCeilingDenied(snap.ID, "delivery-check-resume", err)
		}
		slog.Warn("delivery check resume not submitted", "job_id", snap.ID, "err", err)
		return err
	}
	snap.AutoResumedBy = res.ID
	_ = s.persist(snap)
	// A child receipt watching this job follows the work into its continuation.
	s.retargetChildWakes(snap.ID, res.ID)
	s.recordEvent(snap.ID, EventDeliveryCheckAutoResumed, DeliveryCheckEventDetail{
		Round: d.stored.Round, ResumeCount: snap.CheckResumeCount, MaxResumes: d.max, ResumedJobID: res.ID,
		Agent: d.stored.Agent, Model: d.stored.Model, SessionID: d.stored.SessionID, Source: d.stored.Source,
		Verdict: d.stored.Verdict, FailedItems: countFailedItems(d.stored.Items)})
	return nil
}

func countFailedItems(items []CheckItem) int {
	n := 0
	for _, it := range items {
		if !it.Passed {
			n++
		}
	}
	return n
}

// commentDeliveryCheck leaves one short conclusion on the job. resumedID is the
// continuation of a failed check ("" = none was started).
func (s *Service) commentDeliveryCheck(snap JobResult, d checkDecision, resumedID string) {
	// The comment is stored on the job: it speaks the project's language (design S3a rule 3).
	s.insertSystemComment("job", snap.ID, checkCommentBody(s.notifyLang(snap.ProjectKey), snap, d, resumedID))
}

// checkCommentBody is the text of the conclusion comment, in lang.
func checkCommentBody(lang i18n.Lang, snap JobResult, d checkDecision, resumedID string) string {
	t := func(key string, args ...any) string { return i18n.T(lang, "job.check_comment."+key, args...) }
	summary := truncateUTF8(d.stored.Summary, checkCommentSummary)
	failed := countFailedItems(d.stored.Items)
	var body string
	switch {
	case d.err != "":
		body = t("unfinished", "error", d.err)
	case d.stored.Verdict == "pass":
		body = t("pass", "round", d.stored.Round, "summary", summary)
	case resumedID != "":
		body = t("resumed", "round", d.stored.Round, "failed", failed, "job", resumedID,
			"n", snap.CheckResumeCount, "max", d.max, "summary", summary)
	case d.action == checkActionResume:
		body = t("resume_failed", "round", d.stored.Round, "failed", failed,
			"error", truncateUTF8(d.resumeErr, checkCommentSummary), "summary", summary)
	default:
		body = t("failed", "round", d.stored.Round, "failed", failed, "summary", summary)
	}
	if summary == "" && d.err == "" {
		// A language that puts a space before the summary must not leave it dangling.
		body = strings.TrimRight(body, " ")
	}
	return body
}

func withCheckAction(c StoredCheck, action string) StoredCheck {
	c.Action = action
	return c
}
