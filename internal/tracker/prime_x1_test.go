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
	if !strings.Contains(body, "full first\nfull second") || strings.Contains(body, "secret second line") || !strings.Contains(body, "summary: "+strings.Repeat("a", 79)+"…") || !strings.Contains(body, "全文：`gofer memory show <key>`") {
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
