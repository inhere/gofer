// Package sources holds the read-only material adapters of daily memory synthesis. Every
// adapter implements memorysynthesis.Source over a narrow reader interface (satisfied by
// *jobstore.Store or the today service), so this package imports the data layer but no
// entry layer (G021/G022). Adapters only select allow-listed structured fields: never a
// prompt, argv, environment, attachment or transcript. Secret scrubbing and the byte budget
// are applied afterwards by memorysynthesis.Build.
package sources

import (
	"strconv"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/memorysynthesis"
)

// Config is shared by every adapter: the server time zone and lookback that define the
// business-day window, and the per-item text caps.
type Config struct {
	Loc      *time.Location
	Lookback time.Duration
}

func (c Config) window(day string) (memorysynthesis.Window, error) {
	return memorysynthesis.DayWindow(day, c.Loc, c.Lookback)
}

// Kinds written by the adapters.
const (
	KindWorkItem    = "work_item"
	KindWorkJournal = "work_journal"
	KindJobReport   = "job_report"
	KindPlan        = "plan"
	KindPlanTodo    = "plan_todo"
	KindMemoryFind  = "memory_finding"
	KindMemorySugg  = "memory_suggestion"
	KindPrevious    = "previous_synthesis"
)

func stamp(w memorysynthesis.Window, unix int64) string {
	if unix <= 0 {
		return ""
	}
	return time.Unix(unix, 0).In(w.Start.Location()).Format(time.RFC3339)
}

// kv renders non-empty "label: value" lines joined by newlines.
func kv(pairs ...string) string {
	var b strings.Builder
	for i := 0; i+1 < len(pairs); i += 2 {
		v := strings.TrimSpace(pairs[i+1])
		if v == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(pairs[i])
		b.WriteString(": ")
		b.WriteString(v)
	}
	return b.String()
}

// tailLines keeps the last n lines of s (the END of a report is where the conclusion is).
func tailLines(s string, n int) string {
	s = strings.TrimRight(strings.ToValidUTF8(s, ""), "\n ")
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
