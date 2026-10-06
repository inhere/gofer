package tracker

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

func primeTestStore(t *testing.T) *Store {
	t.Helper()
	s, _, err := Init(t.TempDir(), "prime", true)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(s.Dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "prime:") {
		t.Fatalf("new tracker should omit optional prime config: %s", data)
	}
	return s
}

func TestPrimeLimitsActiveIssues(t *testing.T) {
	s := primeTestStore(t)
	issues := make([]Issue, 12)
	for i := range issues {
		issues[i] = Issue{ID: fmt.Sprintf("item-%02d", i), Title: fmt.Sprintf("Issue %02d", i), Status: "in_progress", Priority: i % 3, UpdatedAt: fmt.Sprintf("2026-09-%02dT00:00:00Z", i+1)}
	}
	if err := s.WriteIssues(issues); err != nil {
		t.Fatal(err)
	}
	body, err := s.Prime()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(body, "- item-") != 10 || !strings.Contains(body, "共 12 条，`gofer issue ls` 查看全部") {
		t.Fatalf("active issue limit/notice: %q", body)
	}
	if strings.Index(body, "item-09") > strings.Index(body, "item-00") || strings.Contains(body, "item-02") {
		t.Fatalf("active issue priority/update order: %q", body)
	}
}

func TestPrimeMemorySummaryVsFullByTag(t *testing.T) {
	s := primeTestStore(t)
	long := strings.Repeat("a", 100) + "\nsecret second line"
	items := []Memory{
		{Key: "full", Content: "full first\nfull second", Tags: []string{"prime"}, UpdatedAt: "2026-09-30T00:00:00Z"},
		{Key: "summary", Content: long, UpdatedAt: "2026-09-29T00:00:00Z"},
	}
	if err := s.UpdateMemories(func([]Memory) ([]Memory, error) { return items, nil }); err != nil {
		t.Fatal(err)
	}
	body, err := s.Prime()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "full first\nfull second") || strings.Contains(body, "secret second line") || !strings.Contains(body, "summary: "+strings.Repeat("a", 68)+"…") || !strings.Contains(body, "全文：`gofer memory show <key>`") {
		t.Fatalf("memory full/summary formatting: %q", body)
	}
}

func TestPrimeConfigToggles(t *testing.T) {
	s := primeTestStore(t)
	cfg, err := s.ReadConfig()
	if err != nil {
		t.Fatal(err)
	}
	falseValue, one := false, 1
	cfg.Prime = PrimeConfig{Issues: &falseValue, ReadyLimit: &one, MemorySummaryLimit: &one, Handoff: &falseValue, ScopedMemory: &falseValue}
	b, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir, "config.yaml"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteIssues([]Issue{{ID: "active", Title: "Active", Status: "in_progress"}, {ID: "ready-a", Title: "Ready A", Status: "open"}, {ID: "ready-b", Title: "Ready B", Status: "open"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateMemories(func([]Memory) ([]Memory, error) {
		return []Memory{{Key: "one", Content: "One", UpdatedAt: "2026-09-30"}, {Key: "two", Content: "Two", UpdatedAt: "2026-09-29"}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	body, err := s.Prime()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "## 进行中/已认领 issue") || strings.Contains(body, "active") || !strings.Contains(body, "ready-a") || strings.Contains(body, "ready-b") || !strings.Contains(body, "one:") || strings.Contains(body, "two:") {
		t.Fatalf("prime config toggles/limits: %q", body)
	}
}

// A migrated bd repository can carry dozens of memories: they must not push the
// in-progress / ready sections out of the byte budget, and the memories left out
// by memory_summary_limit are counted with a pointer to the search command.
func TestPrimeKeepsIssueSectionsWhenMemoriesAreMany(t *testing.T) {
	s := primeTestStore(t)
	limit := 5
	if err := s.UpdateConfig(func(c *Config) { c.Prime.MemorySummaryLimit = &limit }); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteIssues([]Issue{{ID: "p-1", Title: "Doing it", Status: "in_progress"}, {ID: "p-2", Title: "Next up", Status: "open", Priority: 1}}); err != nil {
		t.Fatal(err)
	}
	mem := make([]Memory, 60)
	for i := range mem {
		mem[i] = Memory{Key: fmt.Sprintf("key-%02d", i), Content: strings.Repeat("长", 300), UpdatedAt: "2026-09-30T00:00:00Z"}
	}
	if err := s.UpdateMemories(func([]Memory) ([]Memory, error) { return mem, nil }); err != nil {
		t.Fatal(err)
	}
	body, err := s.Prime()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "p-1 [in_progress] Doing it") || !strings.Contains(body, "p-2 P1 Next up") {
		t.Fatalf("issue sections lost:\n%s", body)
	}
	if strings.Count(body, "- key-") != 5 || !strings.Contains(body, "另有 55 条记忆未列出：`gofer memory ls <关键字>` 搜索") {
		t.Fatalf("memory summary limit/omitted notice:\n%s", body)
	}
	if len([]byte(body)) > PrimeMaxBytes || !strings.Contains(body, "gofer memory ls <关键字>") {
		t.Fatalf("size/hint: %d", len(body))
	}
}

// Without a limit the budget still protects the issue sections.
func TestPrimeBudgetReservesIssueRowsBeforeMemory(t *testing.T) {
	s := primeTestStore(t)
	if err := s.WriteIssues([]Issue{{ID: "p-1", Title: "Doing it", Status: "in_progress"}}); err != nil {
		t.Fatal(err)
	}
	mem := make([]Memory, 80)
	for i := range mem {
		mem[i] = Memory{Key: fmt.Sprintf("key-%02d", i), Content: strings.Repeat("长", 300), UpdatedAt: "2026-09-30T00:00:00Z"}
	}
	if err := s.UpdateMemories(func([]Memory) ([]Memory, error) { return mem, nil }); err != nil {
		t.Fatal(err)
	}
	body, _ := s.Prime()
	if !strings.Contains(body, "p-1 [in_progress] Doing it") || len([]byte(body)) > PrimeMaxBytes || !strings.Contains(body, "截断") {
		t.Fatalf("budget:\n%s", body)
	}
}
