package stage

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// What waits for a person on a flow (plan F4): the facts behind the 「等我」 flow cards. They
// are derived from work_stages and the stored facts every time, not kept anywhere, so a card
// is gone the moment its fact is (an approval, a step back, a passing verify run).

// Kinds of a waiting flow.
const (
	// WaitReview: the review step waits for a person's approval.
	WaitReview = "review"
	// WaitNeedsHuman: the current step's gate cannot be judged here; a person confirms.
	WaitNeedsHuman = "needs_human"
	// WaitVerifyFailed: the newest verify-template run since doing finished failed.
	WaitVerifyFailed = "verify_failed"
)

// WaitingFact is one flow that waits for a person.
type WaitingFact struct {
	WorkItemID string   `json:"work_item_id"`
	Title      string   `json:"title"`
	ProjectKey string   `json:"project_key,omitempty"`
	Step       string   `json:"step"`
	Kind       string   `json:"kind"`
	Missing    []string `json:"missing,omitempty"`
	// Since is when the wait began: entering the step, or the failed run.
	Since int64 `json:"since"`
}

// gateCacheTTL keeps a gate evaluation for card derivation: the 「等我」 list is read by every
// seat status and sweep, and a gate may run git.
const gateCacheTTL = 10 * time.Second

type cachedGate struct {
	at time.Time
	g  Gate
}

type gateCache struct {
	mu sync.Mutex
	m  map[string]cachedGate
}

// Waiting lists the flows in progress that wait for a person; allow, when non-nil, is the
// caller's project scope.
func (s *Service) Waiting(allow func(string) bool) ([]WaitingFact, error) {
	list, err := s.store.ListOpenWorkStages("")
	if err != nil {
		return nil, err
	}
	var out []WaitingFact
	for _, ws := range list {
		if ws.Status != jobstore.StageActive {
			continue // a blocked flow is the agent's own report, not something to decide
		}
		item, ok, err := s.store.GetWorkItem(ws.WorkItemID)
		if err != nil {
			return nil, err
		}
		if !ok || (allow != nil && !allow(item.ProjectKey)) {
			continue
		}
		fact := WaitingFact{WorkItemID: ws.WorkItemID, Title: item.Title, ProjectKey: item.ProjectKey, Step: ws.StepName(), Since: ws.StepEnteredAt}
		switch ws.StepName() {
		case StepReview:
			if ws.ApprovedStep == ws.Step {
				continue
			}
			fact.Kind = WaitReview
			out = append(out, fact)
			continue
		case StepDone, "":
			continue
		case StepVerify:
			if run, failed, err := s.failedVerifyRun(ws, item); err != nil {
				return nil, err
			} else if failed {
				fact.Kind, fact.Since = WaitVerifyFailed, run.CreatedAt
				fact.Missing = []string{fmt.Sprintf("verify run %s (%s) failed; fix it and run it again", run.ID, run.Title)}
				out = append(out, fact)
				continue
			}
		}
		g, err := s.cachedGate(ws, item)
		if err != nil {
			return nil, err
		}
		if !g.OK && g.NeedsHuman {
			fact.Kind, fact.Missing = WaitNeedsHuman, g.Missing
			out = append(out, fact)
		}
	}
	return out, nil
}

func (s *Service) cachedGate(ws jobstore.WorkStage, item jobstore.WorkItem) (Gate, error) {
	key := fmt.Sprintf("%s|%d|%s|%d|%d", ws.WorkItemID, ws.Step, ws.Status, ws.ApprovedStep, ws.UpdatedAt)
	now := s.now()
	s.gates.mu.Lock()
	if c, ok := s.gates.m[key]; ok && now.Sub(c.at) < gateCacheTTL {
		s.gates.mu.Unlock()
		return c.g, nil
	}
	s.gates.mu.Unlock()
	g, err := s.evalFor(ws, item)
	if err != nil {
		return Gate{}, err
	}
	s.gates.mu.Lock()
	if s.gates.m == nil || len(s.gates.m) > 256 {
		s.gates.m = map[string]cachedGate{}
	}
	s.gates.m[key] = cachedGate{at: now, g: g}
	s.gates.mu.Unlock()
	return g, nil
}

// failedVerifyRun finds the newest run of a verify template that started after doing
// finished (the verify step was entered), and reports whether it failed.
func (s *Service) failedVerifyRun(ws jobstore.WorkStage, item jobstore.WorkItem) (jobstore.Workflow, bool, error) {
	templates := slices.Clone(config.DefaultStageVerifyTemplates)
	if c := s.config(); c != nil {
		templates = c.StageVerifyTemplatesFor(item.ProjectKey)
	}
	var newest jobstore.Workflow
	for _, tpl := range templates {
		f := jobstore.WorkflowListFilter{Template: strings.TrimSpace(tpl)}
		if item.ProjectKey != "" {
			f.ProjectKeys = []string{item.ProjectKey}
		}
		runs, _, err := s.store.ListWorkflowsPage(f, 1, 0)
		if err != nil {
			return jobstore.Workflow{}, false, err
		}
		if len(runs) > 0 && runs[0].CreatedAt >= ws.StepEnteredAt && runs[0].CreatedAt >= newest.CreatedAt {
			newest = runs[0]
		}
	}
	return newest, newest.ID != "" && newest.Status == jobstore.WorkflowFailed, nil
}
