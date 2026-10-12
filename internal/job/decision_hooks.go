package job

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/delivery"
	"github.com/inhere/gofer/internal/util"
)

// Decision hooks (AUTO-04b, docs/design/2026-10-12-decision-hooks-design.md): the
// server runs the configured command synchronously at a decision point and acts on
// its JSON answer. An answer may only refuse the action or make it more
// conservative; a hook that fails (or asks for anything else) is handled by its
// on_error setting, deny by default.

// ErrDecisionDenied is a decision hook's refusal. HTTP: 403.
var ErrDecisionDenied = errors.New("denied by decision hook")

// EventJobDecisionHook records one hook decision on the job it shaped:
// {point, hook, decision, reason?, patch?, error?}.
const EventJobDecisionHook = "job.decision_hook"

// Environment variables gofer sets for a decision hook process.
const (
	EnvDecisionPoint = "GOFER_DECISION_POINT"
)

const (
	decisionAllow = "allow"
	decisionDeny  = "deny"
	// decisionInputVersion is the stdin protocol version.
	decisionInputVersion = 1
	// decisionStdoutMax caps the answer a hook may print.
	decisionStdoutMax = 64 << 10
	// decisionPromptMax caps the prompt text a hook is shown.
	decisionPromptMax = 64 << 10
)

// decisionAnswer is a hook's stdout.
type decisionAnswer struct {
	Decision string          `json:"decision"`
	Reason   string          `json:"reason,omitempty"`
	Patch    json.RawMessage `json:"patch,omitempty"`
}

// decisionRecord is one hook's decision as recorded on the job (job.decision_hook).
type decisionRecord struct {
	Point    string         `json:"point"`
	Hook     string         `json:"hook"`
	Decision string         `json:"decision"`
	Reason   string         `json:"reason,omitempty"`
	Patch    map[string]any `json:"patch,omitempty"`
	// Error is set when the hook failed and on_error decided instead.
	Error string `json:"error,omitempty"`
}

func (r decisionRecord) detail() map[string]any {
	d := map[string]any{"point": r.Point, "hook": r.Hook, "decision": r.Decision}
	if r.Reason != "" {
		d["reason"] = r.Reason
	}
	if len(r.Patch) > 0 {
		d["patch"] = r.Patch
	}
	if r.Error != "" {
		d["error"] = r.Error
	}
	return d
}

// limitWriter keeps at most max bytes and remembers that more were written.
type limitWriter struct {
	buf  bytes.Buffer
	max  int
	over bool
}

func (w *limitWriter) Write(p []byte) (int, error) {
	if room := w.max - w.buf.Len(); room < len(p) {
		w.over = true
		if room > 0 {
			w.buf.Write(p[:room])
		}
		return len(p), nil
	}
	return w.buf.Write(p)
}

