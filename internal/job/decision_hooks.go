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
	if p.TimeoutSec != nil {
		cur, _ := normalizeTimeout(req.TimeoutSec, req.Interactive, isCLIAgent(cfg, req.Agent), cfg.EffectiveMaxTimeoutSec(req.ProjectKey))
		if *p.TimeoutSec <= 0 || (cur > 0 && int64(*p.TimeoutSec) > int64(cur.Seconds())) {
			return fmt.Errorf("patch: timeout_sec can only be lowered (now %ds)", int64(cur.Seconds()))
		}
	}
	if b := p.Budget; b != nil {
		cur := cfg.EffectiveBudget(req.ProjectKey, gateAgentOf(req), req.Budget)
		if cur == nil {
			cur = &config.Budget{}
		}
		if b.MaxTokens < 0 || b.MaxCostUSD < 0 || b.MaxTurns < 0 ||
			(b.MaxTokens > 0 && cur.MaxTokens > 0 && b.MaxTokens > cur.MaxTokens) ||
			(b.MaxCostUSD > 0 && cur.MaxCostUSD > 0 && b.MaxCostUSD > cur.MaxCostUSD) ||
			(b.MaxTurns > 0 && cur.MaxTurns > 0 && b.MaxTurns > cur.MaxTurns) {
			return errors.New("patch: budget can only be lowered")
		}
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
