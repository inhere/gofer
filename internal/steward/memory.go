package steward

import (
	"strings"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/i18n/prompts"
)

// Memory hygiene (design prime-memory-quality §2.6, P4): during a review the steward reads
// the server-side doctor findings of the mirrored repository memories and proposes cleanup
// cards (gofer_memory_suggest) that a person adopts or dismisses on 「今天」. Like the T4
// advice step it only runs when there is something new: a finding signature the steward
// was never shown. internal/today owns the findings (it imports this package), so the
// server wires them in through SetMemoryHygiene.

// MemoryHygieneLines caps the finding lines quoted in the review prompt.
const MemoryHygieneLines = 8

// MemoryHygiene is what the memory step of a review needs.
type MemoryHygiene struct {
	// Findings counts the flagged memories that still have an open action.
	Findings int
	// Signatures are tracker/key/action of every open action (sorted).
	Signatures []string
	// Lines are a few "tracker · key（kind）：slugs" lines for the prompt.
	Lines []string
	// Remaining is how many memory suggestions today still allows.
	Remaining int
}

// SetMemoryHygiene supplies the server-side memory doctor summary. nil (or an error)
// leaves the memory step out of every review.
func (s *Service) SetMemoryHygiene(fn func() (MemoryHygiene, error)) { s.memoryHygiene = fn }

// kvMemoryPresented holds the finding signatures the last review put before the steward.
const kvMemoryPresented = "steward.memory_presented"

// memoryPending returns the hygiene summary (nil when there is nothing to propose today)
// and whether any signature is new to the steward.
func (s *Service) memoryPending() (*MemoryHygiene, bool) {
	if s.memoryHygiene == nil {
		return nil, false
	}
	h, err := s.memoryHygiene()
	if err != nil || h.Findings == 0 || h.Remaining <= 0 || len(h.Signatures) == 0 {
		return nil, false
	}
	seen := map[string]bool{}
	for _, k := range strings.Split(s.kv(kvMemoryPresented), "\n") {
		seen[k] = true
	}
	fresh := false
	for _, k := range h.Signatures {
		if !seen[k] {
			fresh = true
			break
		}
	}
	return &h, fresh
}

// rememberMemoryPresented records the signatures a review has put before the steward.
func (s *Service) rememberMemoryPresented(h *MemoryHygiene) {
	if h != nil {
		s.setKV(kvMemoryPresented, strings.Join(h.Signatures, "\n"))
	}
}

// memorySection is the review step that proposes memory cleanup; empty without findings.
func memorySection(lang i18n.Lang, step int, h *MemoryHygiene) string {
	if h == nil || h.Findings == 0 || h.Remaining <= 0 {
		return ""
	}
	more := h.Findings - len(h.Lines)
	out := prompts.MustRender(lang, prompts.NameStewardMemory, prompts.StewardMemoryData{
		Step: step, Findings: h.Findings, Remaining: h.Remaining, Lines: h.Lines, More: max(more, 0),
	})
	return out + "\n"
}
