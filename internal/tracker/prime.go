package tracker

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inhere/gofer/internal/i18n"
)

// PrimeMaxBytes is the byte budget of one session-opening prime (and of the
// tracker part of a dispatched job's rules).
const PrimeMaxBytes = 8 << 10

func primeText(lang i18n.Lang, key string, args ...any) string {
	return i18n.T(lang, "prime."+key, args...)
}

func workPrimeHint(lang i18n.Lang) string { return primeText(lang, "header.work_hint") }
func trackerPrimeHint(lang i18n.Lang) string {
	return primeText(lang, "header.tracker_hint") + "\n" + memoryWriteHint(lang)
}
func primeTakeoverHint(lang i18n.Lang) string { return primeText(lang, "header.takeover") }
func memoryWriteHint(lang i18n.Lang) string   { return primeText(lang, "header.memory_write") }
func memoryFlagHint(lang i18n.Lang) string    { return primeText(lang, "memory.flag_hint") }

// PrimeWithHandoffSection appends the caller-provided best-effort server handoff
// section while preserving the existing prime bytes first. The caller is expected
// to have applied project selection, timeout and ordering before this seam.
func (s *Store) PrimeWithHandoffSection(handoff string) (string, error) {
	return s.PrimeWithHandoffSectionIn(i18n.Default, handoff)
}

// PrimeWithHandoffSectionIn is the language-aware handoff variant.
func (s *Store) PrimeWithHandoffSectionIn(lang i18n.Lang, handoff string) (string, error) {
	base, err := s.PrimeWith(PrimeOptions{Language: lang})
	if err != nil {
		return "", err
	}
	return appendHandoffSection(lang, base, handoff), nil
}

// PrimeWithHandoffFetch runs a best-effort handoff lookup supplied by the command
// layer. The callback owns server discovery/authentication; tracker only applies
// the two-second budget and the existing handoff-only truncation seam.
func (s *Store) PrimeWithHandoffFetch(ctx context.Context, fetch func(context.Context) (string, error)) (string, error) {
	base, err := s.Prime()
	if err != nil {
		return "", err
	}
	if fetch == nil {
		return base, nil
	}
	result := make(chan struct {
		section string
		err     error
	}, 1)
	go func() {
		section, err := fetch(ctx)
		result <- struct {
			section string
			err     error
		}{section, err}
	}()
	var section string
	select {
	case <-ctx.Done():
		return base, nil
	case fetched := <-result:
		section = fetched.section
		if fetched.err != nil {
			return base, nil
		}
	}
	if strings.TrimSpace(section) == "" {
		return base, nil
	}
	return appendHandoffSection(i18n.Default, base, section), nil
}

func appendHandoffSection(lang i18n.Lang, base, handoff string) string {
	heading := primeText(lang, "header.handoff_heading")
	marker := "\n" + primeText(lang, "header.handoff_cut") + "\n"
	section := "\n" + heading + "\n\n" + handoff
	if len([]byte(base))+len([]byte(section)) <= PrimeMaxBytes {
		return base + section
	}
	remaining := PrimeMaxBytes - len([]byte(base)) - len([]byte("\n"+heading+"\n\n")) - len([]byte(marker))
	if remaining < 0 {
		return base
	}
	return base + "\n" + heading + "\n\n" + utf8Prefix(handoff, remaining) + marker
}

