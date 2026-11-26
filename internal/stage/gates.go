package stage

import (
	"errors"
	"fmt"
	"os/exec"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/inhere/gofer/internal/jobstore"
)

// Gate is the answer of one step's gate. Missing is written so it tells the agent what to
// do next. NeedsHuman means the gate cannot be judged here (or only a person can pass it):
// the flow stays on the step and the item belongs in the person's queue — never an error.
type Gate struct {
	Step       string   `json:"step"`
	OK         bool     `json:"ok"`
	Missing    []string `json:"missing"`
	NeedsHuman bool     `json:"needs_human"`
}

func (g Gate) fail(msg string) Gate {
	g.OK = false
	g.Missing = append(g.Missing, msg)
	return g
}

func (g Gate) human(msg string) Gate {
	g = g.fail(msg)
	g.NeedsHuman = true
	return g
}

func (g Gate) finish() Gate {
	g.OK = len(g.Missing) == 0
	if g.OK {
		g.NeedsHuman = false
	}
	if g.Missing == nil {
		g.Missing = []string{}
	}
	return g
}

const (
	// designIssueLines is how many leading lines of the design doc must name the issue.
	designIssueLines = 20
	gitOutCap        = 256 * 1024
	// maxListed caps how many unfinished todos / runs a Missing line spells out.
	maxListed = 5
)

var shaRe = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)

// evalInput is what a gate looks at, read once per evaluation.
type evalInput struct {
	ws    jobstore.WorkStage
	item  jobstore.WorkItem
	links []jobstore.WorkLink
	// verifyTemplates are the workflow templates the verify gate looks for.
	verifyTemplates []string
	// repo is the project's execution root; "" when the project is unknown here.
	repo string
}

func (in evalInput) linksOf(kind, role string) []jobstore.WorkLink {
	var out []jobstore.WorkLink
	for _, l := range in.links {
		if l.Kind == kind && (role == "" || l.Role == role) {
			out = append(out, l)
		}
	}
	return out
}

func (in evalInput) hasStep(name string) bool {
	for _, s := range in.ws.Steps {
		if s == name {
			return true
		}
	}
	return false
}

// evaluate runs the gate of the flow's current step.
func (s *Service) evaluate(in evalInput) Gate {
	g := Gate{Step: in.ws.StepName(), Missing: []string{}}
	switch g.Step {
	case StepDesign:
		return s.designGate(in, g)
	case StepReview:
		return s.reviewGate(in, g)
	case StepPlan:
		return s.planGate(in, g)
	case StepDoing:
		return s.doingGate(in, g)
	case StepVerify:
		return s.verifyGate(in, g)
	default: // done: no gate
		return g.finish()
	}
}

// gitOut runs a read-only git command in the project root.
func (s *Service) gitOut(in evalInput, args ...string) (string, error) {
	if in.repo == "" {
		return "", errors.New("the work item's project is not registered on this server")
	}
	return s.git(in.repo, gitOutCap, args...)
}

// gitReady checks that the project root is a git work tree this server can read; the reason
// is the needs_human text.
func (s *Service) gitReady(in evalInput) error {
	out, err := s.gitOut(in, "rev-parse", "--is-inside-work-tree")
	if err != nil || strings.TrimSpace(out) != "true" {
		reason := "not a git work tree"
		if err != nil {
			reason = err.Error()
		}
		return fmt.Errorf("git is not readable in the project root from this server (%s)", reason)
	}
	return nil
}

// cleanDocPath validates a doc link's repository-relative path.
func cleanDocPath(p string) (string, bool) {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, ":") {
		return "", false
	}
	c := path.Clean(p)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return "", false
	}
	return c, true
}

