package tracker

import "strings"

// prime_selection.go is gofer-11tz: picking the material an injected prime is FOR.
//
// A prime used to be scoped by the cwd alone (which `when.paths` the working directory
// falls under). A dispatched job knows more than its cwd: the dispatcher names the
// subsystems / languages / modules the task touches (`--prime-keywords`, the
// GOFER_PRIME_KEYWORDS environment variable, the plan item), and the task itself carries
// words in its title and its opening prompt. Those are matched against what a memory
// declares — its `when.keywords` (the trigger its author wrote for exactly this) and its
// key / tags / summary — so the material a job most needs is injected in full and ranked
// first, while everything else stays listed exactly as before.
//
// A hit never REMOVES a memory: a relevant memory without keyword metadata would be lost
// that way, and the index is cheap.

// PrimeSelectionTextBytes is how much of the prompt the selection keeps: enough for the
// opening paragraph that names the goal, the subsystem and the files, without re-reading
// a whole task book (a rendered template can be tens of kilobytes).
const PrimeSelectionTextBytes = 2000

// PrimeSelectionMaxKeywords bounds a keyword list (a run-away flag or environment value
// must not turn into a 10k-term scan per memory).
const PrimeSelectionMaxKeywords = 64

// PrimeSelection is what a task brings to a prime: the keywords its dispatcher named, and
// the task's own words (the title plus the head of the prompt). Text is the string the
// memories' `when.keywords` are matched against; Keywords are matched against a memory's
// key / tags / summary as well.
type PrimeSelection struct {
	Keywords []string
	Text     string
}

// empty reports whether the selection says nothing — then every caller keeps the plain
// cwd-scoped behaviour, byte for byte.
func (s PrimeSelection) empty() bool {
	return len(s.Keywords) == 0 && strings.TrimSpace(s.Text) == ""
}

// NewPrimeSelection builds a task's selection: the keywords (trimmed, deduplicated, empty
// entries dropped, at most PrimeSelectionMaxKeywords — they also join the matching text,
// so a memory whose `when.keywords` carries one of them matches even without a prompt),
// the title, and the head of the prompt.
func NewPrimeSelection(keywords []string, title, prompt string) PrimeSelection {
	kws := normalizePrimeKeywords(keywords)
	head := prompt
	if len(head) > PrimeSelectionTextBytes {
		head = utf8Prefix(head, PrimeSelectionTextBytes)
	}
	parts := make([]string, 0, len(kws)+2)
	parts = append(parts, kws...)
	if t := strings.TrimSpace(title); t != "" {
		parts = append(parts, t)
	}
	if h := strings.TrimSpace(head); h != "" {
		parts = append(parts, h)
	}
	return PrimeSelection{Keywords: kws, Text: strings.Join(parts, "\n")}
}

// KeywordsOnly is the selection of a bare keyword list — what `gofer repo prime
// --keywords` has at a session opening, where there is no job title or prompt yet.
func KeywordsOnly(keywords []string) PrimeSelection {
	return NewPrimeSelection(keywords, "", "")
}

// normalizePrimeKeywords trims, deduplicates (case-insensitively, keeping the first
// spelling) and caps the list.
func normalizePrimeKeywords(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, raw := range in {
		kw := strings.TrimSpace(raw)
		if kw == "" {
			continue
		}
		key := strings.ToLower(kw)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, kw)
		if len(out) >= PrimeSelectionMaxKeywords {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// SplitPrimeKeywords parses a comma-separated keyword list (the CLI flag, the
// GOFER_PRIME_KEYWORDS environment variable). An item may carry its own whitespace
// (`rux, prime` is two keywords); empty items are dropped.
func SplitPrimeKeywords(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return normalizePrimeKeywords(strings.Split(value, ","))
}

// MemoryMatchesSelection reports whether a memory is relevant to the task behind sel:
//
//   - one of the memory's own `when.keywords` triggers appears in the task text, or
//   - one of the dispatcher's explicit keywords is carried by the memory — in its key,
//     one of its tags, its summary, or one of its own `when.keywords`.
//
// Everything is compared case-insensitively. A memory with no metadata at all can be
// matched by the second rule (its key or summary), which is what makes a plain memory
// about a subsystem reachable by naming that subsystem.
func MemoryMatchesSelection(m Memory, sel PrimeSelection) bool {
	if sel.empty() {
		return false
	}
	if text := strings.TrimSpace(sel.Text); text != "" {
		if _, ok := MemoryMatchesKeyword(m.MemoryMeta, text); ok {
			return true
		}
	}
	if len(sel.Keywords) == 0 {
		return false
	}
	// The displayed summary, not only the stored one: a long memory without --summary
	// shows its first sentence in every index, so that is where a reader looks for the
	// subject too.
	hay := strings.ToLower(m.Key + "\n" + strings.Join(m.Tags, "\n") + "\n" + DisplayMemorySummary(m.MemoryMeta, m.Content))
	for _, kw := range sel.Keywords {
		if strings.Contains(hay, strings.ToLower(kw)) {
			return true
		}
		// A memory whose trigger word sits inside the keyword ("rux-plot" matches a
		// memory declaring `rux`) — the same direction MemoryMatchesKeyword uses.
		if _, ok := MemoryMatchesKeyword(m.MemoryMeta, kw); ok {
			return true
		}
	}
	return false
}
