package tracker

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
)

func TestMemoryFlagUnflagAndContentClears(t *testing.T) {
	s := primeTestStore(t)
	_, err := s.SetMemoryPatch("r1", MemoryPatch{Content: "use make test", Kind: ptrTo(MemoryKindRule), By: "a"}, true)
	assert.Require(t, assert.NoErr(t, err))
	before, err := s.Memory("r1")
	assert.Require(t, assert.NoErr(t, err))

	_, err = NewMemoryFlag("  ", "bot", "", time.Now())
	assert.Err(t, err)

	for i := 0; i < MemoryFlagsMax+2; i++ {
		flag, err := NewMemoryFlag(fmt.Sprintf("reason %d", i), "bot", "job-1", time.Now())
		assert.Require(t, assert.NoErr(t, err))
		_, err = s.FlagMemory("r1", flag)
		assert.Require(t, assert.NoErr(t, err))
	}
	got, err := s.Memory("r1")
	assert.Require(t, assert.NoErr(t, err))
	assert.Len(t, got.Flags, MemoryFlagsMax)
	assert.Eq(t, fmt.Sprintf("reason %d", MemoryFlagsMax+1), got.Flags[0].Reason)
	assert.Eq(t, "job-1", got.Flags[0].Job)
	// A flag is a report, not a write: updated_at stays.
	assert.Eq(t, before.UpdatedAt, got.UpdatedAt)
	assert.Contains(t, MemoryDetail(got, time.Now()), "flags: 5")
	assert.Contains(t, MemoryListLine(got, time.Now()), "⚠ 待复核（reason 6）")

	// Same content (e.g. a summary edit) keeps the flags; new content clears them.
	_, err = s.SetMemoryPatch("r1", MemoryPatch{Content: "use make test", Summary: ptrTo("s"), By: "a"}, true)
	assert.Require(t, assert.NoErr(t, err))
	got, _ = s.Memory("r1")
	assert.Len(t, got.Flags, MemoryFlagsMax)
	_, err = s.SetMemoryPatch("r1", MemoryPatch{Content: "use make check", By: "a"}, true)
	assert.Require(t, assert.NoErr(t, err))
	got, _ = s.Memory("r1")
	assert.Empty(t, got.Flags)

	flag, _ := NewMemoryFlag("again", "bot", "", time.Now())
	_, err = s.FlagMemory("r1", flag)
	assert.Require(t, assert.NoErr(t, err))
	got, err = s.UnflagMemory("r1")
	assert.Require(t, assert.NoErr(t, err))
	assert.Empty(t, got.Flags)

	_, err = s.FlagMemory("missing", flag)
	assert.ErrMsgContains(t, err, "not found")
}

func TestMemoryFlagInjectionAndDoctor(t *testing.T) {
	flagged := MemoryMeta{Kind: MemoryKindRule, Flags: []MemoryFlag{{At: "2026-10-09T00:00:00Z", By: "bot", Job: "job-9", Reason: "make test 已改名 make check"}}}
	note := MemoryMeta{Summary: "web 构建", Flags: []MemoryFlag{{At: "2026-10-09T00:00:00Z", Reason: "路径已变"}}}
	items := []Memory{
		{Key: "r-verify", Content: "RULE FULL TEXT", UpdatedAt: "2026-10-01T00:00:00Z", MemoryMeta: flagged},
		{Key: "n-web", Content: "note body", UpdatedAt: "2026-10-01T00:00:00Z", MemoryMeta: note},
	}

	// Local prime: the rule keeps its full text behind the prefix, the note is an
	// index line with the prefix, and the rules area carries the flag hint.
	s := primeTestStore(t)
	assert.Require(t, assert.NoErr(t, s.UpdateMemories(func([]Memory) ([]Memory, error) { return items, nil })))
	body := primeWith(t, s, "")
	assert.Contains(t, body, "- ⚠ 待复核（make test 已改名 make check） r-verify: RULE FULL TEXT\n")
	assert.Contains(t, body, "⚠ 待复核（路径已变） n-web")
	assert.Contains(t, body, MemoryFlagHint)

	// Dispatched-job rules.
	rule := renderPrimeRule("POLICY", items, time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC))
	assert.Contains(t, rule, "- ⚠ 待复核（make test 已改名 make check） r-verify: RULE FULL TEXT\n")
	assert.Contains(t, rule, "- ⚠ 待复核（路径已变） n-web")
	assert.Contains(t, rule, MemoryFlagHint)

	// Scoped sections use the same view.
	scoped := RenderScopedPrimeSection("全局记忆", items, ScopedPrimeOptions{Budget: -1, SummaryLimit: -1})
	assert.Contains(t, scoped, "⚠ 待复核（make test 已改名 make check） r-verify: RULE FULL TEXT")

	report := DiagnoseMemories(items, DoctorOptions{Now: time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)})
	assert.Eq(t, []string{DoctorFlagged}, slugsOf(report, "r-verify"))
	text := FormatDoctorReport(report)
	assert.Contains(t, text, "flagged: 1 次 · 最近：make test 已改名 make check · job:job-9 · by bot · 2026-10-09")
	assert.True(t, ValidDoctorSlug(DoctorFlagged))
}

func TestSyncMergesMemoryFlags(t *testing.T) {
	base := Memory{Key: "k", Content: "c", UpdatedAt: "2026-10-01T00:00:00Z"}
	local := base
	local.Flags = []MemoryFlag{{At: "2026-10-02T00:00:00Z", Reason: "stale"}}
	var report SyncReport
	out := mergeMemories(map[string]Memory{"k": base}, map[string]Memory{"k": local}, map[string]Memory{"k": base}, &report)
	assert.Len(t, out, 1)
	assert.Eq(t, "stale", out[0].Flags[0].Reason)
	assert.Empty(t, report.Conflicts)
	// The server side cleared them (reviewed) while local is unchanged: cleared wins.
	out = mergeMemories(map[string]Memory{"k": local}, map[string]Memory{"k": local}, map[string]Memory{"k": base}, &report)
	assert.Empty(t, out[0].Flags)
	assert.True(t, strings.TrimSpace(MemoryFlagPrefix(out[0].MemoryMeta)) == "")
}

func ptrTo[T any](v T) *T { return &v }
