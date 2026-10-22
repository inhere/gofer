// Package desktop shows a notification on the machine's OWN desktop — the channel a
// server uses to reach a person who is neither watching the console nor a chat
// client (an approval parked for hours, a session waiting for a reply).
//
// It is split in two halves:
//
//   - Notifier is the platform popup: one implementation per GOOS (notifier_<goos>.go),
//     all of them a short child process (PowerShell toast / osascript / notify-send),
//     so nothing is linked in and a missing tool is a skip, not a crash.
//   - Dispatcher is the policy: same-type notifications arriving inside a window
//     coalesce into ONE popup ("3 个 job 等待批准"), and an environment that can never
//     show a popup (Windows session 0, no desktop session, tool missing) is reported
//     once per cause instead of once per event.
//
// The package imports only internal/procattr + internal/util, so the job service can
// drive it without a cycle, and tests can swap the Notifier (no real popup).
package desktop

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/inhere/gofer/internal/util"
)

// Notification is one system notification: Title is the headline, Text the body and
// Link the console page a click opens (empty when the event has none).
type Notification struct {
	EventType string
	Title     string
	Text      string
	Link      string
	At        int64
}

// Notifier shows ONE notification on the local desktop. A platform that cannot show
// it returns *SkipError; any other error is a real failure (the popup command ran and
// failed).
type Notifier interface {
	Show(n Notification) error
}

// SkipError reports that a notification was NOT shown for an ENVIRONMENT reason that
// retrying cannot change: no interactive desktop (a Windows service in session 0), no
// desktop session, or the platform's notification tool is not installed. Reason is a
// stable key, so the dispatcher records each cause ONCE rather than on every event.
type SkipError struct{ Reason string }

func (e *SkipError) Error() string { return "desktop notification skipped: " + e.Reason }

// DefaultMergeWindow is the coalescing window used when the config sets none: several
// approvals arriving together (a plan handing five items to a person at once) are one
// popup, not five.
const DefaultMergeWindow = 10 * time.Second

// MergeLabels names the thing a merged notification counts, per event type. A type
// without an entry renders "<n> 条通知（<type>）".
var MergeLabels = map[string]string{
	"job.awaiting_approval":  "个 job 等待批准",
	"job.needs_review":       "个 job 等待验收",
	"interaction.created":    "个 job 等待确认",
	"session.awaiting_reply": "个会话等你回复",
}

// maxMergedLines bounds how many items a merged body lists before it summarises the
// rest — a toast taller than its screen is not a notification.
const maxMergedLines = 8

// Dispatcher queues notifications, merges same-type ones inside its window and hands
// each merged group to the Notifier. It is safe for concurrent use.
type Dispatcher struct {
	notifier Notifier
	window   time.Duration

	mu      sync.Mutex
	pending []Notification
	timer   *time.Timer
	logged  map[string]bool // skip reasons already recorded
	closed  bool
}

// NewDispatcher builds a dispatcher around notifier. window <= 0 pops each
// notification immediately (no merging) — on the PUSHING goroutine, so a caller on a
// hot path should keep the window on (the default) and let the timer thread show it.
func NewDispatcher(notifier Notifier, window time.Duration) *Dispatcher {
	if window < 0 {
		window = 0
	}
	return &Dispatcher{notifier: notifier, window: window, logged: make(map[string]bool)}
}

