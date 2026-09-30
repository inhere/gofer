package commands

import (
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/tracker"
)

func TestRepoStatusShowsPrimeSize(t *testing.T) {
	root := t.TempDir()
	s, _, err := tracker.Init(root, "prime", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateMemories(func([]tracker.Memory) ([]tracker.Memory, error) {
		return []tracker.Memory{{Key: "large", Content: strings.Repeat("x", 10000), Tags: []string{"prime"}, UpdatedAt: "2026-09-30"}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	out := trackerRunOK(t, root, "repo", "status")
	if !strings.Contains(out, "prime_bytes:") || !strings.Contains(out, "prime_truncated: true") {
		t.Fatalf("repo status must show prime size/truncation: %q", out)
	}
}
