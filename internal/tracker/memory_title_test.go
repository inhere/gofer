package tracker

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestMemoryTitleValidationAndTrim(t *testing.T) {
	s := primeTestStore(t)
	item, err := s.SetMemoryPatch("t1", MemoryPatch{Content: "c", Title: strp("  部署流程  ")}, true)
	if err != nil || item.Title != "部署流程" {
		t.Fatalf("trim: %+v %v", item, err)
	}
	// Omitted keeps, "" clears.
	if item, err = s.SetMemoryPatch("t1", MemoryPatch{Content: "c2"}, true); err != nil || item.Title != "部署流程" {
		t.Fatalf("keep: %+v %v", item, err)
	}
	if item, err = s.SetMemoryPatch("t1", MemoryPatch{Content: "c2", Title: strp("")}, true); err != nil || item.Title != "" {
		t.Fatalf("clear: %+v %v", item, err)
	}
	// 80 runes pass, 81 fail; line breaks fail.
	if _, err = s.SetMemoryPatch("t2", MemoryPatch{Content: "c", Title: strp(strings.Repeat("长", MemoryTitleMaxRunes))}, true); err != nil {
		t.Fatalf("80 runes: %v", err)
	}
	for name, title := range map[string]string{"long": strings.Repeat("长", MemoryTitleMaxRunes+1), "lf": "a\nb", "cr": "a\rb"} {
		if _, err = s.SetMemoryPatch("t3", MemoryPatch{Content: "c", Title: strp(title)}, true); err == nil {
			t.Fatalf("%s title accepted", name)
		}
	}
	if _, err := s.Memory("t3"); err == nil {
		t.Fatal("rejected write left a record")
	}
	// A legacy line without title stays byte-identical; a titled one adds the field.
	raw := `{"key":"old","content":"c","updated_at":"2026-01-01T00:00:00Z","by":"x"}`
	var m Memory
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	if out, _ := json.Marshal(m); string(out) != raw {
		t.Fatalf("legacy bytes changed: %s", out)
	}
	m.Title = "T"
	if out, _ := json.Marshal(m); !strings.Contains(string(out), `"title":"T"`) {
		t.Fatalf("title not serialised: %s", out)
	}
}

func TestMemoryTitleDisplayAndListSearch(t *testing.T) {
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	m := Memory{Key: "k", Content: "body", UpdatedAt: "2026-10-19T00:00:00Z", MemoryMeta: MemoryMeta{Title: "发版流程", Summary: "发版前读"}}
	if line := MemoryListLine(m, now); !strings.Contains(line, " · 发版流程 — 发版前读") {
		t.Fatalf("ls line: %q", line)
	}
	if d := MemoryDetail(m, now); !strings.Contains(d, "title: 发版流程\n") || !strings.Contains(d, "summary: 发版前读") {
		t.Fatalf("detail: %s", d)
	}
	if !MemoryMatchesFilter(m, MemoryFilter{Keyword: "发版流"}) || MemoryMatchesFilter(m, MemoryFilter{Keyword: "nope"}) {
		t.Fatal("keyword search must cover the title")
	}
}

func TestPrimeIndexLineWithTitle(t *testing.T) {
	s := primeTestStore(t)
	long := strings.Repeat("a", 100) + "\nsecond"
	items := []Memory{
		{Key: "plain", Content: long, UpdatedAt: "2026-09-29T00:00:00Z"},
		{Key: "both", Content: long, UpdatedAt: "2026-09-29T00:00:00Z", MemoryMeta: MemoryMeta{Title: "部署手册", Summary: "上线前读"}},
		{Key: "only", Content: long, UpdatedAt: "2026-09-29T00:00:00Z", MemoryMeta: MemoryMeta{Title: "仅标题"}},
	}
	if err := s.UpdateMemories(func([]Memory) ([]Memory, error) { return items, nil }); err != nil {
		t.Fatal(err)
	}
	body, err := s.Prime()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"- [其他] plain · " + strings.Repeat("a", 79) + "…", // regression: no title, unchanged
		"- [其他] both · 部署手册 — 上线前读 · ",
		"- [其他] only · 仅标题 · ",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("prime missing %q in %q", want, body)
		}
	}
	if strings.Contains(body, "仅标题 — ") {
		t.Fatalf("title-only row must not get a derived summary: %q", body)
	}
}
