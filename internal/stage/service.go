package stage

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
)

// ErrMoved is returned by an action whose flow was moved by someone else between reading and
// writing it (the step is no longer the one the caller saw).
var ErrMoved = errors.New("stage flow moved to another step meanwhile; read its status and try again")

// GitRunner runs a read-only git command in dir (see project.GitRO).
type GitRunner func(dir string, capBytes int, args ...string) (string, error)

// Deps is what the service needs from the process.
type Deps struct {
	Store *jobstore.Store
	// Config returns the current server configuration (projects, stage settings); it is
	// called per request so a reload applies.
	Config func() *config.Config
	// Git defaults to project.GitRO.
	Git GitRunner
	// Deliver hands the `remind` level's "continue" turn to a session; nil = the level
	// only records.
	Deliver DeliverFunc
	// Sync runs the reminder inline instead of in its own goroutine (tests).
	Sync bool
	// Now is the clock of the report's observation window (default time.Now).
	Now func() time.Time
}

// Service is the stage flow service.
type Service struct {
	store   *jobstore.Store
	cfg     func() *config.Config
	git     GitRunner
	deliver DeliverFunc
	sync    bool
	nowFn   func() time.Time
}

// New builds the service; Deps.Store is required.
func New(d Deps) *Service {
	s := &Service{store: d.Store, cfg: d.Config, git: d.Git, deliver: d.Deliver, sync: d.Sync, nowFn: d.Now}
	if s.git == nil {
		s.git = project.GitRO
	}
	return s
}

// View is a flow as the entry layers show it. Gate is the evaluation of the current step
// (nil when the flow is finished, or when the caller asked for no evaluation).
type View struct {
	WorkItemID    string   `json:"work_item_id"`
	ProjectKey    string   `json:"project_key,omitempty"`
	Template      string   `json:"template"`
	Steps         []string `json:"steps"`
	Step          string   `json:"step"`
	StepIndex     int      `json:"step_index"`
	StepEnteredAt int64    `json:"step_entered_at"`
	Status        string   `json:"status"`
	Level         string   `json:"level"`
	BlockedReason string   `json:"blocked_reason,omitempty"`
	DoingDoneSHA  string   `json:"doing_done_sha,omitempty"`
	ReviewMax     int      `json:"review_max"`
	Approved      bool     `json:"approved,omitempty"`
	Gate          *Gate    `json:"gate,omitempty"`
}

// Result is the answer of an action that may advance the flow. Advanced is false when the
// gate did not pass (Gate says what is missing) or another call advanced the flow first.
type Result struct {
	Advanced bool   `json:"advanced"`
	Gate     Gate   `json:"gate"`
	Stage    View   `json:"stage"`
	From     string `json:"from,omitempty"`
	// Moved is set when the flow had already left the step this call evaluated: a concurrent
	// `done` won, and nothing was written by this one.
	Moved bool `json:"moved,omitempty"`
}

// Enabled reports whether the stage flow is on for a project (projects.<key>.stage.enabled,
// default true). A flow already started is unaffected by it; callers (the start-a-flow
// prompts) use it to decide whether to remind.
func (s *Service) Enabled(projectKey string) bool {
	if c := s.config(); c != nil {
		return c.StageEnabledFor(projectKey)
	}
	return true
}

func (s *Service) config() *config.Config {
	if s.cfg == nil {
		return nil
	}
	return s.cfg()
}

func (s *Service) viewOf(ws jobstore.WorkStage, projectKey string) View {
	return View{
		WorkItemID: ws.WorkItemID, ProjectKey: projectKey, Template: ws.Template, Steps: ws.Steps,
		Step: ws.StepName(), StepIndex: ws.Step, StepEnteredAt: ws.StepEnteredAt, Status: ws.Status,
		Level: ws.Level, BlockedReason: ws.BlockedReason, DoingDoneSHA: ws.DoingDoneSHA, ReviewMax: ws.ReviewMax,
		Approved: ws.ApprovedStep == ws.Step,
	}
}

