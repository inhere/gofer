package steward

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/i18n/prompts"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/work"
)

// rolePrompt is the steward's standing instruction: who it is, what it may and may not do
// (the credential enforces the same limits — this only keeps it from trying), how to write.
// The prompt.steward.role setting replaces it; prompt.steward.role.extra is appended after
// it (settings.go).
var rolePrompt = prompts.MustRender(i18n.ZhCN, prompts.NameStewardRole, struct{}{})

// stewardText is the steward's catalog entry key (under the steward.* sections) in lang.
// The language comes from the steward project (`steward.language` → server.language), so a
// session and its review always read the same language.
func stewardText(lang i18n.Lang, key string, args ...any) string {
	return i18n.T(lang, "steward."+key, args...)
}

// primeStats says how much of the picture the prime carries.
type primeStats struct {
	ItemsTotal   int
	ItemsShown   int
	RequestsShow int
	JournalShown int
	Truncated    bool
}

// Prime is the first message a steward session gets.
type Prime struct {
	Text  string
	Bytes int
	Stats primeStats
}

// priorityClass orders work items for the prime: waiting-on-me first, then due ones, then
// those needing on-site / resources, then the rest (design §14.4).
func priorityClass(it work.ItemView) int {
	switch {
	case it.Status == jobstore.WorkNeedsMe:
		return 0
	case it.Due:
		return 1
	case it.Status == jobstore.WorkNeedsOnsite || it.Status == jobstore.WorkWaitingResource:
		return 2
	}
	return 3
}

func sortForPrime(items []work.ItemView) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := priorityClass(items[i]), priorityClass(items[j])
		if a != b {
			return a < b
		}
		if items[i].LastActivityAt != items[j].LastActivityAt {
			return items[i].LastActivityAt > items[j].LastActivityAt
		}
		return items[i].ID < items[j].ID
	})
}

