package today

import (
	"sort"
	"strings"

	"github.com/inhere/gofer/internal/i18n"
)

// KindWorkerEnroll is the single summary card for worker self-enrollment requests that
// still wait for an administrator's decision. It is composed by the HTTP layer for callers
// holding worker.manage only, after (and outside) the Service result: it never goes through
// the Service cache, the snooze table or the advice path.
const KindWorkerEnroll = "worker_enroll"

// WorkerEnrollCardKey is the stable key of the worker enrollment summary card;
// WorkerEnrollLink is the console target its 「去处理」 action opens (the Runners page,
// enrollment section).
const (
	WorkerEnrollCardKey = KindWorkerEnroll + ":pending"
	WorkerEnrollLink    = "/runners#enrollments"
)

// EnrollPending is what the card shows of one pending request: display fields only
// (never a pairing code, secret hash or token).
type EnrollPending struct {
	Hostname    string
	RequestedID string
	CreatedAt   int64
	DecideUntil int64
}

// WorkerEnrollCard builds the one summary card for pending requests, or false when there
// are none. ExpiresAt is the earliest decide deadline.
func WorkerEnrollCard(pending []EnrollPending) (Card, bool) {
	return WorkerEnrollCardIn("", pending)
}

// WorkerEnrollCardIn is WorkerEnrollCard with the card text in lang ("" = the default).
func WorkerEnrollCardIn(lang i18n.Lang, pending []EnrollPending) (Card, bool) {
	if len(pending) == 0 {
		return Card{}, false
	}
	var oldest, earliest int64
	names := make([]string, 0, len(pending))
	for _, p := range pending {
		if oldest == 0 || p.CreatedAt < oldest {
			oldest = p.CreatedAt
		}
		if earliest == 0 || p.DecideUntil < earliest {
			earliest = p.DecideUntil
		}
		label := strings.TrimSpace(p.Hostname)
		if id := strings.TrimSpace(p.RequestedID); id != "" {
			if label != "" {
				label += " → " + id
			} else {
				label = id
			}
		}
		if label == "" {
			label = "(unknown)"
		}
		names = append(names, label)
	}
	sort.Strings(names)
	const shown = 3
	summary := ""
	for i, n := range names[:min(shown, len(names))] {
		if i == 0 {
			summary = n
			continue
		}
		summary = i18n.T(lang, "today.card.enroll_join", "a", summary, "b", n)
	}
	if len(names) > shown {
		summary = i18n.T(lang, "today.card.enroll_more", "names", summary, "n", len(names))
	}
	return Card{
		Key: WorkerEnrollCardKey, Kind: KindWorkerEnroll, Tag: "Worker", Urgency: UrgencyNow,
		Title:        i18n.T(lang, "today.card.enroll_title", "n", len(pending)),
		Blocks:       Blocks{Items: len(pending)},
		WaitingSince: oldest, ActivityAt: oldest, ExpiresAt: earliest,
		Summary: oneLine(summary, summaryRunes),
		Actions: []Action{{ID: "open", Label: i18n.T(lang, "today.action.handle"), Style: "link", Value: WorkerEnrollLink}},
	}, true
}
