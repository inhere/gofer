package tracker

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// cliMemoryGolden is the zh-CN text the memory commands print (`memory ls`,
// `memory show`, `memory ls --archived`, `memory doctor`, `memory scope`, the
// age column and the `memory add` summary rule). It was generated from the code
// BEFORE those messages moved into internal/i18n (GOFER_GOLDEN_UPDATE=1), so an
// existing zh-CN deployment keeps exactly the text it had
// (docs/design/2026-10-27-i18n-design.md §11 S2c).
var cliMemoryGolden = filepath.Join("testdata", "cli-memory-zh-CN.golden")

// TestZhMemoryCliTextIsUnchanged pins that text byte for byte.
func TestZhMemoryCliTextIsUnchanged(t *testing.T) {
	t.Setenv("GOFER_LANG", "zh-CN")
	got := collectMemoryCliText()
	if os.Getenv("GOFER_GOLDEN_UPDATE") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cliMemoryGolden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(cliMemoryGolden)
	if err != nil {
		t.Fatalf("read golden (regenerate with GOFER_GOLDEN_UPDATE=1): %v", err)
	}
	if got != string(want) {
		t.Errorf("zh-CN memory text changed (regenerate with GOFER_GOLDEN_UPDATE=1 only when the change is intended)\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// collectMemoryCliText renders one block per case, in a fixed order.
func collectMemoryCliText() string {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	at := func(days int) string { return now.AddDate(0, 0, -days).Format(time.RFC3339) }

	plain := Memory{Key: "k-plain", Content: "plain content", Tags: []string{"t1"}, UpdatedAt: at(1), By: "suga",
		MemoryMeta: MemoryMeta{Source: "job:j1"}}
	handoff := Memory{Key: "k-handoff", Content: "handoff body", Tags: []string{"handoff"}, UpdatedAt: at(30),
		MemoryMeta: MemoryMeta{Kind: MemoryKindHandoff}}
	future := Memory{Key: "k-live-handoff", Content: "live", Tags: []string{"handoff"}, UpdatedAt: at(1),
		MemoryMeta: MemoryMeta{Kind: MemoryKindHandoff, ExpiresAt: at(-3)}}
	stale := Memory{Key: "k-stale", Content: "an old note", UpdatedAt: at(100)}
	flagged := Memory{Key: "k-flagged", Content: "flagged body", UpdatedAt: at(2),
		MemoryMeta: MemoryMeta{Flags: []MemoryFlag{{At: at(1), By: "agent", Job: "j9", Reason: "the path is gone"}}}}
	imported := Memory{Key: "k-imported", Content: "imported body", UpdatedAt: at(5),
		MemoryMeta: MemoryMeta{CreatedUnknown: true, CreatedAt: at(5)}}
	inferred := Memory{Key: "k-inferred", Content: "inferred kind", Tags: []string{"prime"}, UpdatedAt: at(2)}
	derived := Memory{Key: "k-derived", Content: strings.Repeat("这条记忆没有摘要。", 30), UpdatedAt: at(2)}

	cases := []struct{ name, text string }{
		{"list/plain", MemoryListLine(plain, now)},
		{"list/expired-handoff", MemoryListLine(handoff, now)},
		{"list/stale-note", MemoryListLine(stale, now)},
		{"list/flagged", MemoryListLine(flagged, now)},
		{"list/created-unknown", MemoryListLine(imported, now)},
		{"detail/plain", MemoryDetail(plain, now)},
		{"detail/live-handoff", MemoryDetail(future, now)},
		{"detail/expired-handoff", MemoryDetail(handoff, now)},
		{"detail/flagged", MemoryDetail(flagged, now)},
		{"detail/kind-inferred", MemoryDetail(inferred, now)},
		{"detail/derived-summary", MemoryDetail(derived, now)},
		{"archive/line", ArchivedMemoryListLine(ArchivedMemory{Memory: plain, ArchivedAt: at(0), ArchiveReason: "superseded"}, now)},
		{"doctor/report", FormatDoctorReport(DoctorReport{
			Checked: 2, Flagged: 2, Suppressed: 1,
			Memories: []DoctorEntry{
				{Key: "k-stale", Kind: MemoryKindNote, Summary: "an old note", Findings: []DoctorFinding{
					{Slug: DoctorNoteStale, Detail: "updated " + AgeText(stale.UpdatedAt, now)},
					{Slug: DoctorDuplicate, Detail: "similar to k-stale2 (0.75)"},
				}},
				{Key: "k-flagged", Kind: MemoryKindRule, Findings: []DoctorFinding{
					{Slug: DoctorFlagged, Detail: flaggedDetail(flagged.Flags)},
				}},
			},
		})},
		{"age/today", AgeText(at(0), now)},
		{"age/days", AgeText(at(5), now)},
		{"scope/confidence", strings.Join([]string{
			ScopeConfidenceLabel(ScopeConfidenceHigh), ScopeConfidenceLabel(ScopeConfidenceMedium), ScopeConfidenceLabel("low"),
		}, " ")},
		{"write/summary-required", errText(ValidateMemoryForWrite(derived))},
	}

	var sb strings.Builder
	for _, c := range cases {
		fmt.Fprintf(&sb, "=== %s\n%s\n", c.name, c.text)
	}
	return sb.String()
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
