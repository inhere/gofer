package brief

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/i18n/prompts"
)

// The session-opening reference to the latest daily memory synthesis (design
// 2026-10-28-daily-memory-synthesis §6): one short block with the day, the run id, a bounded
// summary and the command to read more. It never carries highlights, source material or
// anything but the run's own (already scrubbed) summary sentence; with no recent successful
// synthesis there is no block at all.
const (
	// SynthesisPrimeMaxBytes bounds the whole block, heading and hint included.
	SynthesisPrimeMaxBytes = 600
	// SynthesisPrimeMaxAgeDays: a synthesis whose day is older than this is not referenced
	// (an old summary must not pass for today's state).
	SynthesisPrimeMaxAgeDays = 3
	// synthesisMinSummaryBytes: a summary cut shorter than this says nothing, so the block
	// is dropped instead.
	synthesisMinSummaryBytes = 24
)

// SynthesisRef is the input of RenderSynthesisIn.
type SynthesisRef struct {
	Day     string // YYYY-MM-DD of the synthesized business day
	ID      string // run id
	Summary string
}

// SynthesisFresh reports whether a synthesis of `day` (YYYY-MM-DD, local zone) is recent
// enough to reference at now. A malformed day is never fresh.
func SynthesisFresh(day string, now time.Time) bool {
	d, err := time.ParseInLocation("2006-01-02", day, now.Location())
	if err != nil {
		return false
	}
	return now.Before(d.AddDate(0, 0, 1+SynthesisPrimeMaxAgeDays))
}

// RenderSynthesisIn renders the block in lang ("\n" first, "\n" last, like the other prime
// sections) or "" when there is nothing to say or it does not fit: budget is the bytes the
// whole prime has left. An over-long summary is cut at a rune boundary with an ellipsis;
// when even the shortest useful summary does not fit, the whole block is omitted rather than
// squeezing out the prime's core rules.
func RenderSynthesisIn(lang i18n.Lang, r SynthesisRef, budget int) string {
	summary := strings.Join(strings.Fields(r.Summary), " ")
	if summary == "" || strings.TrimSpace(r.ID) == "" || strings.TrimSpace(r.Day) == "" {
		return ""
	}
	limit := min(SynthesisPrimeMaxBytes, budget)
	render := func(s string) string {
		out, err := prompts.Render(lang, prompts.NameMemorySynthesisPrime, prompts.MemorySynthesisPrimeData{Day: r.Day, ID: r.ID, Summary: s})
		if err != nil {
			return ""
		}
		return "\n" + out + "\n"
	}
	if out := render(summary); out != "" && len(out) <= limit {
		return out
	}
	fixed := len(render("…")) // everything but the summary text, the ellipsis included
	room := limit - fixed
	if fixed == 0 || room < synthesisMinSummaryBytes {
		return ""
	}
	cut := summary[:min(room, len(summary))]
	for !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	out := render(strings.TrimSpace(cut) + "…")
	if len(out) > limit {
		return ""
	}
	return out
}
