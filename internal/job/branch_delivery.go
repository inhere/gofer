package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/delivery"
	"github.com/inhere/gofer/internal/secret"
	"github.com/inhere/gofer/internal/store"
)

// Delivery of a worktree job's branch (GIT-02): merge / push / pull request, after
// the job is done — or, when it is reviewed, after it is accepted.

// DeliveryResult is a job's delivery outcome (jobs.delivery_json).
type DeliveryResult = delivery.Result

// Delivery events.
const (
	EventJobDelivered       = "job.delivered"
	EventJobDeliveryFailed  = "job.delivery_failed"
	EventJobDeliverySkipped = "job.delivery_skipped"
)

// deliveryTimeout bounds one delivery (git + the platform API).
const deliveryTimeout = 3 * time.Minute

// Delivery errors.
var (
	ErrDeliveryNotApplicable = errors.New("job cannot be delivered")
)

// DeliverOptions overrides a delivery (manual `gofer job deliver`).
type DeliverOptions struct {
	Mode   string // "" = the job's / project's mode
	Draft  *bool
	Branch string // remote branch name ("" = the template)
	DryRun bool
}

// DeliveryPreview contains only the operator-facing fields of a delivery plan.
type DeliveryPreview struct {
	Mode         string   `json:"mode"`
	Branch       string   `json:"branch"`
	RemoteBranch string   `json:"remote_branch"`
	Remote       string   `json:"remote"`
	Base         string   `json:"base"`
	Draft        bool     `json:"draft"`
	Labels       []string `json:"labels"`
	AllowPush    bool     `json:"allow_push"`
	Title        string   `json:"title"`
	Body         string   `json:"body"`
}

// EffectiveDeliveryMode is the mode a job delivers with: the job's own --deliver,
// else its project's delivery.mode, else none.
func EffectiveDeliveryMode(cfg *config.Config, req JobRequest) string {
	if req.Deliver != "" {
		return req.Deliver
	}
	if cfg != nil {
		if d := cfg.Projects[req.ProjectKey].Delivery; d != nil && d.Mode != "" {
			return d.Mode
		}
	}
	return delivery.ModeNone
}

// autoDeliver is the terminal hook that delivers a finished worktree job: only a
// job that ended done (accepted, when it was reviewed) with commits ahead.
func (s *Service) autoDeliver(snap JobResult) {
	if snap.Status != StatusDone || snap.WorktreePath == "" || snap.Delivery != nil {
		return
	}
	req := requestOfJob(snap)
	if EffectiveDeliveryMode(s.config(), req) == delivery.ModeNone {
		return
	}
	if snap.CommitsAhead <= 0 {
		s.recordEvent(snap.ID, EventJobDeliverySkipped, map[string]any{"reason": "no commits ahead of the base"})
		return
	}
	if _, err := s.Deliver(snap.ID, DeliverOptions{}); err != nil {
		slog.Warn("job: auto delivery", "job_id", snap.ID, "err", err)
	}
}

// Deliver hands the job's worktree branch over and records the outcome on the job.
// A job run on this machine is delivered here; a job on a worker is delivered on that
// worker (deliverRemote).
func (s *Service) Deliver(jobID string, opts DeliverOptions) (DeliveryResult, error) {
	if opts.DryRun {
		return DeliveryResult{}, fmt.Errorf("%w: use PreviewDelivery for a dry run", ErrInvalidRequest)
	}
	cfg := s.config()
	snap, plan, err := s.checkedDeliveryPlan(cfg, jobID, opts, false)
	if err != nil {
		return DeliveryResult{}, err
	}
	if snap.Runner != "" && IsRemoteRunner(cfg, snap.Runner) {
		return s.deliverRemote(snap, plan)
	}
	// One delivery per project at a time: two merges must not touch the main checkout
	// together.
	mu := s.deliveryLock(snap.ProjectKey)
	mu.Lock()
	defer mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), deliveryTimeout)
	defer cancel()
	res := delivery.Execute(ctx, plan, delivery.Env{})
	return res, s.recordDelivery(jobID, res)
}

// PreviewDelivery plans a delivery without executing or recording it.
func (s *Service) PreviewDelivery(jobID string, opts DeliverOptions) (DeliveryPreview, error) {
	cfg := s.config()
	// A job awaiting review can be previewed: seeing the request it would open is
	// part of deciding whether to accept it (gofer-iy68).
	snap, plan, err := s.checkedDeliveryPlan(cfg, jobID, opts, true)
	if err != nil {
		return DeliveryPreview{}, err
	}
	base := plan.Base
	if base == "" && !IsRemoteRunner(cfg, snap.Runner) {
		ctx, cancel := context.WithTimeout(context.Background(), deliveryTimeout)
		defer cancel()
		_, base, _ = delivery.BaseBranch(ctx, plan.Worktree)
	}
	remote := plan.Remote
	if remote == "" {
		remote = "origin"
	}
	return DeliveryPreview{
		Mode: plan.Mode, Branch: plan.Branch, RemoteBranch: plan.RemoteBranch,
		Remote: remote, Base: base, Draft: plan.Draft, Labels: append([]string{}, plan.Labels...),
		AllowPush: plan.AllowPush, Title: plan.Title, Body: plan.Body,
	}, nil
}

