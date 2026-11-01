package inbox

import (
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/today"
)

// Category is one class of the 「等我」 (waiting on me) list. The classes are mutually
// exclusive and exhaustive over what may call a person (design 2026-11-01 §4.1): a subject
// falls into the FIRST class whose structured fact holds, in the order of Categories.
type Category string

const (
	CatApproval  Category = "approval"   // 待你批准
	CatAnswer    Category = "answer"     // 待你回答
	CatReview    Category = "review"     // 待你验收
	CatPlanStuck Category = "plan_stuck" // 计划卡住
	CatFailed    Category = "failed"     // 你提交的任务失败了
	CatDelivery  Category = "delivery"   // 投递待你确认
)

// Categories is the judgement order: first match wins.
var Categories = []Category{CatApproval, CatAnswer, CatReview, CatPlanStuck, CatFailed, CatDelivery}

// KindJobFailed is the card kind of a failed job a person submitted.
const KindJobFailed = today.KindJobFailed

// Signals are the structured facts about ONE subject (a job, a session, a plan, a decision).
// Every field is read from a store row or a server-stamped marker, never from a model's
// summary or inferred work-item status.
type Signals struct {
	// HeldForApproval: job status awaiting_approval (a push hold included).
	HeldForApproval bool
	// ApprovalInteraction: a pending interaction of type permission / confirmation, or a
	// terminal tool-permission prompt.
	ApprovalInteraction bool
	// Question: a gofer_ask_human decision, a pending question / choice interaction, an
	// AskUserQuestion, or a session relay turn awaiting the person's reply.
	Question bool
	// NeedsReview: job status needs_review.
	NeedsReview bool
	// PlanBlocked: plan status blocked.
	PlanBlocked bool
	// TodoNeedsReview: a plan todo whose job waits in needs_review.
	TodoNeedsReview bool
	// FailedBySelf: terminal failed / timeout, submitted by a person (job.HumanSubmitted),
	// with no follow-up job.
	FailedBySelf bool
	// DeliveryHeld: a letter held for nobody to receive, or bounced.
	DeliveryHeld bool
}

// Classify returns the first Category whose fact holds, in Categories order; ok is false
// when the subject does not wait on a person at all.
func Classify(s Signals) (Category, bool) {
	switch {
	case s.HeldForApproval || s.ApprovalInteraction:
		return CatApproval, true
	case s.Question:
		return CatAnswer, true
	case s.NeedsReview:
		return CatReview, true
	case s.PlanBlocked || s.TodoNeedsReview:
		return CatPlanStuck, true
	case s.FailedBySelf:
		return CatFailed, true
	case s.DeliveryHeld:
		return CatDelivery, true
	}
	return "", false
}

// Rank is the position of c in Categories (lower = earlier); unknown sorts last.
func Rank(c Category) int {
	for i, x := range Categories {
		if x == c {
			return i
		}
	}
	return len(Categories)
}

// Label is the 「等谁、要什么」 text of a category in lang.
func Label(lang i18n.Lang, c Category) string {
	return i18n.T(lang, "inbox.waiting."+string(c))
}

// signalsOf reads the structured facts a today card stands for, and the subject they are
// about: cards about the same job (an approval interaction and a needs_review state, say)
// share a subject so the job is listed once, under its first matching class.
func signalsOf(c today.Card) (subject string, s Signals, ok bool) {
	switch c.Kind {
	case today.KindApproval:
		return "job:" + c.Refs.JobID, Signals{HeldForApproval: true}, true
	case today.KindInteraction:
		sub := "job:" + c.Refs.JobID
		switch c.Subtype {
		case job.InteractionTypePermission, job.InteractionTypeConfirmation:
			return sub, Signals{ApprovalInteraction: true}, true
		}
		return sub, Signals{Question: true}, true
	case today.KindPermission:
		sub := "session:" + c.Refs.SessionID
		if c.Subtype == today.PermissionSubtypeQuestion {
			return sub, Signals{Question: true}, true
		}
		return sub, Signals{ApprovalInteraction: true}, true
	case today.KindDecision:
		return "decision:" + c.Refs.DecisionID, Signals{Question: true}, true
	case today.KindRelay:
		return "session:" + c.Refs.SessionID, Signals{Question: true}, true
	case today.KindReview:
		return "job:" + c.Refs.JobID, Signals{NeedsReview: true, TodoNeedsReview: c.Refs.TodoID != ""}, true
	case today.KindPlanBlocked:
		return "plan:" + c.Refs.PlanID, Signals{PlanBlocked: true}, true
	case today.KindMailHeld:
		return c.Key, Signals{DeliveryHeld: true}, true
	case KindJobFailed:
		return "job:" + c.Refs.JobID, Signals{FailedBySelf: true}, true
	}
	return "", Signals{}, false
}

// Waiting folds the candidate cards into THE 「等我」 list. Cards whose kind carries no
// structured waiting fact (work items, suggestions, merges, memory, capture, ...) are left
// out. Cards about one subject collapse to one entry under the first matching Category, which
// keeps the position of the subject's first card; the input order is otherwise kept. Each
// returned card carries Waiting (the category id) and WaitingLabel (in lang). The result is
// never nil.
func Waiting(lang i18n.Lang, cards []today.Card) []today.Card {
	out := make([]today.Card, 0, len(cards))
	at := map[string]int{}
	for _, c := range cards {
		subject, sig, ok := signalsOf(c)
		if !ok {
			continue
		}
		cat, ok := Classify(sig)
		if !ok {
			continue
		}
		c.Waiting, c.WaitingLabel = string(cat), Label(lang, cat)
		if i, dup := at[subject]; dup {
			if Rank(cat) < Rank(Category(out[i].Waiting)) {
				out[i] = c
			}
			continue
		}
		at[subject] = len(out)
		out = append(out, c)
	}
	return out
}