// docCommitted reports whether git has a commit touching rel on HEAD. A git failure is
// returned as an error (needs_human), not as "not committed".
func (s *Service) docCommitted(in evalInput, rel string) (bool, error) {
	out, err := s.gitOut(in, "log", "-1", "--format=%H", "--", rel)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

func (s *Service) designGate(in evalInput, g Gate) Gate {
	docs := in.linksOf(jobstore.WorkLinkDoc, jobstore.WorkDocRoleDesign)
	issues := in.linksOf(jobstore.WorkLinkIssue, "")
	if len(docs) == 0 {
		g = g.fail("link the design doc to the work item (a doc link with role design, a repository-relative path)")
	}
	if len(issues) == 0 {
		g = g.fail("link the issue the design is about to the work item; the design doc must name it in its first 20 lines")
	}
	if len(docs) == 0 || len(issues) == 0 {
		return g.finish()
	}
	if err := s.gitReady(in); err != nil {
		return g.human(err.Error()).finish()
	}
	for _, d := range docs {
		rel, ok := cleanDocPath(d.Ref)
		if !ok {
			g = g.fail(fmt.Sprintf("design doc %q must be a repository-relative path inside the project", d.Ref))
			continue
		}
		committed, err := s.docCommitted(in, rel)
		if err != nil {
			return g.human("cannot read git history of the design doc: " + err.Error()).finish()
		}
		if !committed {
			g = g.fail(fmt.Sprintf("design doc %s is not committed on the project's current branch: commit it", rel))
			continue
		}
		body, err := s.gitOut(in, "show", "HEAD:./"+rel)
		if err != nil {
			return g.human("cannot read the committed design doc: " + err.Error()).finish()
		}
		head := firstLines(body, designIssueLines)
		named := false
		ids := make([]string, 0, len(issues))
		for _, is := range issues {
			ids = append(ids, is.Ref)
			if strings.Contains(head, is.Ref) {
				named = true
			}
		}
		if !named {
			g = g.fail(fmt.Sprintf("design doc %s must name the issue (%s) in its first %d lines: put the issue id in the header, then commit", rel, strings.Join(ids, " or "), designIssueLines))
		}
	}
	return g.finish()
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

func (s *Service) reviewGate(in evalInput, g Gate) Gate {
	if in.ws.ApprovedStep == in.ws.Step {
		return g.finish()
	}
	reviewMax := in.ws.ReviewMax
	if reviewMax <= 0 {
		reviewMax = DefaultReviewMax
	}
	var rounds []jobstore.WorkLink
	for _, l := range in.linksOf(jobstore.WorkLinkJob, jobstore.WorkJobRoleReview) {
		if l.CreatedAt >= in.ws.StepEnteredAt {
			rounds = append(rounds, l)
		}
	}
	sort.SliceStable(rounds, func(i, j int) bool { return rounds[i].CreatedAt < rounds[j].CreatedAt })
	ids := make([]string, 0, len(rounds))
	for _, l := range rounds {
		ids = append(ids, l.Ref)
	}
	jobs, err := s.store.GetJobsByIDs(ids)
	if err != nil {
		return g.human("cannot read the review jobs: " + err.Error()).finish()
	}
	var notes []string
	pending := false
	for i, l := range rounds {
		if i >= reviewMax {
			break
		}
		rec, found := jobs[l.Ref]
		switch {
		case !found:
			notes = append(notes, fmt.Sprintf("review job %s does not exist", l.Ref))
		case rec.ResultDecision == "pass":
			return g.finish()
		case rec.ResultDecision == "":
			pending = true
			notes = append(notes, fmt.Sprintf("review job %s has not reported a result yet", l.Ref))
		default:
			notes = append(notes, fmt.Sprintf("review job %s decided %s", l.Ref, rec.ResultDecision))
		}
	}
	if len(rounds) >= reviewMax && !pending {
		g = g.human(fmt.Sprintf("review rounds are used up (max %d, %s): only a person can approve now", reviewMax, strings.Join(notes, "; ")))
		return g.finish()
	}
	for _, n := range notes {
		g = g.fail(n)
	}
	if pending {
		return g.finish()
	}
	return g.fail(fmt.Sprintf("get a review job to pass: link a job with role review whose result decision is pass (round %d of max %d), or ask a person to approve", len(rounds)+1, reviewMax)).finish()
}

func (s *Service) planGate(in evalInput, g Gate) Gate {
	plans := in.linksOf(jobstore.WorkLinkPlan, "")
	if len(plans) == 0 {
		g = g.fail("link a gofer plan to the work item (create it with gofer plan create, then link it)")
	}
	for _, l := range plans {
		_, found, err := s.store.GetPlan(l.Ref)
		if err != nil {
			return g.human("cannot read plan " + l.Ref + ": " + err.Error()).finish()
		}
		if !found {
			g = g.fail(fmt.Sprintf("plan %s does not exist: link an existing plan", l.Ref))
			continue
		}
		todos, err := s.store.ListTodosByPlan(l.Ref)
		if err != nil {
			return g.human("cannot read the todos of plan " + l.Ref + ": " + err.Error()).finish()
		}
		if len(todos) == 0 {
			g = g.fail(fmt.Sprintf("plan %s has no todo: add at least one with gofer plan add-todo", l.Ref))
		}
	}
	if !in.ws.PlanDoc {
		return g.finish()
	}
	docs := in.linksOf(jobstore.WorkLinkDoc, jobstore.WorkDocRolePlan)
	if len(docs) == 0 {
		return g.fail("link the plan doc to the work item (a doc link with role plan, a repository-relative path) and commit it").finish()
	}
	if err := s.gitReady(in); err != nil {
		return g.human(err.Error()).finish()
	}
	for _, d := range docs {
		rel, ok := cleanDocPath(d.Ref)
		if !ok {
			g = g.fail(fmt.Sprintf("plan doc %q must be a repository-relative path inside the project", d.Ref))
			continue
		}
		committed, err := s.docCommitted(in, rel)
		if err != nil {
			return g.human("cannot read git history of the plan doc: " + err.Error()).finish()
		}
		if !committed {
			g = g.fail(fmt.Sprintf("plan doc %s is not committed on the project's current branch: commit it", rel))
		}
	}
	return g.finish()
}

func (s *Service) doingGate(in evalInput, g Gate) Gate {
	plans := in.linksOf(jobstore.WorkLinkPlan, "")
	if len(plans) == 0 {
		if in.hasStep(StepPlan) {
			g = g.fail("link a gofer plan to the work item: the doing step is judged by its todos")
		}
		return g.finish()
	}
	unfinished := 0
	var listed []string
	for _, l := range plans {
		todos, err := s.store.ListTodosByPlan(l.Ref)
		if err != nil {
			return g.human("cannot read the todos of plan " + l.Ref + ": " + err.Error()).finish()
		}
		for _, t := range todos {
			if t.Status == jobstore.TodoDone || t.Status == jobstore.TodoSkipped {
				continue
			}
			unfinished++
			if len(listed) < maxListed {
				listed = append(listed, fmt.Sprintf("todo %s (%s) of plan %s", t.TodoID, t.Status, l.Ref))
			}
		}
	}
	if unfinished > 0 {
		msg := "finish or skip every todo of the linked plan: " + strings.Join(listed, ", ")
		if unfinished > len(listed) {
			msg += fmt.Sprintf(" and %d more", unfinished-len(listed))
		}
		g = g.fail(msg)
	}
	return g.finish()
}

func (s *Service) verifyGate(in evalInput, g Gate) Gate {
	if in.ws.DoingDoneSHA == "" {
		return g.human("the commit the doing step finished on was not recorded (git was not readable then): a person has to confirm the verification").finish()
	}
	if !shaRe.MatchString(in.ws.DoingDoneSHA) {
		return g.human("the recorded doing commit is not a valid commit id: a person has to confirm the verification").finish()
	}
	if err := s.gitReady(in); err != nil {
		return g.human(err.Error()).finish()
	}
	templates := in.verifyTemplates
	short := in.ws.DoingDoneSHA
	if len(short) > 8 {
		short = short[:8]
	}
	var after, undecided int
	for _, tpl := range templates {
		f := jobstore.WorkflowListFilter{Status: jobstore.WorkflowDone, Template: tpl}
		if in.item.ProjectKey != "" {
			f.ProjectKeys = []string{in.item.ProjectKey}
		}
		runs, _, err := s.store.ListWorkflowsPage(f, 50, 0)
		if err != nil {
			return g.human("cannot read the workflow runs: " + err.Error()).finish()
		}
		for _, w := range runs {
			if w.CreatedAt < in.ws.StepEnteredAt {
				continue
			}
			after++
			jobs, err := s.store.ListWorkflowJobs(w.ID)
			if err != nil {
				return g.human("cannot read the jobs of workflow run " + w.ID + ": " + err.Error()).finish()
			}
			base := ""
			for _, j := range jobs {
				if j.BaseSHA != "" {
					base = j.BaseSHA
					break
				}
			}
			if !shaRe.MatchString(base) {
				undecided++
				continue
			}
			contains, err := s.isAncestor(in, in.ws.DoingDoneSHA, base)
			if err != nil {
				undecided++
				continue
			}
			if contains {
				return g.finish()
			}
		}
	}
	want := strings.Join(templates, ", ")
	if undecided > 0 {
		return g.human(fmt.Sprintf("%d finished run(s) of workflow template(s) [%s] could not be matched to commit %s (no recorded base commit, or git cannot compare it here): a person has to confirm", undecided, want, short)).finish()
	}
	if after > 0 {
		g = g.fail(fmt.Sprintf("%d finished run(s) of [%s] ran on commits that do not contain %s (where doing finished): run it again on the current HEAD", after, want, short))
	} else {
		g = g.fail(fmt.Sprintf("no finished run of workflow template(s) [%s] started after doing finished: run one on the commit that contains %s (gofer wf run --template <name>)", want, short))
	}
	return g.finish()
}

// isAncestor runs `git merge-base --is-ancestor <anc> <desc>`: true when desc is anc or a
// descendant of it. Exit status 1 is a plain "no"; anything else (an unknown commit, git
// missing) is an error, which the caller counts as undecidable.
func (s *Service) isAncestor(in evalInput, anc, desc string) (bool, error) {
	_, err := s.gitOut(in, "merge-base", "--is-ancestor", anc, desc)
	if err == nil {
		return true, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}