// load reads the flow and its work item.
func (s *Service) load(id string) (jobstore.WorkStage, jobstore.WorkItem, error) {
	id = strings.TrimSpace(id)
	item, ok, err := s.store.GetWorkItem(id)
	if err != nil {
		return jobstore.WorkStage{}, jobstore.WorkItem{}, err
	}
	if !ok {
		return jobstore.WorkStage{}, jobstore.WorkItem{}, jobstore.ErrWorkItemNotFound
	}
	ws, ok, err := s.store.GetWorkStage(id)
	if err != nil {
		return jobstore.WorkStage{}, jobstore.WorkItem{}, err
	}
	if !ok {
		return jobstore.WorkStage{}, jobstore.WorkItem{}, jobstore.ErrStageNotFound
	}
	return ws, item, nil
}

// evalFor evaluates the current step's gate of ws.
func (s *Service) evalFor(ws jobstore.WorkStage, item jobstore.WorkItem) (Gate, error) {
	links, err := s.store.ListWorkLinksRole(ws.WorkItemID, "")
	if err != nil {
		return Gate{}, err
	}
	in := evalInput{ws: ws, item: item, links: links, verifyTemplates: slices.Clone(config.DefaultStageVerifyTemplates)}
	if c := s.config(); c != nil {
		in.verifyTemplates = c.StageVerifyTemplatesFor(item.ProjectKey)
		if p, ok := c.Projects[item.ProjectKey]; ok && item.ProjectKey != "" {
			in.repo = c.ExecPath(p)
		}
	}
	g := s.evaluate(in)
	// A person's approval also waives a gate that cannot be judged here.
	if !g.OK && g.NeedsHuman && ws.ApprovedStep == ws.Step {
		g = Gate{Step: g.Step, OK: true, Missing: []string{}}
	}
	return g, nil
}

// Status returns the flow of a work item with the current step's gate evaluated.
func (s *Service) Status(id string) (View, error) {
	ws, item, err := s.load(id)
	if err != nil {
		return View{}, err
	}
	v := s.viewOf(ws, item.ProjectKey)
	if ws.Open() {
		g, err := s.evalFor(ws, item)
		if err != nil {
			return View{}, err
		}
		v.Gate = &g
	}
	return v, nil
}

// ForSession returns the flow of the work item the session is currently on (the newest
// change when it is on several); ok=false when it has none in progress. withGate evaluates
// the current step's gate too (it may run git, so a per-turn caller passes false).
func (s *Service) ForSession(sid string, withGate bool) (View, bool, error) {
	list, err := s.store.WorkStagesForSession(sid)
	if err != nil || len(list) == 0 {
		return View{}, false, err
	}
	if withGate {
		v, err := s.Status(list[0].WorkItemID)
		return v, err == nil, err
	}
	item, _, err := s.store.GetWorkItem(list[0].WorkItemID)
	if err != nil {
		return View{}, false, err
	}
	return s.viewOf(list[0], item.ProjectKey), true, nil
}

// ListOpen returns the flows in progress (project != "" keeps one project's), without gates.
func (s *Service) ListOpen(projectKey string) ([]View, error) {
	list, err := s.store.ListOpenWorkStages(projectKey)
	if err != nil {
		return nil, err
	}
	out := make([]View, 0, len(list))
	for _, ws := range list {
		item, ok, err := s.store.GetWorkItem(ws.WorkItemID)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		out = append(out, s.viewOf(ws, item.ProjectKey))
	}
	return out, nil
}

// StartInput names what a flow starts on: an issue (the item already linked to it, else a new
// item linked to it) and/or an explicit work item.
type StartInput struct {
	Issue      string
	WorkItemID string
	// Template is a built-in template name; empty = feature.
	Template string
	// ProjectKey and Title are used only when a new work item has to be created.
	ProjectKey string
	Title      string
	// SessionID, when set, becomes a current session of the work item.
	SessionID string
	By        string
}