// runDecisionHook runs one hook with input on stdin and parses its answer. Any
// failure — the process, the exit code, the JSON, an unknown decision — is an error;
// the caller applies on_error.
func runDecisionHook(ctx context.Context, cfg *config.Config, hook config.DecisionHookConfig, point string, input any) (decisionAnswer, error) {
	stdin, err := json.Marshal(input)
	if err != nil {
		return decisionAnswer{}, fmt.Errorf("marshal input: %w", err)
	}
	env := util.EnvironWithout(effectiveJobEnvDeny(cfg), nil, util.MergeEnv(map[string]string{
		EnvDecisionPoint: point,
		EnvHookName:      hook.Name,
	}, hook.Env))
	out := &limitWriter{max: decisionStdoutMax}
	run := runHookCommand(ctx, hook.Name, hook.Command, hook.EffectiveTimeout(), hook.EffectiveDir(), env, stdin, out)
	if run.err != nil {
		return decisionAnswer{}, errors.New(redactHookText(hookErrorText(run), hook.Env))
	}
	if out.over {
		return decisionAnswer{}, fmt.Errorf("answer longer than %d bytes", decisionStdoutMax)
	}
	var ans decisionAnswer
	dec := json.NewDecoder(bytes.NewReader(out.buf.Bytes()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ans); err != nil {
		return decisionAnswer{}, fmt.Errorf("answer is not a decision JSON object: %v", err)
	}
	switch ans.Decision {
	case decisionAllow, decisionDeny:
	default:
		return decisionAnswer{}, fmt.Errorf("unknown decision %q (want allow or deny)", ans.Decision)
	}
	if ans.Decision == decisionDeny && len(ans.Patch) > 0 && string(ans.Patch) != "null" {
		return decisionAnswer{}, errors.New("a deny cannot carry a patch")
	}
	return ans, nil
}

// decisionFailure turns a failed run into the on_error outcome, logging it.
func decisionFailure(hook config.DecisionHookConfig, point string, err error) decisionRecord {
	rec := decisionRecord{Point: point, Hook: hook.Name, Decision: hook.EffectiveOnError(), Error: err.Error()}
	if rec.Decision == config.DecisionOnErrorDeny {
		rec.Reason = "the decision hook failed: " + err.Error()
	}
	slog.Warn("decision_hook.failed", "event", "decision_hook.failed", "name", hook.Name, "point", point,
		"on_error", rec.Decision, "err", err.Error())
	return rec
}

// denyError is the error a refusal surfaces to the caller.
func denyError(rec decisionRecord) error {
	reason := strings.TrimSpace(rec.Reason)
	if reason == "" {
		reason = "no reason given"
	}
	return fmt.Errorf("%w %s: %s", ErrDecisionDenied, rec.Hook, reason)
}

// --- job.submit ---------------------------------------------------------------

// submitPatch is what a job.submit hook may change. Every field moves the job in the
// conservative direction; anything else in the patch is refused (unknown fields fail
// the decode, a loosening value fails check).
type submitPatch struct {
	Review           *bool          `json:"review,omitempty"`
	Hold             *bool          `json:"hold,omitempty"`
	HoldReason       string         `json:"hold_reason,omitempty"`
	ReadOnly         *bool          `json:"read_only,omitempty"`
	TimeoutSec       *int           `json:"timeout_sec,omitempty"`
	Budget           *config.Budget `json:"budget,omitempty"`
	TagsAdd          []string       `json:"tags_add,omitempty"`
	Title            *string        `json:"title,omitempty"`
	AcceptanceAppend string         `json:"acceptance_append,omitempty"`
}

func decodeSubmitPatch(raw json.RawMessage) (submitPatch, error) {
	var p submitPatch
	if len(raw) == 0 || string(raw) == "null" {
		return p, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return submitPatch{}, fmt.Errorf("patch: %v (allowed: review, hold, hold_reason, read_only, timeout_sec, budget, tags_add, title, acceptance_append)", err)
	}
	return p, nil
}

// check refuses a patch that would loosen req or cannot apply to it.
func (p submitPatch) check(cfg *config.Config, req JobRequest) error {
	if err := p.shapeCheck(); err != nil {
		return err
	}
	if p.TimeoutSec != nil {
		cur, _ := normalizeTimeout(req.TimeoutSec, req.Interactive, isCLIAgent(cfg, req.Agent), cfg.EffectiveMaxTimeoutSec(req.ProjectKey))
		if cur > 0 && int64(*p.TimeoutSec) > int64(cur.Seconds()) {
			return fmt.Errorf("patch: timeout_sec can only be lowered (now %ds)", int64(cur.Seconds()))
		}
	}
	if b := p.Budget; b != nil {
		cur := cfg.EffectiveBudget(req.ProjectKey, gateAgentOf(req), req.Budget)
		if cur == nil {
			cur = &config.Budget{}
		}
		if (b.MaxTokens > 0 && cur.MaxTokens > 0 && b.MaxTokens > cur.MaxTokens) ||
			(b.MaxCostUSD > 0 && cur.MaxCostUSD > 0 && b.MaxCostUSD > cur.MaxCostUSD) ||
			(b.MaxTurns > 0 && cur.MaxTurns > 0 && b.MaxTurns > cur.MaxTurns) {
			return errors.New("patch: budget can only be lowered")
		}
	}
	return nil
}

// shapeCheck is the part of check that needs no request: values that can only
// loosen whatever they are applied to.
func (p submitPatch) shapeCheck() error {
	if p.Review != nil && !*p.Review {
		return errors.New("patch: review can only be turned on")
	}
	if p.ReadOnly != nil && !*p.ReadOnly {
		return errors.New("patch: read_only can only be turned on")
	}
	if p.Hold != nil && !*p.Hold {
		return errors.New("patch: hold can only be turned on")
	}
	if p.HoldReason != "" && (p.Hold == nil || !*p.Hold) {
		return errors.New("patch: hold_reason needs hold: true")
	}
	if p.TimeoutSec != nil && *p.TimeoutSec <= 0 {
		return errors.New("patch: timeout_sec must be positive")
	}
	if b := p.Budget; b != nil && (b.MaxTokens < 0 || b.MaxCostUSD < 0 || b.MaxTurns < 0) {
		return errors.New("patch: budget values must not be negative")
	}
	for _, t := range p.TagsAdd {
		if strings.TrimSpace(t) == "" {
			return errors.New("patch: tags_add has an empty tag")
		}
	}
	return nil
}

// apply writes the patch into req.
func (p submitPatch) apply(req *JobRequest) {
	if p.Review != nil {
		req.Review = true
	}
	if p.ReadOnly != nil {
		req.ReadOnly = true
	}
	if p.Hold != nil {
		req.Hold = true
		if p.HoldReason != "" {
			req.HoldReason = p.HoldReason
		}
	}
	if p.TimeoutSec != nil {
		req.TimeoutSec = *p.TimeoutSec
	}
	if b := p.Budget; b != nil {
		req.Budget = b.Over(req.Budget).Normalize()
	}
	for _, t := range p.TagsAdd {
		if t = strings.TrimSpace(t); !slices.Contains(req.Tags, t) {
			req.Tags = append(req.Tags, t)
		}
	}
	if p.Title != nil {
		req.Title = strings.TrimSpace(*p.Title)
	}
	if a := strings.TrimSpace(p.AcceptanceAppend); a != "" {
		if strings.TrimSpace(req.Acceptance) == "" {
			req.Acceptance = a
		} else {
			req.Acceptance = strings.TrimRight(req.Acceptance, "\n") + "\n" + a
		}
	}
}

// asMap renders the applied patch for the job event.
func (p submitPatch) asMap() map[string]any {
	b, _ := json.Marshal(p)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

// submitHookInput is the job.submit stdin.
type submitHookInput struct {
	Version int             `json:"version"`
	Point   string          `json:"point"`
	Hook    string          `json:"hook"`
	Project string          `json:"project"`
	Caller  decisionCaller  `json:"caller"`
	Job     submitHookJobIn `json:"job"`
}

type decisionCaller struct {
	ID      string `json:"id"`
	Channel string `json:"channel,omitempty"`
	Client  string `json:"client,omitempty"`
}

type submitHookJobIn struct {
	Kind       string         `json:"kind"`
	Agent      string         `json:"agent"`
	Runner     string         `json:"runner"`
	Cmd        []string       `json:"cmd,omitempty"`
	Prompt     string         `json:"prompt,omitempty"`
	Cwd        string         `json:"cwd"`
	Title      string         `json:"title,omitempty"`
	Tags       []string       `json:"tags"`
	Review     bool           `json:"review"`
	Hold       bool           `json:"hold"`
	Worktree   bool           `json:"worktree"`
	ReadOnly   bool           `json:"read_only"`
	Deliver    string         `json:"deliver,omitempty"`
	TimeoutSec int            `json:"timeout_sec"`
	Budget     *config.Budget `json:"budget,omitempty"`
	Acceptance string         `json:"acceptance,omitempty"`
	PlanID     string         `json:"plan_id,omitempty"`
	ResumedOf  string         `json:"resumed_from,omitempty"`
}

// submitKind classifies a submit for the hook (job.kind).
func submitKind(req JobRequest) string {
	switch {
	case isInternalSubmit(req):
		return "internal"
	case req.ResumedFrom != "":
		return "resume"
	case req.FellBackFrom != "":
		return "fallback"
	case req.WorkflowID != "":
		return "workflow"
	case req.TodoID != "":
		return "plan"
	case req.Attempt > 1:
		return "retry"
	}
	return "submit"
}

// isInternalSubmit reports whether the job is one of gofer's own.
func isInternalSubmit(req JobRequest) bool {
	return req.Steward || req.LeaderOfPlan != "" || req.Messenger != nil || hiddenJobTag(req.Tags)
}

func submitHookJob(cfg *config.Config, req JobRequest) submitHookJobIn {
	cur, _ := normalizeTimeout(req.TimeoutSec, req.Interactive, isCLIAgent(cfg, req.Agent), cfg.EffectiveMaxTimeoutSec(req.ProjectKey))
	prompt := req.Prompt
	if len(prompt) > decisionPromptMax {
		prompt = strings.ToValidUTF8(prompt[:decisionPromptMax], "")
	}
	return submitHookJobIn{
		Kind: submitKind(req), Agent: req.Agent, Runner: req.Runner, Cmd: req.Cmd, Prompt: prompt,
		Cwd: req.Cwd, Title: req.Title, Tags: nonNilStrings(req.Tags), Review: reviewRequested(cfg, &req),
		Hold: req.Hold, Worktree: worktreeRequested(cfg, &req), ReadOnly: req.ReadOnly, Deliver: req.Deliver,
		TimeoutSec: int(cur.Seconds()), Budget: cfg.EffectiveBudget(req.ProjectKey, gateAgentOf(req), req.Budget),
		Acceptance: req.Acceptance, PlanID: req.PlanID, ResumedOf: req.ResumedFrom,
	}
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// decideSubmit asks the job.submit hooks about req, in config order, and applies
// their patches to req. A refusal (an answer, or a failure under on_error: deny) is
// returned as ErrDecisionDenied. The applied patch (the union, in order) is returned
// so the caller can carry it onto the request it keeps for a hold.
func (s *Service) decideSubmit(cfg *config.Config, req *JobRequest) ([]submitPatch, []decisionRecord, error) {
	hooks := cfg.DecisionHooksFor(config.DecisionPointSubmit, req.ProjectKey, isInternalSubmit(*req))
	if len(hooks) == 0 {
		return nil, nil, nil
	}
	var patches []submitPatch
	var recs []decisionRecord
	for _, hook := range hooks {
		in := submitHookInput{
			Version: decisionInputVersion, Point: config.DecisionPointSubmit, Hook: hook.Name, Project: req.ProjectKey,
			Caller: decisionCaller{ID: req.CallerID, Channel: req.Channel, Client: req.Client},
			Job:    submitHookJob(cfg, *req),
		}
		ans, err := runDecisionHook(context.Background(), cfg, hook, config.DecisionPointSubmit, in)
		var p submitPatch
		if err == nil && ans.Decision == decisionAllow {
			if p, err = decodeSubmitPatch(ans.Patch); err == nil {
				err = p.check(cfg, *req)
			}
			if err == nil && p.Hold != nil {
				if herr := checkHoldCombination(*req); herr != nil {
					err = fmt.Errorf("patch: hold cannot apply to this job (%v)", herr)
				}
			}
		}
		var rec decisionRecord
		if err != nil {
			rec = decisionFailure(hook, config.DecisionPointSubmit, err)
			p = submitPatch{}
		} else {
			rec = decisionRecord{Point: config.DecisionPointSubmit, Hook: hook.Name, Decision: ans.Decision, Reason: ans.Reason}
		}
		if rec.Decision == decisionDeny {
			slog.Info("decision_hook.denied", "event", "decision_hook.denied", "name", hook.Name, "point", config.DecisionPointSubmit,
				"project", req.ProjectKey, "agent", req.Agent, "caller_id", req.CallerID, "reason", rec.Reason)
			return nil, nil, denyError(rec)
		}
		if m := p.asMap(); len(m) > 0 {
			p.apply(req)
			rec.Patch = m
			patches = append(patches, p)
		}
		recs = append(recs, rec)
	}
	return patches, recs, nil
}

// patchReceived applies the hooks' patches to the request as received (the one a
// held job keeps and re-submits on approval).
func patchReceived(received []byte, patches []submitPatch) ([]byte, error) {
	var r JobRequest
	if err := json.Unmarshal(received, &r); err != nil {
		return nil, fmt.Errorf("decode held request: %w", err)
	}
	for _, p := range patches {
		p.apply(&r)
	}
	return json.Marshal(r)
}

// recordDecisions records the job.submit decisions on the job they shaped.
func (s *Service) recordDecisions(jobID string, recs []decisionRecord) {
	for _, r := range recs {
		s.recordEvent(jobID, EventJobDecisionHook, r.detail())
	}
}

// --- job.deliver --------------------------------------------------------------

// deliverPatch is what a job.deliver hook may change: what the request says, and
// that it is opened as a draft. Where it goes (branch, remote, mode) is not its call.
type deliverPatch struct {
	Title     *string  `json:"title,omitempty"`
	Body      *string  `json:"body,omitempty"`
	Draft     *bool    `json:"draft,omitempty"`
	LabelsAdd []string `json:"labels_add,omitempty"`
}

func decodeDeliverPatch(raw json.RawMessage) (deliverPatch, error) {
	var p deliverPatch
	if len(raw) == 0 || string(raw) == "null" {
		return p, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return deliverPatch{}, fmt.Errorf("patch: %v (allowed: title, body, draft, labels_add)", err)
	}
	if p.Draft != nil && !*p.Draft {
		return deliverPatch{}, errors.New("patch: draft can only be turned on")
	}
	if p.Title != nil && strings.TrimSpace(*p.Title) == "" {
		return deliverPatch{}, errors.New("patch: title cannot be empty")
	}
	for _, l := range p.LabelsAdd {
		if strings.TrimSpace(l) == "" {
			return deliverPatch{}, errors.New("patch: labels_add has an empty label")
		}
	}
	return p, nil
}

func (p deliverPatch) apply(plan *delivery.Plan) {
	if p.Title != nil {
		plan.Title = strings.TrimSpace(*p.Title)
	}
	if p.Body != nil {
		plan.Body = *p.Body
	}
	if p.Draft != nil {
		plan.Draft = true
	}
	for _, l := range p.LabelsAdd {
		if l = strings.TrimSpace(l); !slices.Contains(plan.Labels, l) {
			plan.Labels = append(slices.Clone(plan.Labels), l)
		}
	}
}

func (p deliverPatch) asMap() map[string]any {
	b, _ := json.Marshal(p)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

// deliverHookInput is the job.deliver stdin.
type deliverHookInput struct {
	Version  int               `json:"version"`
	Point    string            `json:"point"`
	Hook     string            `json:"hook"`
	Project  string            `json:"project"`
	Delivery deliverHookPlanIn `json:"delivery"`
}

type deliverHookPlanIn struct {
	JobID        string   `json:"job_id"`
	Mode         string   `json:"mode"`
	Branch       string   `json:"branch"`
	RemoteBranch string   `json:"remote_branch"`
	Remote       string   `json:"remote"`
	Base         string   `json:"base"`
	Draft        bool     `json:"draft"`
	Labels       []string `json:"labels"`
	Title        string   `json:"title"`
	Body         string   `json:"body"`
	Commits      []Commit `json:"commits"`
	DiffStat     string   `json:"diff_stat,omitempty"`
}

// decideDeliver asks the job.deliver hooks about plan, in config order, and returns
// the plan with their patches applied. A refusal is returned as ErrDecisionDenied.
func (s *Service) decideDeliver(cfg *config.Config, snap JobResult, plan delivery.Plan) (delivery.Plan, []decisionRecord, error) {
	hooks := cfg.DecisionHooksFor(config.DecisionPointDeliver, snap.ProjectKey, hiddenJobTag(snap.Tags))
	var recs []decisionRecord
	for _, hook := range hooks {
		commits := snap.Commits
		if commits == nil {
			commits = []Commit{}
		}
		remote := plan.Remote
		if remote == "" {
			remote = "origin"
		}
		in := deliverHookInput{
			Version: decisionInputVersion, Point: config.DecisionPointDeliver, Hook: hook.Name, Project: snap.ProjectKey,
			Delivery: deliverHookPlanIn{
				JobID: snap.ID, Mode: plan.Mode, Branch: plan.Branch, RemoteBranch: plan.RemoteBranch, Remote: remote,
				Base: plan.Base, Draft: plan.Draft, Labels: nonNilStrings(plan.Labels), Title: plan.Title, Body: plan.Body,
				Commits: commits, DiffStat: snap.DiffSummary,
			},
		}
		ans, err := runDecisionHook(context.Background(), cfg, hook, config.DecisionPointDeliver, in)
		var p deliverPatch
		if err == nil && ans.Decision == decisionAllow {
			p, err = decodeDeliverPatch(ans.Patch)
		}
		var rec decisionRecord
		if err != nil {
			rec = decisionFailure(hook, config.DecisionPointDeliver, err)
			p = deliverPatch{}
		} else {
			rec = decisionRecord{Point: config.DecisionPointDeliver, Hook: hook.Name, Decision: ans.Decision, Reason: ans.Reason}
		}
		if m := p.asMap(); len(m) > 0 {
			p.apply(&plan)
			// The event names what changed, not the (possibly long) new text.
			for k := range m {
				if k == "body" || k == "title" {
					m[k] = "rewritten"
				}
			}
			rec.Patch = m
		}
		recs = append(recs, rec)
		if rec.Decision == decisionDeny {
			slog.Info("decision_hook.denied", "event", "decision_hook.denied", "name", hook.Name, "point", config.DecisionPointDeliver,
				"project", snap.ProjectKey, "job_id", snap.ID, "reason", rec.Reason)
			return plan, recs, denyError(rec)
		}
	}
	return plan, recs, nil
}

// --- trying a hook ------------------------------------------------------------

// DecisionTrial is one test run of a decision hook (`gofer tool hook-test`).
type DecisionTrial struct {
	Hook     string          `json:"hook"`
	Point    string          `json:"point"`
	Decision string          `json:"decision,omitempty"`
	Reason   string          `json:"reason,omitempty"`
	Patch    json.RawMessage `json:"patch,omitempty"`
	// Error is why the run counts as failed (the process, the answer or the patch).
	Error string `json:"error,omitempty"`
	// Outcome is what gofer would do: the decision, or on_error after a failure.
	Outcome string `json:"outcome"`
}

// TryDecisionHook runs the named hook once with input (a sample stdin) and reports
// how gofer would read its answer. It changes nothing; the patch is checked for
// fields and values the point accepts, not against a concrete job.
func TryDecisionHook(cfg *config.Config, name, point string, input []byte) (DecisionTrial, error) {
	var hook config.DecisionHookConfig
	found := false
	for _, h := range cfg.Server.DecisionHooks {
		if h.Name == name {
			hook, found = h, true
			break
		}
	}
	if !found {
		return DecisionTrial{}, fmt.Errorf("no decision hook named %q in server.decision_hooks", name)
	}
	if point == "" {
		point = hook.Points[0]
	}
	if !slices.Contains(hook.Points, point) {
		return DecisionTrial{}, fmt.Errorf("hook %q does not answer %s (points: %s)", name, point, strings.Join(hook.Points, ", "))
	}
	if !json.Valid(input) {
		return DecisionTrial{}, errors.New("the input is not JSON")
	}
	tr := DecisionTrial{Hook: name, Point: point}
	ans, err := runDecisionHook(context.Background(), cfg, hook, point, json.RawMessage(input))
	if err == nil {
		tr.Decision, tr.Reason, tr.Patch = ans.Decision, ans.Reason, ans.Patch
		if ans.Decision == decisionAllow {
			switch point {
			case config.DecisionPointSubmit:
				var p submitPatch
				if p, err = decodeSubmitPatch(ans.Patch); err == nil {
					err = p.shapeCheck()
				}
			case config.DecisionPointDeliver:
				_, err = decodeDeliverPatch(ans.Patch)
			}
		}
	}
	if err != nil {
		tr.Error = err.Error()
		tr.Outcome = hook.EffectiveOnError()
		return tr, nil
	}
	tr.Outcome = ans.Decision
	return tr, nil
}
