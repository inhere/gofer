package stage

import (
	"sort"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// The consistency report of the autonomy levels (plan F4), the stage twin of the plan
// driver's (internal/job/driver/report.go): every recorded "would do" is compared with what
// actually happened next, then totalled by action and by rule.
//
// Same method as the driver, restated here because stage may not import job (G022) and the
// driver's summary is written over job.PlanDriverReportRow / jobstore.PlanDriverAction:
//
//   - an action reads match / mismatch once its follow-up is known (the first step change of
//     the flow after the action), no_followup once ReportObserveWindow passed without one,
//     and stays pending (not observed yet) before that;
//   - Rate = match / (match + mismatch + no_followup): a no_followup is not agreement;
//   - the recent figure covers the latest ReportRecentWindow resolved actions;
//   - the outcome is read from the journal at report time (nothing is stamped), so it is
//     repeatable from any later moment.

// ReportObserveWindow is how long an action is watched before it reads no_followup; the
// plan driver's window (driver.ObserveWindow).
const ReportObserveWindow = 24 * time.Hour

// ReportRecentWindow is how many resolved actions the recent figure covers (the driver's
// job.PlanDriverUpgradeWindow).
const ReportRecentWindow = 20

// Outcomes of a recorded action.
const (
	OutcomeMatch      = "match"
	OutcomeMismatch   = "mismatch"
	OutcomeNoFollowup = "no_followup"
	OutcomePending    = "pending"
)

// What actually happened after an action (the first step change that followed).
const (
	observedAgentAdvance = "agent_advance" // the flow moved on by the agent's own `done`
	observedHumanAdvance = "human_advance" // a person moved it on
	observedHumanApprove = "human_approve"
	observedAgentBack    = "agent_back"
	observedHumanBack    = "human_back"
	observedBlocked      = "blocked"
	observedDropped      = "dropped"
)

// agreesWith maps an action to what counts as the flow doing the same: the continue turn is
// right when the agent then carried the step through; the waiting card is right when a
// person then acted on it.
var agreesWith = map[string][]string{
	ActionSendContinue: {observedAgentAdvance},
	ActionWaitCard:     {observedHumanApprove, observedHumanBack, observedHumanAdvance},
}

// ReportRow totals actions: one row per action, or per action and rule.
type ReportRow struct {
	Action string `json:"action"`
	// Rule is set on the ByRule rows only.
	Rule       string  `json:"rule,omitempty"`
	Total      int     `json:"total"`
	Match      int     `json:"match"`
	Mismatch   int     `json:"mismatch"`
	NoFollowup int     `json:"no_followup"`
	Pending    int     `json:"pending"`
	Rate       float64 `json:"rate"`
	// RecentN / RecentMatch / RecentRate cover the latest ReportRecentWindow resolved
	// actions (ByAction rows only).
	RecentN     int     `json:"recent_n,omitempty"`
	RecentMatch int     `json:"recent_match,omitempty"`
	RecentRate  float64 `json:"recent_rate,omitempty"`
}

// ReportAction is one recorded action with its outcome.
type ReportAction struct {
	WorkItemID string `json:"work_item_id"`
	Action     string `json:"action"`
	Step       string `json:"step"`
	Rule       string `json:"rule"`
	// Outcome is match / mismatch / no_followup / pending.
	Outcome string `json:"outcome"`
	// Observed is what actually happened next ("" while pending or with no follow-up).
	Observed string `json:"observed,omitempty"`
	At       int64  `json:"at"`
}

// Report is the answer of `gofer stage report`.
type Report struct {
	// WorkItemID / ProjectKey say what the report covers (both empty = every flow).
	WorkItemID  string `json:"work_item_id,omitempty"`
	ProjectKey  string `json:"project_key,omitempty"`
	WindowHours int    `json:"window_hours"`
	Total       int    `json:"total"`
	Pending     int    `json:"pending"`
	// Recent* is the overall figure of the latest ReportRecentWindow resolved actions.
	RecentN     int         `json:"recent_n"`
	RecentMatch int         `json:"recent_match"`
	RecentRate  float64     `json:"recent_rate"`
	ByAction    []ReportRow `json:"by_action"`
	ByRule      []ReportRow `json:"by_rule"`
	// Recent lists the newest actions, newest first.
	Recent []ReportAction `json:"recent"`
}

// Report builds the consistency report of one work item (id != "") or of a project (project
// != ""; both empty = every flow on this server).
func (s *Service) Report(workItemID, project string) (Report, error) {
	workItemID, project = strings.TrimSpace(workItemID), strings.TrimSpace(project)
	lines, err := s.store.ListStageJournal(workItemID, project)
	if err != nil {
		return Report{}, err
	}
	rep := summarizeActions(observeActions(lines, s.now()))
	rep.WorkItemID, rep.ProjectKey = workItemID, project
	return rep, nil
}

func (s *Service) now() time.Time {
	if s.nowFn != nil {
		return s.nowFn()
	}
	return time.Now()
}

func isHuman(by string) bool { return by == "human" || strings.HasPrefix(by, "human:") }

// transitionOf reads a journal line as the step change it records ("" = not one: a gate
// result, an unblock, a level change, a reminder ...).
func transitionOf(l jobstore.StageJournalLine) string {
	if l.Kind != jobstore.WorkJournalStage {
		return ""
	}
	switch {
	case strings.HasPrefix(l.Text, "stage step:"):
		if isHuman(l.By) {
			return observedHumanAdvance
		}
		return observedAgentAdvance
	case strings.HasPrefix(l.Text, "stage approve:"):
		return observedHumanApprove
	case strings.HasPrefix(l.Text, "stage back:"):
		if isHuman(l.By) {
			return observedHumanBack
		}
		return observedAgentBack
	case strings.HasPrefix(l.Text, "stage blocked"):
		return observedBlocked
	case strings.HasPrefix(l.Text, "stage dropped"):
		return observedDropped
	}
	return ""
}

// observeActions settles every recorded action of the lines (grouped by work item, oldest
// first inside one) against the first step change that followed it.
func observeActions(lines []jobstore.StageJournalLine, now time.Time) []ReportAction {
	var out []ReportAction
	for i, l := range lines {
		if l.Kind != jobstore.WorkJournalStageAction {
			continue
		}
		w, ok := parseWould(l.Text)
		if !ok {
			continue
		}
		a := ReportAction{WorkItemID: l.WorkItemID, Action: w.Action, Step: w.Step, Rule: w.Rule, At: l.At, Outcome: OutcomePending}
		for _, n := range lines[i+1:] {
			if n.WorkItemID != l.WorkItemID {
				break
			}
			if obs := transitionOf(n); obs != "" {
				a.Observed = obs
				break
			}
		}
		switch {
		case a.Observed != "" && agrees(w.Action, a.Observed):
			a.Outcome = OutcomeMatch
		case a.Observed != "":
			a.Outcome = OutcomeMismatch
		case now.Unix()-l.At >= int64(ReportObserveWindow/time.Second):
			a.Outcome = OutcomeNoFollowup
		}
		out = append(out, a)
	}
	// Oldest first across work items, so "the latest N" means the newest in time.
	sort.SliceStable(out, func(i, j int) bool { return out[i].At < out[j].At })
	return out
}

func agrees(action, observed string) bool {
	for _, o := range agreesWith[action] {
		if o == observed {
			return true
		}
	}
	return false
}

type ruleKey struct{ action, rule string }

func (r *ReportRow) add(outcome string) {
	r.Total++
	switch outcome {
	case OutcomeMatch:
		r.Match++
	case OutcomeMismatch:
		r.Mismatch++
	case OutcomeNoFollowup:
		r.NoFollowup++
	default:
		r.Pending++
	}
}

func (r *ReportRow) finish() {
	if n := r.Match + r.Mismatch + r.NoFollowup; n > 0 {
		r.Rate = float64(r.Match) / float64(n)
	}
}

// summarizeActions totals actions (oldest first) by action and by action+rule.
func summarizeActions(actions []ReportAction) Report {
	rep := Report{WindowHours: int(ReportObserveWindow.Hours()), ByAction: []ReportRow{}, ByRule: []ReportRow{}, Recent: []ReportAction{}}
	acts := map[string]*ReportRow{}
	rules := map[ruleKey]*ReportRow{}
	resolved := map[string][]string{}
	var all []string
	for _, a := range actions {
		ar := acts[a.Action]
		if ar == nil {
			ar = &ReportRow{Action: a.Action}
			acts[a.Action] = ar
		}
		k := ruleKey{a.Action, a.Rule}
		rr := rules[k]
		if rr == nil {
			rr = &ReportRow{Action: a.Action, Rule: a.Rule}
			rules[k] = rr
		}
		ar.add(a.Outcome)
		rr.add(a.Outcome)
		rep.Total++
		if a.Outcome == OutcomePending {
			rep.Pending++
			continue
		}
		resolved[a.Action] = append(resolved[a.Action], a.Outcome)
		all = append(all, a.Outcome)
	}
	for _, r := range acts {
		r.finish()
		r.RecentN, r.RecentMatch = recentMatches(resolved[r.Action])
		if r.RecentN > 0 {
			r.RecentRate = float64(r.RecentMatch) / float64(r.RecentN)
		}
		rep.ByAction = append(rep.ByAction, *r)
	}
	for _, r := range rules {
		r.finish()
		rep.ByRule = append(rep.ByRule, *r)
	}
	sort.Slice(rep.ByAction, func(i, j int) bool { return rep.ByAction[i].Action < rep.ByAction[j].Action })
	sort.Slice(rep.ByRule, func(i, j int) bool {
		if rep.ByRule[i].Action != rep.ByRule[j].Action {
			return rep.ByRule[i].Action < rep.ByRule[j].Action
		}
		return rep.ByRule[i].Rule < rep.ByRule[j].Rule
	})
	rep.RecentN, rep.RecentMatch = recentMatches(all)
	if rep.RecentN > 0 {
		rep.RecentRate = float64(rep.RecentMatch) / float64(rep.RecentN)
	}
	for i := len(actions) - 1; i >= 0 && len(rep.Recent) < ReportRecentWindow; i-- {
		rep.Recent = append(rep.Recent, actions[i])
	}
	return rep
}

// recentMatches counts the matches among the latest ReportRecentWindow outcomes (oldest first).
func recentMatches(outcomes []string) (n, match int) {
	if len(outcomes) > ReportRecentWindow {
		outcomes = outcomes[len(outcomes)-ReportRecentWindow:]
	}
	for _, o := range outcomes {
		if o == OutcomeMatch {
			match++
		}
	}
	return len(outcomes), match
}