// UnreadCount counts the items of a column that are unread.
func UnreadCount(items []Item) int {
	n := 0
	for _, it := range items {
		if it.State == "unread" {
			n++
		}
	}
	return n
}

// ---- terminal jobs

// TerminalSource is the slice of the job store the terminal-job sources read.
type TerminalSource interface {
	ListJobs(q jobstore.ListQuery) ([]jobstore.JobRecord, error)
}

// provenanceSource is implemented by a store that knows from when job provenance is
// recorded (jobstore.Store.ProvenanceSince). Jobs started before that carry no stamp telling
// a person's submission from an agent's, so they never count as the person's own.
type provenanceSource interface {
	ProvenanceSince() int64
}

// failedWindowSec bounds how old a failed job of the person's may be and still wait for
// them: an older failure is history, not something to act on now.
const failedWindowSec = 72 * 3600

// clockSource is implemented by a store with a clock (jobstore.Store.UnixNow); the failure
// window applies only then, so a source without one keeps every failure.
type clockSource interface {
	UnixNow() int64
}

// tooOld reports whether a failure that ended at endedAt is past the window.
func tooOld(src TerminalSource, endedAt int64) bool {
	c, ok := src.(clockSource)
	return ok && c.UnixNow()-endedAt > failedWindowSec
}

// ownTerminal reports whether j is a terminal job of the person's own that the inbox
// may show: human-submitted, started after provenance is recorded, visible to the caller.
func ownTerminal(src TerminalSource, j jobstore.JobRecord, allow func(string) bool) bool {
	if j.EndedAt == 0 || !job.HumanSubmitted(j) || (allow != nil && !allow(j.ProjectKey)) {
		return false
	}
	if ps, ok := src.(provenanceSource); ok {
		if since := ps.ProvenanceSince(); since > 0 && j.StartedAt < since {
			return false
		}
	}
	return true
}

// terminalScan bounds how many newest rows of one status are considered.
const terminalScan = 100

// TerminalCards builds the person's own terminal-job cards:
//
//   - failed: a failed / timed-out job a person submitted themselves (job.HumanSubmitted) that
//     nothing continued (no auto-resume, no failover, no job naming it as its source). It is a
//     "job.failed" card and enters the 「等我」 list.
//   - fyi: a done job a person submitted themselves. It is a "job.terminal" card, the only
//     terminal an inbox shows besides the failures above.
//
// Jobs of an agent, a session, a job credential or the system produce neither: their result
// already went back to whoever started them. allow, when non-nil, is the caller's project
// scope. Failures are ordered newest first.
func TerminalCards(src TerminalSource, lang i18n.Lang, allow func(string) bool) (failed, fyi []today.Card, err error) {
	failed, fyi = []today.Card{}, []today.Card{}
	for _, status := range []string{job.StatusFailed, job.StatusTimeout} {
		rows, err := src.ListJobs(jobstore.ListQuery{Status: status, Limit: terminalScan})
		if err != nil {
			return nil, nil, err
		}
		for _, j := range rows {
			if !ownTerminal(src, j, allow) || tooOld(src, j.EndedAt) {
				continue
			}
			if j.AutoResumedBy != "" || j.FellBackTo != "" {
				continue
			}
			next, err := src.ListJobs(jobstore.ListQuery{SourceJob: j.ID, Limit: 1})
			if err != nil {
				return nil, nil, err
			}
			if len(next) > 0 {
				continue
			}
			failed = append(failed, TerminalCard(lang, j, KindJobFailed))
		}
	}
	sortNewestFirst(failed)
	rows, err := src.ListJobs(jobstore.ListQuery{Status: job.StatusDone, Limit: terminalScan})
	if err != nil {
		return nil, nil, err
	}
	for _, j := range rows {
		if !ownTerminal(src, j, allow) {
			continue
		}
		fyi = append(fyi, TerminalCard(lang, j, "job.terminal"))
	}
	return failed, fyi, nil
}

func sortNewestFirst(cards []today.Card) {
	for i := 1; i < len(cards); i++ {
		for j := i; j > 0 && cards[j].ActivityAt > cards[j-1].ActivityAt; j-- {
			cards[j], cards[j-1] = cards[j-1], cards[j]
		}
	}
}

// TerminalCard is the card of a finished job. The key is the same for both kinds so a job's
// read / cleared state follows it when it moves between columns (the revision changes, so it
// resurfaces).
func TerminalCard(lang i18n.Lang, j jobstore.JobRecord, kind string) today.Card {
	title := j.ID
	if j.Error != "" {
		title = i18n.T(lang, "httpapi.inbox_job_failed", "id", j.ID)
	}
	c := today.Card{Key: "job.terminal:" + j.ID, Kind: kind, Title: title, Summary: j.Error, ProjectKey: j.ProjectKey, ActivityAt: j.EndedAt, WaitingSince: j.EndedAt, Refs: today.Refs{JobID: j.ID}, Actions: []today.Action{}}
	if kind == KindJobFailed {
		c.Actions = []today.Action{{ID: "open", Label: i18n.T(lang, "today.action.details"), Style: "link", Value: "/jobs/" + j.ID}}
	}
	return c
}
