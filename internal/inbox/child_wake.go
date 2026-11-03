package inbox

import (
	"encoding/json"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/today"
)

// KindChildWakeFailed is the card kind of a child job whose result can no longer reach
// the resident parent job that submitted it (design 2026-11-03 告警).
const KindChildWakeFailed = today.KindChildWakeFailed

// childWakeScan bounds how many alert rows one request looks at (they are already
// windowed by time; the cap keeps a burst from dominating the inbox).
const childWakeScan = 100

// ChildWakeSource is the slice of the job store the child-receipt alerts read: the
// cross-job event query plus the parent job row (for the project scope and the link).
type ChildWakeSource interface {
	ListJobEventsByType(eventType string, sinceAt int64) ([]jobstore.JobEvent, error)
	GetJob(id string) (jobstore.JobRecord, bool, error)
	UnixNow() int64
}

// ChildWakeCards builds the cards of child receipts that will never reach their resident
// parent (job.child_wake_failed): the parent ended, or the receipt expired before the
// child produced an outcome. Each card is about the PARENT job — that is where the child
// list and the reason live — and uses the same 72-hour window as the failed-job cards: an
// older alert is history, not something to act on now. allow, when non-nil, is the
// caller's project scope. Newest first.
func ChildWakeCards(src ChildWakeSource, lang i18n.Lang, allow func(string) bool) ([]today.Card, error) {
	since := src.UnixNow() - failedWindowSec
	events, err := src.ListJobEventsByType(job.EventJobChildWakeFailed, since)
	if err != nil {
		return nil, err
	}
	cards := make([]today.Card, 0, len(events))
	seenParent := make(map[string]bool, len(events))
	for _, ev := range events {
		if len(cards) >= childWakeScan {
			break
		}
		var detail struct {
			WakeupID   string `json:"wakeup_id"`
			ChildJobID string `json:"child_job_id"`
			Reason     string `json:"reason"`
		}
		_ = json.Unmarshal([]byte(ev.Detail), &detail)
		parent, ok, err := src.GetJob(ev.JobID)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue // a pruned parent: nothing to show or open
		}
		if allow != nil && !allow(parent.ProjectKey) {
			continue
		}
		// One card per stranded child receipt, but the subject (and so the 「等我」 entry)
		// is the parent: several alerts of one parent read as one item.
		key := "job.child_wake:" + detail.WakeupID
		if detail.WakeupID == "" {
			key = "job.child_wake:" + parent.ID + ":" + detail.ChildJobID
		}
		if seenParent[key] {
			continue
		}
		seenParent[key] = true
		cards = append(cards, today.Card{
			Key:          key,
			Kind:         KindChildWakeFailed,
			Title:        i18n.T(lang, "httpapi.inbox_child_wake_failed", "child", detail.ChildJobID, "parent", parent.ID),
			Summary:      childWakeReason(lang, detail.Reason),
			ProjectKey:   parent.ProjectKey,
			ActivityAt:   ev.At,
			WaitingSince: ev.At,
			Refs:         today.Refs{JobID: parent.ID},
			Actions: []today.Action{{
				ID:    "open",
				Label: i18n.T(lang, "today.action.details"),
				Style: "link",
				Value: "/jobs/" + parent.ID,
			}},
		})
	}
	return cards, nil
}

// childWakeReason renders the machine reason of an alert; an unknown one shows as-is
// rather than as an empty summary.
//
// The presence check has to go through HasKey: i18n.T returns the KEY ITSELF for an
// unknown key (it only falls back to English, then gives up), so a "text != empty" test is
// always true and an unmapped reason would be shown to a person as
// httpapi.inbox_child_wake_reason_x. T also falls back to English, so the key counts as
// known when either the request's language or English carries it.
func childWakeReason(lang i18n.Lang, reason string) string {
	if reason == "" {
		return ""
	}
	key := "httpapi.inbox_child_wake_reason_" + reason
	if i18n.HasKey(lang, key) || i18n.HasKey(i18n.EN, key) {
		return i18n.T(lang, key)
	}
	return reason
}