// Start begins a flow. The work item is the explicit one, else the open item already linked
// to the issue, else a new item that gets the issue linked. A flow already in progress on it
// is jobstore.ErrStageActive.
func (s *Service) Start(in StartInput) (View, error) {
	name := strings.TrimSpace(in.Template)
	if name == "" {
		name = TemplateFeature
	}
	tpl, ok := TemplateByName(name)
	if !ok {
		return View{}, fmt.Errorf("%w: unknown stage template %q (built-in: %s)", jobstore.ErrWorkInvalid, name, strings.Join(TemplateNames(), ", "))
	}
	issue := strings.TrimSpace(in.Issue)
	by := strings.TrimSpace(in.By)
	var item jobstore.WorkItem
	switch id := strings.TrimSpace(in.WorkItemID); {
	case id != "":
		it, found, err := s.store.GetWorkItem(id)
		if err != nil {
			return View{}, err
		}
		if !found {
			return View{}, jobstore.ErrWorkItemNotFound
		}
		item = it
		if issue != "" {
			if _, err := s.store.AddWorkLink(item.ID, jobstore.WorkLinkIssue, issue, by); err != nil {
				return View{}, err
			}
		}
	case issue != "":
		linked, err := s.store.WorkItemsLinkedToIssue(issue)
		if err != nil {
			return View{}, err
		}
		if len(linked) > 0 {
			item = linked[0]
			break
		}
		title := strings.TrimSpace(in.Title)
		if title == "" {
			title = s.issueTitle(issue)
		}
		it, err := s.store.CreateWorkItem(jobstore.WorkItemInput{
			Title: title, ProjectKey: strings.TrimSpace(in.ProjectKey), Source: jobstore.WorkOriginHuman, By: by,
		})
		if err != nil {
			return View{}, err
		}
		item = it
		if _, err := s.store.AddWorkLink(item.ID, jobstore.WorkLinkIssue, issue, by); err != nil {
			return View{}, err
		}
	default:
		return View{}, fmt.Errorf("%w: start needs an issue id or a work item id", jobstore.ErrWorkInvalid)
	}
	if _, err := s.store.StartWorkStage(jobstore.WorkStage{
		WorkItemID: item.ID, Template: tpl.Name, Steps: tpl.Steps, ReviewMax: tpl.ReviewMax, PlanDoc: tpl.PlanDoc,
	}, by); err != nil {
		return View{}, err
	}
	if sid := strings.TrimSpace(in.SessionID); sid != "" {
		if err := s.store.AttachWorkSession(item.ID, sid, by); err != nil {
			return View{}, err
		}
	}
	return s.Status(item.ID)
}

// issueTitle is the tracker mirror's title of the issue, else the issue id.
func (s *Service) issueTitle(issue string) string {
	recs, err := s.store.FindTrackerIssues(issue)
	if err == nil {
		for _, r := range recs {
			var b struct {
				Title string `json:"title"`
			}
			if json.Unmarshal(r.Body, &b) == nil && strings.TrimSpace(b.Title) != "" {
				return issue + " " + strings.TrimSpace(b.Title)
			}
		}
	}
	return issue
}

// Done evaluates the current step's gate and, when it passes, moves the flow to the next
// step (the last step finishes it). A blocked flow is unblocked first. A gate that does not
// pass leaves the flow where it is and says what is missing. Of several concurrent calls
// exactly one advances the flow; the others see Moved.
func (s *Service) Done(id, by string) (Result, error) { return s.advance(id, by, triggerDone) }