func (s *Service) checkedDeliveryPlan(cfg *config.Config, jobID string, opts DeliverOptions, preview bool) (JobResult, delivery.Plan, error) {
	snap, ok := s.Get(jobID)
	if !ok {
		return JobResult{}, delivery.Plan{}, fmt.Errorf("%w: %q", ErrUnknownJob, jobID)
	}
	if snap.WorktreePath == "" {
		return JobResult{}, delivery.Plan{}, fmt.Errorf("%w: job %s has no managed worktree (run it with --worktree)", ErrDeliveryNotApplicable, jobID)
	}
	if snap.Status != StatusDone && !(preview && snap.Status == StatusNeedsReview) {
		return JobResult{}, delivery.Plan{}, fmt.Errorf("%w: job %s is %s (a job is delivered once done / accepted)", ErrDeliveryNotApplicable, jobID, snap.Status)
	}
	plan, err := s.deliveryPlan(cfg, snap, opts)
	if err != nil {
		return JobResult{}, delivery.Plan{}, err
	}
	if plan.Mode == delivery.ModeNone {
		return JobResult{}, delivery.Plan{}, fmt.Errorf("%w: delivery mode is none (set projects.%s.delivery.mode or pass --mode)", ErrDeliveryNotApplicable, snap.ProjectKey)
	}
	return snap, plan, nil
}

// recordDelivery stores the outcome on the job and records the event.
func (s *Service) recordDelivery(jobID string, res DeliveryResult) error {
	snap, ok := s.Get(jobID)
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownJob, jobID)
	}
	r := res
	snap.Delivery = &r
	if e := s.entry(jobID); e != nil {
		e.mu.Lock()
		e.result.Delivery = &r
		e.mu.Unlock()
	}
	if err := s.persist(snap); err != nil {
		return fmt.Errorf("persist delivery of job %q: %w", jobID, err)
	}
	detail := map[string]any{"mode": res.Mode, "state": res.State}
	for k, v := range map[string]string{"branch": res.Branch, "base": res.Base, "pr_url": res.PRURL, "provider": res.Provider, "note": res.Note, "error": res.Error} {
		if v != "" {
			detail[k] = v
		}
	}
	switch res.State {
	case delivery.StatePending:
		// the outcome event comes when the remote delivery ends
	case delivery.StateFailed:
		s.recordEvent(jobID, EventJobDeliveryFailed, detail)
	default:
		s.recordEvent(jobID, EventJobDelivered, detail)
	}
	return nil
}

// deliveryPlan assembles the self-contained plan of a job's delivery.
func (s *Service) deliveryPlan(cfg *config.Config, snap JobResult, opts DeliverOptions) (delivery.Plan, error) {
	req := requestOfJob(snap)
	// A continuation (reject --resume) delivers the branch its chain started: the
	// task is the original ask, not the rework note it was resumed with.
	task := requestOfJob(s.resumeRoot(snap))
	var dc config.DeliveryConfig
	if cfg != nil {
		if d := cfg.Projects[snap.ProjectKey].Delivery; d != nil {
			dc = *d
		}
	}
	mode := opts.Mode
	if mode == "" {
		mode = EffectiveDeliveryMode(cfg, req)
	}
	if !delivery.ValidMode(mode) {
		return delivery.Plan{}, fmt.Errorf("%w: delivery mode %q is not none, merge, push or pr", ErrInvalidRequest, mode)
	}
	draft := dc.IsDraft()
	if req.DeliverDraft != nil {
		draft = *req.DeliverDraft
	}
	if opts.Draft != nil {
		draft = *opts.Draft
	}
	branch := firstNonEmptyString(opts.Branch, req.DeliverBranch)
	if branch == "" {
		branch = renderDeliveryBranch(dc.Branch, snap)
	}
	return delivery.Plan{
		Mode: mode, JobID: snap.ID, Worktree: snap.WorktreePath, Branch: snap.WorktreeBranch,
		RemoteBranch: branch, Remote: dc.Remote, Base: dc.Base,
		AllowPush: dc.AllowPush, PushBase: dc.PushBase, Squash: dc.Squash, Cleanup: dc.Cleanup,
		Draft: draft, Labels: dc.Labels, Provider: dc.Provider, TokenEnv: dc.TokenEnv, Proxy: dc.Proxy,
		Title: deliveryTitle(snap, task), Body: s.deliveryBody(cfg, snap, task),
	}, nil
}

