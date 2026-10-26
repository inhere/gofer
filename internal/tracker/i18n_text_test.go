package tracker

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/i18n"
)

var hanPrime = regexp.MustCompile(`\p{Han}`)

// TestZhPrimeTextIsUnchanged is the pre-migration characterization: these expected bytes
// were captured from the old hard-coded renderer, not from the catalog.
func TestZhPrimeTextIsUnchanged(t *testing.T) {
	policy, err := CommitPolicyTextIn(i18n.ZhCN, "local-commit")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join([]string{
		primeText(i18n.ZhCN, "layout.commit_heading"), policy,
		workPrimeHint(i18n.ZhCN), trackerPrimeHint(i18n.ZhCN),
		primeTakeoverHint(i18n.ZhCN), MemoryFlagPrefixIn(i18n.ZhCN, MemoryMeta{Flags: []MemoryFlag{{Reason: "reason"}}}),
		AgeTextIn(i18n.ZhCN, "2026-10-01", time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)),
	}, "\n")
	want, err := os.ReadFile(filepath.Join("testdata", "prime-zh-characterization.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got != strings.TrimRight(string(want), "\n") {
		t.Errorf("zh prime text changed\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestEnglishPrimeTextHasNoCJK(t *testing.T) {
	s := primeTestStore(t)
	if err := s.WriteIssues([]Issue{{ID: "i-1", Title: "ship it", Status: "in_progress", UpdatedAt: "2026-10-08T00:00:00Z"}, {ID: "i-2", Title: "next", Status: "open"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateMemories(func([]Memory) ([]Memory, error) {
		return []Memory{{Key: "rule", Content: "follow the release checklist", MemoryMeta: MemoryMeta{Kind: MemoryKindRule}}, {Key: "note", Content: "plain note", UpdatedAt: "2026-10-08T00:00:00Z"}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	body, err := s.PrimeWith(PrimeOptions{Language: i18n.EN, Now: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC), WorkHint: true})
	if err != nil {
		t.Fatal(err)
	}
	if hanPrime.MatchString(body) {
		t.Errorf("English prime carries CJK: %q", body)
	}
	for _, text := range []string{
		primeText(i18n.EN, "layout.commit_heading"), trackerPrimeHint(i18n.EN),
		memoryWriteHint(i18n.EN), primeTakeoverHint(i18n.EN),
		MemoryFlagPrefixIn(i18n.EN, MemoryMeta{Flags: []MemoryFlag{{Reason: "reason"}}}),
		AgeTextIn(i18n.EN, "2026-10-01", time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)),
	} {
		if hanPrime.MatchString(text) {
			t.Errorf("English prime text carries CJK: %q", text)
		}
	}
}

func TestMigratedTrackerFilesHaveNoCJK(t *testing.T) {
	for _, file := range []string{"prime.go", "prime_layout.go", "prime_memory.go", "focus.go", "repo_block.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		fset := token.NewFileSet()
		af, err := parser.ParseFile(fset, file, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(af, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if ok && lit.Kind == token.STRING && hanPrime.MatchString(lit.Value) {
				t.Errorf("%s:%d still contains CJK; move generated text to prime.ini", file, fset.Position(lit.Pos()).Line)
			}
			return true
		})
	}
}