// Push queues n. With a window it is shown when the window closes (merged with the
// same-type notifications queued meanwhile); without one it is shown at once.
func (d *Dispatcher) Push(n Notification) {
	if d == nil || d.notifier == nil {
		return
	}
	if strings.TrimSpace(n.Title) == "" && strings.TrimSpace(n.Text) == "" {
		return
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.pending = append(d.pending, n)
	if d.window > 0 {
		if d.timer == nil {
			d.timer = time.AfterFunc(d.window, d.Flush)
		}
		d.mu.Unlock()
		return
	}
	batch := d.pending
	d.pending = nil
	d.mu.Unlock()
	d.show(batch)
}

// Flush shows everything queued right now.
func (d *Dispatcher) Flush() {
	if d == nil {
		return
	}
	d.mu.Lock()
	batch := d.pending
	d.pending = nil
	d.timer = nil
	d.mu.Unlock()
	d.show(batch)
}

// Close flushes what is queued and stops accepting new notifications. It is what
// shutdown calls so a notification queued a moment before exit is not lost.
func (d *Dispatcher) Close() {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.closed = true
	batch := d.pending
	d.pending = nil
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	d.mu.Unlock()
	d.show(batch)
}

func (d *Dispatcher) show(batch []Notification) {
	for _, n := range mergeBatch(batch) {
		if err := d.notifier.Show(n); err != nil {
			d.report(err)
			continue
		}
		d.clearSkips()
	}
}

// report logs a show failure: an environment skip is recorded once per reason
// (Reason is stable), a real failure every time (it is news each time it happens).
func (d *Dispatcher) report(err error) {
	var skip *SkipError
	if !errors.As(err, &skip) {
		slog.Warn("notify.desktop_failed", "err", err)
		return
	}
	d.mu.Lock()
	seen := d.logged[skip.Reason]
	d.logged[skip.Reason] = true
	d.mu.Unlock()
	if !seen {
		slog.Warn("notify.desktop_skipped", "reason", skip.Reason)
	}
}

// clearSkips forgets the recorded skip reasons after a notification WAS shown: the
// channel works again, so a cause that returns later (settings switched off again, the
// service back in session 0) is news and gets its own notify.desktop_skipped line.
func (d *Dispatcher) clearSkips() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.logged) > 0 {
		d.logged = make(map[string]bool)
	}
}

// mergeBatch groups a batch by event type, keeping first-seen order: the merge is per
// KIND of news (three plans' approvals are three "waiting for approval" items, an
// approval and a session reply are two different popups).
func mergeBatch(batch []Notification) []Notification {
	if len(batch) <= 1 {
		return batch
	}
	order := make([]string, 0, len(batch))
	groups := make(map[string][]Notification, len(batch))
	for _, n := range batch {
		if _, ok := groups[n.EventType]; !ok {
			order = append(order, n.EventType)
		}
		groups[n.EventType] = append(groups[n.EventType], n)
	}
	out := make([]Notification, 0, len(order))
	for _, key := range order {
		out = append(out, mergeGroup(groups[key]))
	}
	return out
}

func mergeGroup(items []Notification) Notification {
	if len(items) == 1 {
		return items[0]
	}
	first := items[0]
	label, ok := MergeLabels[first.EventType]
	if !ok {
		label = "条通知（" + first.EventType + "）"
	}
	lines := make([]string, 0, util.CapSum(len(items), 1))
	for i, it := range items {
		if i == maxMergedLines {
			lines = append(lines, fmt.Sprintf("…另有 %d 条", len(items)-i))
			break
		}
		lines = append(lines, mergeLine(it))
	}
	return Notification{
		EventType: first.EventType,
		Title:     fmt.Sprintf("%d %s", len(items), label),
		Text:      strings.Join(lines, "\n"),
		Link:      first.Link,
		At:        items[len(items)-1].At,
	}
}

// mergeLine is one line of a merged body: the item's headline plus the first line of
// its body when that says something else.
func mergeLine(n Notification) string {
	line := firstLine(n.Title)
	if extra := firstLine(n.Text); extra != "" {
		if line == "" {
			line = extra
		} else if line != extra {
			line += " · " + extra
		}
	}
	return clampRunes(line, 140)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}

func clampRunes(s string, max int) string {
	rs := []rune(strings.TrimSpace(s))
	if len(rs) <= max {
		return string(rs)
	}
	return string(rs[:max]) + "…"
}