func firstNonEmptyString(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

var slugUnsafe = regexp.MustCompile(`[^a-z0-9]+`)

// renderDeliveryBranch expands {{job_id}} {{short_id}} {{slug}} ("" = gofer/{{job_id}}).
func renderDeliveryBranch(tmpl string, snap JobResult) string {
	if strings.TrimSpace(tmpl) == "" {
		tmpl = "gofer/{{job_id}}"
	}
	short := snap.ID
	if i := strings.LastIndex(short, "-"); i >= 0 {
		short = short[i+1:]
	}
	slug := strings.Trim(slugUnsafe.ReplaceAllString(strings.ToLower(snap.Title), "-"), "-")
	if len(slug) > 40 {
		slug = strings.Trim(slug[:40], "-")
	}
	if slug == "" {
		slug = short
	}
	return strings.NewReplacer("{{job_id}}", snap.ID, "{{short_id}}", short, "{{slug}}", slug).Replace(tmpl)
}

// requestOfJob reads the job's original request (zero value when unreadable).
func requestOfJob(snap JobResult) JobRequest {
	var req JobRequest
	if snap.RequestJSON != "" {
		_ = json.Unmarshal([]byte(snap.RequestJSON), &req)
	}
	return req
}

// resumeRoot is the job a continuation chain started from (snap itself when it is
// not a continuation or the chain cannot be followed).
func (s *Service) resumeRoot(snap JobResult) JobResult {
	root := snap
	for hop := 0; hop < fallbackChainMaxHops && root.ResumedFrom != ""; hop++ {
		prev, ok := s.Get(root.ResumedFrom)
		if !ok {
			break
		}
		root = prev
	}
	return root
}

func deliveryTitle(snap JobResult, req JobRequest) string {
	if t := strings.TrimSpace(strings.TrimSuffix(snap.Title, " (resumed)")); t != "" {
		return t
	}
	line, _, _ := strings.Cut(strings.TrimSpace(req.Prompt), "\n")
	if len([]rune(line)) > 80 {
		line = string([]rune(line)[:80]) + "…"
	}
	if line == "" {
		line = "gofer job " + snap.ID
	}
	return line
}

// deliveryBody is the request description: what was asked, what was done and how it
// was checked. The prompt passes through the same redaction as the logs.
func (s *Service) deliveryBody(cfg *config.Config, snap JobResult, req JobRequest) string {
	var b strings.Builder
	prompt, _ := secret.RedactString(deliveryTask(req.Prompt))
	if prompt != "" {
		b.WriteString("## 任务\n\n")
		if lines := strings.Count(prompt, "\n"); lines > 12 || len(prompt) > 1500 {
			first, _, _ := strings.Cut(prompt, "\n")
			b.WriteString(first + "\n\n<details><summary>完整任务说明</summary>\n\n" + clipText(prompt, 8000) + "\n\n</details>\n\n")
		} else {
			b.WriteString(prompt + "\n\n")
		}
	}
	if a := strings.TrimSpace(snap.Acceptance); a != "" {
		b.WriteString("## 验收标准\n\n" + a + "\n\n")
	}
	if len(snap.Commits) > 0 {
		b.WriteString("## 提交\n\n")
		for i, c := range snap.Commits {
			if i >= 30 {
				fmt.Fprintf(&b, "- …另 %d 个\n", len(snap.Commits)-i)
				break
			}
			sha := c.SHA
			if len(sha) > 8 {
				sha = sha[:8]
			}
			fmt.Fprintf(&b, "- `%s` %s\n", sha, c.Subject)
		}
		b.WriteString("\n")
	}
	if d := strings.TrimSpace(snap.DiffSummary); d != "" {
		b.WriteString("## 改动统计\n\n```\n" + clipText(d, 3000) + "\n```\n\n")
	}
	if v := snap.Verify; v != nil {
		fmt.Fprintf(&b, "## 验证\n\n`%s` → **%s**", strings.Join(v.Command, " "), v.Status)
		if v.Reason != "" {
			b.WriteString("（" + v.Reason + "）")
		}
		b.WriteString("\n\n")
	}
	if u := snap.Usage; u != nil {
		if line := FormatUsage(u); line != "" {
			b.WriteString("## 用量\n\n" + line + "\n\n")
		}
	}
	if snap.ReviewedBy != "" {
		fmt.Fprintf(&b, "## 验收\n\n%s 已通过", snap.ReviewedBy)
		if n := strings.TrimSpace(snap.ReviewNote); n != "" {
			b.WriteString("：" + n)
		}
		b.WriteString("\n\n")
	}
	b.WriteString("---\n由 gofer 自动创建 · job `" + snap.ID + "`")
	// The web console is usually on a private address, and a pull request may be
	// public: the link is opt-in.
	if cfg != nil && strings.TrimSpace(cfg.Server.WebBaseURL) != "" && deliveryJobLink(cfg, snap.ProjectKey) {
		b.WriteString(" · " + strings.TrimRight(cfg.Server.WebBaseURL, "/") + "/jobs/" + snap.ID)
	}
	b.WriteString("\n")
	return b.String()
}

// deliveryTask is the task text a pull request shows: what the submitter wrote,
// without the sections gofer adds to the prompt — the rules frame (rules and
// memories may name machines, paths and addresses, and the request may be public)
// and the appended work-contract sections (the acceptance criteria get their own
// section in the body).
func deliveryTask(prompt string) string {
	p := stripRulesSection(strings.TrimSpace(prompt))
	for _, h := range []string{acceptanceSectionHeader, scopeSectionHeader} {
		if strings.HasPrefix(p, h+"\n") {
			return ""
		}
		if i := strings.Index(p, "\n\n"+h+"\n"); i >= 0 {
			p = p[:i]
		}
	}
	return strings.TrimSpace(p)
}

func deliveryJobLink(cfg *config.Config, project string) bool {
	d := cfg.Projects[project].Delivery
	return d != nil && d.JobLink
}

func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && (s[n]&0xC0) == 0x80 {
		n--
	}
	return s[:n] + "\n…"
}