// Approve records a person's approval of the current step and advances when the gate passes
// with it: the review gate is the approval itself, and a gate that cannot be judged here
// (needs_human) is waived. A gate that is plainly unmet (a doc missing) stays unmet.
// The entry layer must admit only a person.
func (s *Service) Approve(id, by string) (Result, error) {
	ws, _, err := s.load(id)
	if err != nil {
		return Result{}, err
	}
	if !ws.Open() {
		return Result{}, jobstore.ErrStageClosed
	}
	step := ws.Step
	if _, err := s.store.TransitionWorkStage(jobstore.StageTransition{
		WorkItemID: ws.WorkItemID, FromStep: ws.Step, ToStep: ws.Step, ToStatus: jobstore.StageActive,
		ApprovedStep: &step, Journal: fmt.Sprintf("stage approve: %s approved by %s", ws.StepName(), by), By: by,
	}); err != nil {
		return Result{}, err
	}
	return s.advance(id, by, triggerApprove)
}

func (s *Service) advance(id, by, trigger string) (Result, error) {
	ws, item, err := s.load(id)
	if err != nil {
		return Result{}, err
	}
	if !ws.Open() {
		return Result{}, jobstore.ErrStageClosed
	}
	from := ws.StepName()
	if ws.Status == jobstore.StageBlocked {
		text := "stage unblocked at " + from
		if ws.BlockedReason != "" {
			text += " (was: " + ws.BlockedReason + ")"
		}
		applied, err := s.store.TransitionWorkStage(jobstore.StageTransition{
			WorkItemID: ws.WorkItemID, FromStep: ws.Step, ToStep: ws.Step, ToStatus: jobstore.StageActive,
			Statuses: []string{jobstore.StageBlocked}, Journal: text, By: by,
		})
		if err != nil {
			return Result{}, err
		}
		if applied {
			ws.Status, ws.BlockedReason = jobstore.StageActive, ""
		}
	}
	gate, err := s.evalFor(ws, item)
	if err != nil {
		return Result{}, err
	}
	if !gate.OK {
		if err := s.store.AppendStageJournal(ws.WorkItemID, gateJournal(gate), by, jobstore.WorkLevelDetail); err != nil {
			return Result{}, err
		}
		v := s.viewOf(ws, item.ProjectKey)
		v.Gate = &gate
		return Result{Advanced: false, Gate: gate, Stage: v, From: from}, nil
	}
	next := ws.Step + 1
	toStatus := jobstore.StageActive
	if next >= len(ws.Steps)-1 {
		toStatus = jobstore.StageDone
		next = len(ws.Steps) - 1
	}
	if next <= ws.Step { // a one-step template: nothing after the step
		toStatus, next = jobstore.StageDone, ws.Step
	}
	tr := jobstore.StageTransition{
		WorkItemID: ws.WorkItemID, FromStep: ws.Step, ToStep: next, ToStatus: toStatus,
		Journal: fmt.Sprintf("stage step: %s > %s (gate passed)", from, ws.Steps[next]), By: by,
	}
	if from == StepDoing {
		sha := s.headSHA(item)
		tr.DoingDoneSHA = &sha
	}
	would, wouldOK := wouldAfter(trigger, ws.Steps[next])
	if wouldOK && toStatus == jobstore.StageActive {
		tr.Action = would.journal()
	}
	applied, err := s.store.TransitionWorkStage(tr)
	if err != nil {
		return Result{}, err
	}
	if applied && tr.Action != "" {
		s.afterStep(item, ws.Level, would)
	}
	cur, _, err := s.store.GetWorkStage(ws.WorkItemID)
	if err != nil {
		return Result{}, err
	}
	v := s.viewOf(cur, item.ProjectKey)
	return Result{Advanced: applied, Moved: !applied, Gate: gate, Stage: v, From: from}, nil
}

func gateJournal(g Gate) string {
	kind := "stage gate not passed"
	if g.NeedsHuman {
		kind = "stage gate needs a person"
	}
	return fmt.Sprintf("%s at %s: %s", kind, g.Step, strings.Join(g.Missing, "; "))
}