func utf8Prefix(value string, byteLimit int) string {
	if byteLimit <= 0 {
		return ""
	}
	if len(value) <= byteLimit {
		return value
	}
	cut := value[:byteLimit]
	for !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

// AppendPrimeSections appends optional server-backed sections within the same
// bounded prime budget. Each section must include its heading and body.
func AppendPrimeSections(base string, sections ...string) string {
	return AppendPrimeSectionsIn(i18n.Default, base, sections...)
}

func AppendPrimeSectionsIn(lang i18n.Lang, base string, sections ...string) string {
	out := base
	for _, section := range sections {
		if strings.TrimSpace(section) == "" {
			continue
		}
		remaining := PrimeMaxBytes - len([]byte(out))
		if remaining <= 0 {
			return out
		}
		if len([]byte(section)) <= remaining {
			out += section
			continue
		}
		marker := "\n" + primeText(lang, "layout.scoped_cut_global") + "\n"
		keep := remaining - len([]byte(marker))
		if keep > 0 {
			out += utf8Prefix(section, keep) + marker
		} else {
			out += utf8Prefix(marker, remaining)
		}
		return out
	}
	return out
}

// ScopedPrimeSection is one server-scoped memory section of a dispatched job's
// tracker-prime: the job's project memories, then the machine's global ones.
type ScopedPrimeSection struct {
	Title string
	Items []Memory
	// LsHint is the `gofer memory ls …` command for the omitted-entries note.
	LsHint string
}

// ScopedPrimeSectionCutMark marks a section the byte budget had to cut, so the job knows
// the list it was given is not the whole story.
func ScopedPrimeSectionCutMark(title string) string {
	return ScopedPrimeSectionCutMarkIn(i18n.Default, title)
}

func ScopedPrimeSectionCutMarkIn(lang i18n.Lang, title string) string {
	return "\n" + i18n.T(lang, "prime.memory.scoped_cut", "title", title) + "\n"
}

// scopedPrimeSectionMin is the room a section needs before it is worth rendering at all:
// a section writes its heading and its 「另有 N 条」 note even when no entry fits, so a
// smaller budget would spend the rest of the total on an empty heading.
const scopedPrimeSectionMin = 200

// AppendScopedPrimeSections appends server-scoped memory sections to a job's
// tracker-prime body (base) within the shared PrimeMaxBytes total. The sections keep the
// order they are given in, each one is budgeted on its own out of what is left of the
// total (the caller's ScopedPrimeOptions.Budget is ignored) and marked with
// ScopedPrimeSectionCutMark when entries were dropped. A section with no room left is
// marked cut as well instead of being squeezed out silently.
func AppendScopedPrimeSections(base string, opts ScopedPrimeOptions, sections ...ScopedPrimeSection) string {
	out := base
	for _, sec := range sections {
		if len(sec.Items) == 0 {
			continue
		}
		cut := ScopedPrimeSectionCutMarkIn(opts.Language, sec.Title)
		if PrimeMaxBytes-len([]byte(out)) < scopedPrimeSectionMin+len([]byte(cut)) {
			if len([]byte(cut)) <= PrimeMaxBytes-len([]byte(out)) {
				out += cut
			}
			break
		}
		sectionOpts := opts
		if sec.LsHint != "" {
			sectionOpts.LsHint = sec.LsHint
		}
		sectionOpts.Budget = PrimeMaxBytes - len([]byte(out)) - len([]byte(cut))
		body, truncated := RenderScopedPrimeSectionTruncated(sec.Title, sec.Items, sectionOpts)
		if strings.TrimSpace(body) == "" {
			continue
		}
		out += body
		if truncated {
			out += cut
		}
	}
	// A note or a heading can overshoot its budget by a few bytes: the total is the hard
	// bound the job prompt relies on, so cut on a line boundary rather than exceed it.
	if len(out) > PrimeMaxBytes {
		out = utf8Prefix(out, PrimeMaxBytes)
		if i := strings.LastIndex(out, "\n"); i > 0 {
			out = out[:i+1]
		}
	}
	return out
}

func CommitPolicyText(policy string) (string, error) {
	return CommitPolicyTextIn(i18n.Default, policy)
}

func CommitPolicyTextIn(lang i18n.Lang, policy string) (string, error) {
	key := "header.commit_local"
	switch policy {
	case "ask":
		key = "header.commit_ask"
	case "none":
		key = "header.commit_none"
	case "", "local-commit":
	default:
		return "", fmt.Errorf("unknown commit_policy %q", policy)
	}
	return primeText(lang, key), nil
}

// PrimeRule is the tracker part of a dispatched job's mandatory rules: the
// commit policy, the kind=rule memories that APPLY TO the job, then a one-line
// index of the other applicable memories (so the job knows what it can
// `gofer memory show`). Expired handoffs and archived memories are left out. The
// whole body stays within PrimeMaxBytes-64; rules that do not fit drop to the
// index, index lines that do not fit are counted.
//
// cwd selects the material exactly like the session-opening prime (memoryView): a
// rule whose `when.paths` do not cover cwd is not injected in full, and the index
// lists only memories that apply to cwd (no `when.paths`, or a matching one),
// matching first. A job works in one subtree, so a memory about another subsystem
// is noise in its prompt — that is the whole point of the selection (gofer-krzy).
//
// A cwd this store cannot place inside its repository (unknown root, a path reached
// through a different spelling) selects nothing and keeps the unscoped behaviour:
// an unknown cwd must not silently drop rules.
//
// sel is the task the prime is FOR (gofer-11tz): the memories it hits are injected in
// full when their paths allow it and always appear first in the index, marked （相关）.
// Its zero value keeps the plain cwd-scoped prime.
func (s *Store) PrimeRule(cwd string, sel PrimeSelection) (string, error) {
	return s.PrimeRuleIn(i18n.Default, cwd, sel)
}

func (s *Store) PrimeRuleIn(lang i18n.Lang, cwd string, sel PrimeSelection) (string, error) {
	cfg, err := s.ReadConfig()
	if err != nil {
		return "", err
	}
	policy, err := CommitPolicyTextIn(lang, cfg.CommitPolicy)
	if err != nil {
		return "", err
	}
	memories, err := s.ReadMemories()
	if err != nil {
		return "", err
	}
	rel, inRepo := s.CwdRel(cwd)
	return renderPrimeRuleIn(lang, policy, memories, rel, inRepo, sel, time.Now()), nil
}

// renderPrimeRule renders the job rule body. rel/inRepo are the job cwd relative to
// the store's repository root (PrimeRule); inRepo=false means "unscoped", see above.
func renderPrimeRule(policy string, memories []Memory, rel string, inRepo bool, sel PrimeSelection, now time.Time) string {
	return renderPrimeRuleIn(i18n.Default, policy, memories, rel, inRepo, sel, now)
}

func renderPrimeRuleIn(lang i18n.Lang, policy string, memories []Memory, rel string, inRepo bool, sel PrimeSelection, now time.Time) string {
	lang = i18n.Resolve(lang)
	const limit = PrimeMaxBytes - 64
	view := newMemoryView(memories, memoryViewOptions{CwdRel: rel, CwdKnown: inRepo, Now: now, Selection: sel, Language: lang})
	var rules []Memory
	// The budget below is spent in relevance order (gofer-11tz): a rule the task hit is
	// written first, so a tight budget truncates the material for OTHER tasks.
	for _, m := range view.budgetOrder() {
		if m.EffectiveKind() != MemoryKindRule {
			continue
		}
		// A rule without `when` applies everywhere; one with `when.paths` only inside
		// the subtree it names — outside it the rule drops to the index below.
		if !inRepo || view.wantsFull(m) {
			rules = append(rules, m)
		}
	}
	var out strings.Builder
	out.WriteString(primeText(lang, "layout.commit_heading"))
	out.WriteString("：")
	out.WriteString(policy)
	out.WriteString("\n")
	if len(rules) > 0 {
		out.WriteString(primeText(lang, "memory.rules_heading"))
		out.WriteString(primeText(lang, "memory.rules_full"))
		out.WriteString("\n")
	}
	// Reserve room for the flag hint, the index heading and its truncation note.
	reserve := 160 + len(memoryFlagHint(lang)) + 1
	for _, m := range rules {
		key := m.Key
		if flag := MemoryFlagPrefixIn(lang, m.MemoryMeta); flag != "" {
			key = flag + " " + key
		}
		line := fmt.Sprintf("- %s: %s\n", key, m.Content)
		if out.Len()+len(line)+reserve > limit {
			// No room: leave it un-shown, so the index below still points at it.
			continue
		}
		out.WriteString(line)
		view.shown[m.Key] = true
	}
	if len(memories) > 0 {
		out.WriteString(memoryFlagHint(lang) + "\n")
	}
	// The index is the shared cwd-scoped selection (memoryView.indexEntries): only
	// memories that apply to the job's cwd, matches first. The rules that did not fit
	// above are un-shown, so they land here marked （规则）.
	entries := view.indexEntries(func(m Memory) bool {
		if MemoryExpired(m.MemoryMeta, m.Tags, m.UpdatedAt, now) {
			return false
		}
		// The repository root is the whole repository: a job there may touch any
		// subtree, so every memory stays findable in the index (rules still go in
		// full only when they match). Only `when.paths` scopes a memory to a subtree;
		// keywords / commands are hook triggers and do not hide it.
		// A memory the task SELECTION hit is listed too (gofer-11tz): the hit is what
		// makes it worth reading, and paths only decide whether it goes in full.
		return !inRepo || rel == "" || m.When == nil || len(m.When.Paths) == 0 || view.pathMatched(m) || view.relevant(m)
	})
	if len(entries) > 0 {
		out.WriteString(primeText(lang, "memory.index_heading") + "：\n")
	}
	for i, m := range entries {
		line := "- " + m.Key
		if flag := MemoryFlagPrefixIn(lang, m.MemoryMeta); flag != "" {
			line = "- " + flag + " " + m.Key
		}
		switch m.EffectiveKind() {
		case MemoryKindRule:
			line += primeText(lang, "memory.section_rule")
		case MemoryKindHandoff:
			line += primeText(lang, "memory.section_handoff")
		}
		// The task hit this memory (gofer-11tz): the hit, not the cwd, is why it is worth
		// reading — say so, so a reader knows where the material came from.
		if view.relevant(m) {
			line += primeText(lang, "memory.section_relevant")
		}
		if headline := DisplayMemoryHeadline(m.MemoryMeta, m.Content); headline != "" {
			line += " · " + headline
		}
		line += "\n"
		if out.Len()+len(line)+64 > limit {
			fmt.Fprintf(&out, "%s\n", primeText(lang, "memory.rule_more", "n", len(entries)-i))
			break
		}
		out.WriteString(line)
	}
	return out.String()
}
