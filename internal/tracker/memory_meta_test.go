package tracker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func strp(v string) *string { return &v }

func TestMemoryMetaJSONRoundTrip(t *testing.T) {
	s := primeTestStore(t)
	ttl := 3 * 24 * time.Hour
	kw := []string{"发版", "release"}
	paths := []string{"web/**"}
	item, err := s.SetMemoryPatch("h1", MemoryPatch{Content: "交接正文", Kind: strp("handoff"), Summary: strp("一句话"), Source: strp("plan:p1"), WhenKeywords: &kw, WhenPaths: &paths, TTL: &ttl, Tags: []string{"web"}, By: "me"}, true)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Memory("h1")
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(item)
	b, _ := json.Marshal(got)
	if string(a) != string(b) || got.Kind != "handoff" || got.Summary != "一句话" || got.Source != "plan:p1" || got.When == nil || len(got.When.Keywords) != 2 || got.ExpiresAt == "" || got.CreatedAt == "" {
		t.Fatalf("round trip: %s vs %s", a, b)
	}
	// Legacy bodies without meta keep their exact bytes.
	raw := `{"key":"old","content":"c","updated_at":"2026-01-01T00:00:00Z","by":"x"}`
	var m Memory
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	if out, _ := json.Marshal(m); string(out) != raw {
		t.Fatalf("legacy bytes changed: %s", out)
	}
	// The jsonl line carries the new fields after the original ones.
	data, _ := os.ReadFile(filepath.Join(s.Dir, "memories.jsonl"))
	if !strings.Contains(string(data), `"by":"me","kind":"handoff","summary":"一句话"`) {
		t.Fatalf("wire order: %s", data)
	}
}