// deliverRemote delivers a worker's job on that worker, where the worktree is: an
// internal exec job (tag delivery) runs `gofer tool deliver` there with the plan on
// stdin. The job's delivery reads pending until that internal job ends; its last
// stdout line is the outcome.
func (s *Service) deliverRemote(snap JobResult, plan delivery.Plan) (DeliveryResult, error) {
	raw, err := json.Marshal(plan)
	if err != nil {
		return DeliveryResult{}, err
	}
	dj, err := s.Submit(JobRequest{
		ProjectKey: snap.ProjectKey, Agent: agent.ExecAgentKey, Runner: snap.Runner,
		Cmd: []string{"gofer", "tool", "deliver"}, Cwd: ".", Stdin: string(raw),
		Title: "交付 " + snap.ID, Tags: []string{DeliveryJobTag}, TimeoutSec: int(deliveryTimeout / time.Second),
	})
	if err != nil {
		return DeliveryResult{}, fmt.Errorf("start the delivery on runner %s: %w", snap.Runner, err)
	}
	pending := DeliveryResult{Mode: plan.Mode, State: delivery.StatePending, Branch: plan.RemoteBranch,
		Note: "delivering on runner " + snap.Runner + " (job " + dj.ID + ")", At: s.nowFn().Unix()}
	if err := s.recordDelivery(snap.ID, pending); err != nil {
		return pending, err
	}
	s.goBG(func() {
		final, _ := s.Wait(dj.ID)
		_ = s.recordDelivery(snap.ID, s.remoteDeliveryOutcome(final, plan))
	})
	return pending, nil
}

// remoteDeliveryOutcome reads the delivery job's result line (a job that died before
// printing one is a failed delivery with its error).
func (s *Service) remoteDeliveryOutcome(dj JobResult, plan delivery.Plan) DeliveryResult {
	if dj.ResultDir != "" {
		if b, err := store.NewFileStore(filepath.Dir(dj.ResultDir)).ReadLogTail(dj.ID, store.StreamStdout, 64<<10); err == nil {
			lines := strings.Split(strings.TrimSpace(string(b)), "\n")
			var out DeliveryResult
			if len(lines) > 0 && json.Unmarshal([]byte(lines[len(lines)-1]), &out) == nil && out.State != "" {
				return out
			}
		}
	}
	msg := firstNonEmptyString(dj.Error, "the delivery job ended "+dj.Status+" without a result")
	return DeliveryResult{Mode: plan.Mode, State: delivery.StateFailed, Error: msg + " (job " + dj.ID + ")", At: s.nowFn().Unix()}
}

// deliveryLocks serialises the local deliveries of each project.
var deliveryLocks sync.Map // project key -> *sync.Mutex

func (s *Service) deliveryLock(project string) *sync.Mutex {
	m, _ := deliveryLocks.LoadOrStore(project, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// DeliveryModeOf is the mode a worktree job delivers with under the current config.
func (s *Service) DeliveryModeOf(snap JobResult) string {
	return EffectiveDeliveryMode(s.config(), requestOfJob(snap))
}