func runesCap(s string, n int) string {
	s = strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ")), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

// statusLabel names a work item's status in lang (the same words the work catalog gives the
// requests it sends to a session); an unknown status reads as itself.
func statusLabel(lang i18n.Lang, status string) string {
	key := "work.status." + status
	if !i18n.HasKey(i18n.EN, key) {
		return status
	}
	return i18n.T(lang, key)
}

func ago(lang i18n.Lang, now time.Time, at int64) string {
	if at <= 0 {
		return stewardText(lang, "ago.none")
	}
	d := now.Sub(time.Unix(at, 0))
	switch {
	case d < time.Minute:
		return stewardText(lang, "ago.just_now")
	case d < time.Hour:
		return stewardText(lang, "ago.minutes", "n", int(d.Minutes()))
	case d < 48*time.Hour:
		return stewardText(lang, "ago.hours", "n", int(d.Hours()))
	}
	return stewardText(lang, "ago.days", "n", int(d.Hours()/24))
}

func requestKindLabel(lang i18n.Lang, k string) string {
	return labeled(lang, "request_kind", k)
}

func requestStateLabel(lang i18n.Lang, st string) string {
	return labeled(lang, "request_state", st)
}

func journalKindLabel(lang i18n.Lang, k string) string {
	return labeled(lang, "journal_kind", k)
}

// labeled reads the steward catalog's <area>.<value> entry; a value the catalog does not
// know (a newer server's kind) reads as itself.
func labeled(lang i18n.Lang, area, value string) string {
	key := "steward." + area + "." + value
	if !i18n.HasKey(i18n.EN, key) {
		return value
	}
	return i18n.T(lang, key)
}

// itemLine renders one work item on one line: id / status / title / blocker / next /
// last activity / in-flight requests.
func itemLine(lang i18n.Lang, it work.ItemView, now time.Time) string {
	var b strings.Builder
	line := stewardText(lang, "item.line", "id", it.ID, "status", statusLabel(lang, it.Status),
		"title", runesCap(it.Title, 60))
	b.WriteString(line)
	if it.Unsorted {
		b.WriteString(stewardText(lang, "item.draft"))
	}
	if t := runesCap(it.BlockerText, 60); t != "" {
		b.WriteString(" | " + stewardText(lang, "item.blocker", "text", t))
	}
	if t := runesCap(it.NextStep, 60); t != "" {
		b.WriteString(" | " + stewardText(lang, "item.next", "text", t))
	}
	if it.Due {
		b.WriteString(" | " + stewardText(lang, "item.due"))
	} else if it.RemindAt > 0 {
		b.WriteString(" | " + stewardText(lang, "item.remind",
			"time", time.Unix(it.RemindAt, 0).In(now.Location()).Format("01-02 15:04")))
	}
	b.WriteString(" | " + stewardText(lang, "item.last_activity", "ago", ago(lang, now, it.LastActivityAt)))
	if it.SessionOffline {
		b.WriteString(" | " + stewardText(lang, "item.offline"))
	}
	var inflight []string
	for _, r := range it.Requests {
		if jobstore.WorkRequestActive(r.State) {
			inflight = append(inflight, stewardText(lang, "item.inflight_item",
				"kind", requestKindLabel(lang, r.Kind), "state", requestStateLabel(lang, r.State)))
		}
	}
	if len(inflight) > 0 {
		b.WriteString(" | " + stewardText(lang, "item.inflight",
			"items", strings.Join(inflight, stewardText(lang, "item.list_sep"))))
	}
	return b.String()
}

// BuildPrime renders the steward's first message: role + notes + open work items (priority
// truncated) + the last 24h of journal + the request ledger, within primeMax bytes. The
// role and the notes are never cut (the notes are capped on their own); the items take up
// to 60% of what is left, the ledger and the journal share the rest.
func (s *Service) BuildPrime(now time.Time) (Prime, error) {
	lang := s.language()
	items, err := s.work.List(work.WorkListOptsOpen())
	if err != nil {
		return Prime{}, err
	}
	// Noise reduction (design 2026-10-20 §关键流程): the short-lived sessions do not reach
	// the steward's first message either.
	items = s.work.DropShortItems(items, now)
	sortForPrime(items)

	var out strings.Builder
	out.WriteString(s.roleText())
	out.WriteString("\n\n## " + stewardText(lang, "prime.now_heading") + "\n")
	out.WriteString(stewardText(lang, "prime.now_line",
		"time", now.Format("2006-01-02 15:04"), "weekday", weekday(lang, now.Weekday())))

	// Notes.
	notesText, notesVer := "", 0
	if h, ok, nerr := s.store.GetPlanHandoff(jobstore.StewardNotesKey, 0); nerr == nil && ok {
		notesText, notesVer = h.Body, h.Version
	}
	out.WriteString("\n\n## " + stewardText(lang, "prime.notes_heading"))
	if notesVer > 0 {
		out.WriteString(stewardText(lang, "prime.notes_ver",
			"version", notesVer, "kb", fmt.Sprintf("%.1f", float64(len(notesText))/1024)))
	}
	out.WriteString("\n")
	switch txt := strings.TrimSpace(notesText); {
	case txt == "":
		out.WriteString(stewardText(lang, "prime.notes_empty"))
	case len(txt) > 12<<10:
		out.WriteString(txt[:12<<10] + "\n" + stewardText(lang, "prime.notes_truncated"))
	default:
		out.WriteString(txt)
	}

	remaining := s.primeMax - out.Len() - 1024 // headings and the "另有 N" lines
	if remaining < 2048 {
		remaining = 2048 // the sections below always get a floor, even with silly settings
	}
	var st primeStats
	st.ItemsTotal = len(items)

	// Open items, priority order, one line each, up to 60% of the remaining budget.
	itemsBudget := remaining * 6 / 10
	out.WriteString("\n\n## " + stewardText(lang, "prime.items_heading") + "\n")
	if len(items) == 0 {
		out.WriteString(stewardText(lang, "prime.items_empty"))
	}
	used := 0
	for _, it := range items {
		line := itemLine(lang, it, now) + "\n"
		if used+len(line) > itemsBudget {
			break
		}
		out.WriteString(line)
		used += len(line)
		st.ItemsShown++
	}
	if hidden := st.ItemsTotal - st.ItemsShown; hidden > 0 {
		st.Truncated = true
		out.WriteString(stewardText(lang, "prime.items_hidden", "n", hidden) + "\n")
	}
	remaining -= used

	// Request ledger: the in-flight asks and what became of the recent ones.
	ledgerBudget := min(remaining/2, 3<<10)
	out.WriteString("\n## " + stewardText(lang, "prime.ledger_heading") + "\n")
	reqs, _ := s.store.ListWorkRequests("", false, 100)
	cutoff := now.Add(-24 * time.Hour).Unix()
	usedL, shownL, hiddenL := 0, 0, 0
	for _, r := range reqs {
		if !jobstore.WorkRequestActive(r.State) && r.CreatedAt < cutoff {
			continue
		}
		line := stewardText(lang, "prime.ledger_line", "id", r.ID,
			"kind", requestKindLabel(lang, r.Kind), "state", requestStateLabel(lang, r.State),
			"item", r.WorkItemID, "session", orDash(r.SessionID), "by", orDash(r.By),
			"ago", ago(lang, now, r.CreatedAt)) + "\n"
		if usedL+len(line) > ledgerBudget {
			hiddenL++
			continue
		}
		out.WriteString(line)
		usedL += len(line)
		shownL++
	}
	if shownL == 0 && hiddenL == 0 {
		out.WriteString(stewardText(lang, "prime.ledger_empty") + "\n")
	}
	if hiddenL > 0 {
		st.Truncated = true
		out.WriteString(stewardText(lang, "prime.ledger_hidden", "n", hiddenL) + "\n")
	}
	st.RequestsShow = shownL
	remaining -= usedL

	// Last 24h of journal, newest first, in whatever is left.
	out.WriteString("\n## " + stewardText(lang, "prime.journal_heading") + "\n")
	journalBudget := max(remaining, 1024)
	entries, _ := s.store.ListRecentWorkJournal(cutoff, 300)
	usedJ, hiddenJ := 0, 0
	for _, e := range entries {
		line := stewardText(lang, "prime.journal_line",
			"time", time.Unix(e.At, 0).In(now.Location()).Format("15:04"), "item", e.WorkItemID,
			"kind", journalKindLabel(lang, e.Kind), "by", runesCap(e.By, 40),
			"text", runesCap(e.Text, 90)) + "\n"
		if usedJ+len(line) > journalBudget {
			hiddenJ++
			continue
		}
		out.WriteString(line)
		usedJ += len(line)
		st.JournalShown++
	}
	if len(entries) == 0 {
		out.WriteString(stewardText(lang, "prime.journal_empty") + "\n")
	}
	if hiddenJ > 0 {
		st.Truncated = true
		out.WriteString(stewardText(lang, "prime.journal_hidden", "n", hiddenJ) + "\n")
	}

	text := strings.TrimRight(out.String(), "\n")
	return Prime{Text: text, Bytes: len(text), Stats: st}, nil
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

// weekdayNames are the catalog keys of the weekdays, Sunday first.
var weekdayNames = [...]string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

func weekday(lang i18n.Lang, d time.Weekday) string {
	return stewardText(lang, "weekday."+weekdayNames[d])
}