func TestSetMemoryPatchKeepsUnspecifiedFields(t *testing.T) {
	s := primeTestStore(t)
	paths := []string{"internal/**"}
	if _, err := s.SetMemoryPatch("r", MemoryPatch{Content: "v1", Kind: strp("rule"), Summary: strp("s"), Source: strp("issue:x-1"), WhenPaths: &paths, Tags: []string{"a"}}, true); err != nil {
		t.Fatal(err)
	}
	first, _ := s.Memory("r")
	time.Sleep(2 * time.Millisecond)
	got, err := s.SetMemoryPatch("r", MemoryPatch{Content: "v2"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "v2" || got.Kind != "rule" || got.Summary != "s" || got.Source != "issue:x-1" || got.When == nil || got.When.Paths[0] != "internal/**" || len(got.Tags) != 1 || got.CreatedAt != first.CreatedAt || got.UpdatedAt == first.UpdatedAt {
		t.Fatalf("kept fields: %+v", got)
	}
	// "-" style clear: empty slice / string.
	empty := []string{}
	got, _ = s.SetMemoryPatch("r", MemoryPatch{Content: "v3", Summary: strp(""), WhenPaths: &empty}, false)
	if got.Summary != "" || got.When != nil {
		t.Fatalf("clear: %+v", got)
	}
}

func TestMemoryWriteValidation(t *testing.T) {
	s := primeTestStore(t)
	long := "# 标题\n第一句很重要。" + strings.Repeat("长", 220)
	_, err := s.SetMemoryPatch("n", MemoryPatch{Content: long}, true)
	if err == nil || !strings.Contains(err.Error(), "--summary") || !strings.Contains(err.Error(), "第一句很重要。") {
		t.Fatalf("want summary error with candidate, got %v", err)
	}
	if _, err := s.SetMemoryPatch("h", MemoryPatch{Content: long, Kind: strp("handoff")}, true); err != nil {
		t.Fatalf("handoff may omit summary: %v", err)
	}
	if _, err := s.SetMemoryPatch("n", MemoryPatch{Content: long, Summary: strp("ok")}, true); err != nil {
		t.Fatal(err)
	}
	// An update keeps the stored summary, so it passes without --summary.
	if _, err := s.SetMemoryPatch("n", MemoryPatch{Content: long + "更多"}, true); err != nil {
		t.Fatalf("kept summary: %v", err)
	}
	if _, err := s.SetMemoryPatch("x", MemoryPatch{Content: "c", Kind: strp("bogus")}, true); err == nil {
		t.Fatal("invalid kind accepted")
	}
	ttl := time.Hour
	if _, err := s.SetMemoryPatch("x", MemoryPatch{Content: "c", TTL: &ttl}, true); err == nil {
		t.Fatal("ttl on a note accepted")
	}
	if _, err := s.SetMemoryPatch("x", MemoryPatch{Content: "c", Summary: strp(strings.Repeat("s", 81))}, true); err == nil {
		t.Fatal("summary over 80 accepted")
	}
}

func TestParseMemoryTTL(t *testing.T) {
	for in, want := range map[string]time.Duration{"14d": 14 * 24 * time.Hour, "2w": 14 * 24 * time.Hour, "36h": 36 * time.Hour} {
		if got, err := ParseMemoryTTL(in); err != nil || got != want {
			t.Fatalf("%s: %v %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "0d", "xd", "-1h"} {
		if _, err := ParseMemoryTTL(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestHandoffDefaultTTLAndExpiry(t *testing.T) {
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	m, err := ApplyMemoryPatch(nil, "h", MemoryPatch{Content: "c", Kind: strp("handoff")}, now)
	if err != nil {
		t.Fatal(err)
	}
	at, ok := MemoryExpiresAt(m.MemoryMeta, m.Tags, m.UpdatedAt)
	if !ok || !at.Equal(now.Add(DefaultHandoffTTL)) {
		t.Fatalf("default ttl: %v %v", at, ok)
	}
	if MemoryExpired(m.MemoryMeta, nil, m.UpdatedAt, now.Add(13*24*time.Hour)) || !MemoryExpired(m.MemoryMeta, nil, m.UpdatedAt, now.Add(15*24*time.Hour)) {
		t.Fatal("expiry boundary")
	}
	// Legacy handoff without expires_at: updated_at + 14d.
	legacy := MemoryMeta{Kind: "handoff"}
	if !MemoryExpired(legacy, nil, "2026-09-01T00:00:00Z", now) {
		t.Fatal("legacy handoff should be expired")
	}
	// Turning a handoff into a rule drops the expiry.
	r, _ := ApplyMemoryPatch(&m, "h", MemoryPatch{Content: "c", Kind: strp("rule")}, now)
	if r.ExpiresAt != "" {
		t.Fatalf("rule kept expiry: %+v", r)
	}
}

func TestEffectiveKindLegacyPrimeTag(t *testing.T) {
	if EffectiveMemoryKind(MemoryMeta{}, []string{"prime"}) != MemoryKindRule || EffectiveMemoryKind(MemoryMeta{}, nil) != MemoryKindNote || EffectiveMemoryKind(MemoryMeta{Kind: "note"}, []string{"prime"}) != MemoryKindNote {
		t.Fatal("kind mapping")
	}
}

func TestFallbackMemorySummary(t *testing.T) {
	cases := map[string]string{
		"【★gofer 会话交接·更新25】\n# 标题\n\nhttps://example.com/x\n- 真正的内容。后面不要": "真正的内容。",
		"First line. Second":       "First line.",
		"[link](http://a)\n正文没有句号": "正文没有句号",
		"## only heading":          "",
		strings.Repeat("长", 100):   strings.Repeat("长", 79) + "…",
	}
	for in, want := range cases {
		if got := FallbackMemorySummary(in); got != want {
			t.Fatalf("%q: got %q want %q", in, got, want)
		}
	}
	if DisplayMemorySummary(MemoryMeta{Summary: "written"}, "content") != "written" {
		t.Fatal("stored summary first")
	}
}

func TestMatchMemoryPath(t *testing.T) {
	cases := []struct {
		pattern, rel string
		want         bool
	}{
		{"web/**", "web", true},
		{"web/**", "web/src/app", true},
		{"web/**", "", false},
		{"web/**", "webx", false},
		{"internal/tunnel", "internal/tunnel/x", true},
		{"internal/*/x", "internal/a/x", true},
		{"internal/*/x", "internal/a/b/x", false},
		{"**/testdata", "a/b/testdata", true},
		{"**", "", true},
	}
	for _, c := range cases {
		if got := MatchMemoryPath(c.pattern, c.rel); got != c.want {
			t.Fatalf("%s vs %q: got %v", c.pattern, c.rel, got)
		}
	}
	meta := MemoryMeta{When: &MemoryWhen{Keywords: []string{"Release"}, Commands: []string{"git push"}}}
	if kw, ok := MemoryMatchesKeyword(meta, "准备 release 了"); !ok || kw != "Release" {
		t.Fatal("keyword match")
	}
	if _, ok := MemoryMatchesCommand(meta, "git push origin main"); !ok {
		t.Fatal("command match")
	}
}

func TestMemoryListLineAndDetail(t *testing.T) {
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	h := Memory{Key: "h", Content: "c", UpdatedAt: "2026-09-01T00:00:00Z", MemoryMeta: MemoryMeta{Kind: "handoff", Summary: "交接", Source: "plan:p"}}
	line := MemoryListLine(h, now)
	if !strings.Contains(line, "h [handoff] · 38 天前（已过期） · 交接") {
		t.Fatalf("ls line: %q", line)
	}
	detail := MemoryDetail(h, now)
	for _, want := range []string{"kind: handoff", "summary: 交接", "source: plan:p", "created: 2026-09-01", "expires:", "已过期", "---\nc\n"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("detail missing %q: %s", want, detail)
		}
	}
	legacy := Memory{Key: "p", Content: "x", Tags: []string{"prime"}, UpdatedAt: "2026-10-08T00:00:00Z"}
	if !strings.Contains(MemoryListLine(legacy, now), "p [rule]") || !strings.Contains(MemoryDetail(legacy, now), "由 prime 标签推断") {
		t.Fatal("legacy prime rule display")
	}
}

func TestListMemoriesFilteredByKind(t *testing.T) {
	s := primeTestStore(t)
	_ = s.UpdateMemories(func([]Memory) ([]Memory, error) {
		return []Memory{
			{Key: "a", Content: "x", MemoryMeta: MemoryMeta{Kind: "rule"}},
			{Key: "b", Content: "x", Tags: []string{"prime"}},
			{Key: "c", Content: "x", MemoryMeta: MemoryMeta{Summary: "needle here"}},
		}, nil
	})
	rules, _ := s.ListMemoriesFiltered(MemoryFilter{Kind: "rule"})
	if len(rules) != 2 {
		t.Fatalf("rules: %+v", rules)
	}
	hits, _ := s.ListMemoriesFiltered(MemoryFilter{Keyword: "NEEDLE"})
	if len(hits) != 1 || hits[0].Key != "c" {
		t.Fatalf("summary search: %+v", hits)
	}
}
