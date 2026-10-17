package today

import (
	"fmt"
	"sort"
	"strings"
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
	summary := strings.Join(names[:min(shown, len(names))], "、")
	if len(names) > shown {
		summary += fmt.Sprintf(" 等 %d 个", len(names))
	}
	return Card{
		Key: WorkerEnrollCardKey, Kind: KindWorkerEnroll, Tag: "Worker", Urgency: UrgencyNow,
		Title:        fmt.Sprintf("%d 个 worker 申请注册", len(pending)),
		Blocks:       Blocks{Items: len(pending)},
		WaitingSince: oldest, ActivityAt: oldest, ExpiresAt: earliest,
		Summary: oneLine(summary, summaryRunes),
		Actions: []Action{{ID: "open", Label: "去处理", Style: "link", Value: WorkerEnrollLink}},
	}, true
}