// headSHA is the project root's HEAD, "" when it cannot be read here.
func (s *Service) headSHA(item jobstore.WorkItem) string {
	c := s.config()
	if c == nil || item.ProjectKey == "" {
		return ""
	}
	p, ok := c.Projects[item.ProjectKey]
	if !ok {
		return ""
	}
	out, err := s.git(c.ExecPath(p), 1024, "rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	out = strings.TrimSpace(out)
	if !shaRe.MatchString(out) {
		return ""
	}
	return out
}

// Back sends the flow to the previous step; the reason is required and goes to the journal.
func (s *Service) Back(id, reason, by string) (View, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return View{}, fmt.Errorf("%w: back needs a reason", jobstore.ErrWorkInvalid)
	}
	ws, item, err := s.load(id)
	if err != nil {
		return View{}, err
	}
	if !ws.Open() {
		return View{}, jobstore.ErrStageClosed
	}
	if ws.Step == 0 {
		return View{}, fmt.Errorf("%w: the flow is at its first step (%s), there is nothing to go back to", jobstore.ErrWorkInvalid, ws.StepName())
	}
	tr := jobstore.StageTransition{
		WorkItemID: ws.WorkItemID, FromStep: ws.Step, ToStep: ws.Step - 1, ToStatus: jobstore.StageActive,
		Journal: fmt.Sprintf("stage back: %s > %s: %s", ws.StepName(), ws.Steps[ws.Step-1], reason), By: by,
	}
	would, wouldOK := wouldAfter(triggerBack, ws.Steps[ws.Step-1])
	if wouldOK {
		tr.Action = would.journal()
	}
	applied, err := s.store.TransitionWorkStage(tr)
	if err != nil {
		return View{}, err
	}
	if !applied {
		return View{}, ErrMoved
	}
	if wouldOK {
		s.afterStep(item, ws.Level, would)
	}
	return s.Status(item.ID)
}

// Blocked marks the flow blocked on its current step with a reason. A later Done lifts it.
func (s *Service) Blocked(id, reason, by string) (View, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return View{}, fmt.Errorf("%w: blocked needs a reason", jobstore.ErrWorkInvalid)
	}
	ws, _, err := s.load(id)
	if err != nil {
		return View{}, err
	}
	if !ws.Open() {
		return View{}, jobstore.ErrStageClosed
	}
	applied, err := s.store.TransitionWorkStage(jobstore.StageTransition{
		WorkItemID: ws.WorkItemID, FromStep: ws.Step, ToStep: ws.Step, ToStatus: jobstore.StageBlocked,
		BlockedReason: reason, Journal: fmt.Sprintf("stage blocked at %s: %s", ws.StepName(), reason), By: by,
	})
	if err != nil {
		return View{}, err
	}
	if !applied {
		return View{}, ErrMoved
	}
	return s.Status(ws.WorkItemID)
}

// Drop abandons the flow (the work item itself is untouched). The reason is optional.
func (s *Service) Drop(id, reason, by string) (View, error) {
	ws, item, err := s.load(id)
	if err != nil {
		return View{}, err
	}
	if !ws.Open() {
		return View{}, jobstore.ErrStageClosed
	}
	text := "stage dropped at " + ws.StepName()
	if r := strings.TrimSpace(reason); r != "" {
		text += ": " + r
	}
	applied, err := s.store.TransitionWorkStage(jobstore.StageTransition{
		WorkItemID: ws.WorkItemID, FromStep: ws.Step, ToStep: ws.Step, ToStatus: jobstore.StageDropped, Journal: text, By: by,
	})
	if err != nil {
		return View{}, err
	}
	if !applied {
		return View{}, ErrMoved
	}
	cur, _, err := s.store.GetWorkStage(ws.WorkItemID)
	if err != nil {
		return View{}, err
	}
	return s.viewOf(cur, item.ProjectKey), nil
}
